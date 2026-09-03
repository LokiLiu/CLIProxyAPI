package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const baseSystemPrompt = "You are a model backend for an upstream agent harness. Follow the supplied system and developer instructions. Do not claim access to tools, files, skills, memory, or project context unless they are explicitly present in this request. Return only the next assistant message."

var invalidToolName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []chatMessage   `json:"messages"`
	Tools           []chatTool      `json:"tools"`
	ToolChoice      json.RawMessage `json:"tool_choice"`
	MaxTokens       int             `json:"max_tokens"`
	ReasoningEffort string          `json:"reasoning_effort"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []chatToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Function chatToolFunction `json:"function"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Arguments   json.RawMessage `json:"arguments"`
}

func BuildInvocation(raw []byte, routedModel string) (Invocation, error) {
	var request chatRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return Invocation{}, fmt.Errorf("decode chat completions request: %w", err)
	}
	model := strings.TrimSpace(routedModel)
	if model == "" {
		model = strings.TrimSpace(request.Model)
	}
	if model == "" {
		return Invocation{}, fmt.Errorf("model is required")
	}
	if len(request.Messages) == 0 {
		return Invocation{}, fmt.Errorf("messages must not be empty")
	}

	plan, errPlan := buildToolPlan(request.Tools, request.ToolChoice)
	if errPlan != nil {
		return Invocation{}, errPlan
	}
	system := make([]string, 0)
	conversation := make([]map[string]any, 0, len(request.Messages))
	attachments := make([]Attachment, 0)
	for _, message := range request.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		content, errContent := contentText(message.Content, &attachments)
		if errContent != nil {
			return Invocation{}, errContent
		}
		switch role {
		case "system", "developer":
			if content != "" {
				system = append(system, strings.ToUpper(role[:1])+role[1:]+" instructions:\n"+content)
			}
		case "user":
			conversation = append(conversation, map[string]any{"role": role, "content": content})
		case "assistant":
			entry := map[string]any{"role": role, "content": content}
			if len(message.ToolCalls) > 0 {
				entry["tool_calls"] = message.ToolCalls
			}
			conversation = append(conversation, entry)
		case "tool":
			conversation = append(conversation, map[string]any{
				"role":         role,
				"tool_call_id": message.ToolCallID,
				"content":      content,
			})
		default:
			return Invocation{}, fmt.Errorf("unsupported message role %q", message.Role)
		}
	}

	systemPrompt := baseSystemPrompt
	if len(system) > 0 {
		systemPrompt += "\n\n" + strings.Join(system, "\n\n")
	}
	if instruction := toolInstruction(plan); instruction != "" {
		systemPrompt += "\n\n" + instruction
	}
	conversationJSON, errMarshal := json.MarshalIndent(map[string]any{"messages": conversation}, "", "  ")
	if errMarshal != nil {
		return Invocation{}, fmt.Errorf("encode conversation: %w", errMarshal)
	}
	prompt := "Complete the conversation below and return only the next assistant message.\n\n" + string(conversationJSON)
	return Invocation{
		Model:           model,
		SystemPrompt:    systemPrompt,
		Prompt:          prompt,
		MaxTokens:       request.MaxTokens,
		ReasoningEffort: strings.ToLower(strings.TrimSpace(request.ReasoningEffort)),
		Tools:           plan,
		Attachments:     attachments,
	}, nil
}

func buildToolPlan(tools []chatTool, rawChoice json.RawMessage) (ToolPlan, error) {
	plan := ToolPlan{NameBySDK: make(map[string]string)}
	for _, item := range tools {
		if item.Type != "" && item.Type != "function" {
			return ToolPlan{}, fmt.Errorf("unsupported tool type %q", item.Type)
		}
		name := strings.TrimSpace(item.Function.Name)
		if name == "" {
			return ToolPlan{}, fmt.Errorf("tool function name is required")
		}
		sdkName := invalidToolName.ReplaceAllString(name, "_")
		if sdkName == "" {
			return ToolPlan{}, fmt.Errorf("invalid tool name %q", name)
		}
		if _, duplicate := plan.NameBySDK[sdkName]; duplicate {
			return ToolPlan{}, fmt.Errorf("duplicate normalized tool name %q", sdkName)
		}
		parameters := map[string]any{"type": "object", "properties": map[string]any{}}
		if len(item.Function.Parameters) > 0 {
			if err := json.Unmarshal(item.Function.Parameters, &parameters); err != nil {
				return ToolPlan{}, fmt.Errorf("invalid parameters for tool %s: %w", name, err)
			}
		}
		plan.Specs = append(plan.Specs, ToolSpec{
			Name:        name,
			SDKName:     sdkName,
			Description: item.Function.Description,
			Parameters:  parameters,
		})
		plan.NameBySDK[sdkName] = name
	}
	if len(plan.Specs) == 0 || len(rawChoice) == 0 || string(rawChoice) == "null" {
		return plan, nil
	}
	var text string
	if json.Unmarshal(rawChoice, &text) == nil {
		switch text {
		case "none":
			return ToolPlan{NameBySDK: make(map[string]string)}, nil
		case "required":
			plan.Required = true
		}
		return plan, nil
	}
	var choice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(rawChoice, &choice); err != nil {
		return ToolPlan{}, fmt.Errorf("invalid tool_choice: %w", err)
	}
	if choice.Type == "function" {
		for _, spec := range plan.Specs {
			if spec.Name == choice.Function.Name {
				plan.Selected = spec.Name
				plan.SelectedSDK = spec.SDKName
				plan.Required = true
				return plan, nil
			}
		}
		return ToolPlan{}, fmt.Errorf("tool_choice references unknown function %q", choice.Function.Name)
	}
	return plan, nil
}

func toolInstruction(plan ToolPlan) string {
	if len(plan.Specs) == 0 {
		return "Do not call any tools."
	}
	const concreteNames = "Call concrete function names directly; never call wrappers named tool_calls, tool_call, function_calls, function_call, mcp__openai_tools, full_turn, or punctuation-only names such as []. The upstream harness executes the functions."
	if plan.Selected != "" {
		return fmt.Sprintf("Call the external function %s. %s", plan.Selected, concreteNames)
	}
	if plan.Required {
		return "Call at least one provided external function. " + concreteNames
	}
	return "Use a provided external function when needed. " + concreteNames
}

func contentText(raw json.RawMessage, attachments *[]Attachment) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var parts []map[string]any
	if json.Unmarshal(raw, &parts) != nil {
		return "", fmt.Errorf("message content must be text or an array of text parts")
	}
	var out strings.Builder
	for _, part := range parts {
		kind, _ := part["type"].(string)
		switch kind {
		case "text", "input_text", "output_text":
			value, _ := part["text"].(string)
			out.WriteString(value)
		case "image_url", "input_image", "image":
			value := ""
			if imageURL, ok := part["image_url"].(string); ok {
				value = imageURL
			} else if imageURL, ok := part["image_url"].(map[string]any); ok {
				value, _ = imageURL["url"].(string)
			} else if source, ok := part["source"].(map[string]any); ok {
				if sourceType, _ := source["type"].(string); strings.EqualFold(sourceType, "base64") {
					mediaType, _ := source["media_type"].(string)
					data, _ := source["data"].(string)
					marker, err := appendBase64Image(attachments, mediaType, data)
					if err != nil {
						return "", err
					}
					out.WriteString(marker)
					continue
				}
			}
			if marker, handled, err := parseDataImageURL(value, attachments); handled {
				if err != nil {
					return "", err
				}
				out.WriteString(marker)
			} else if strings.TrimSpace(value) != "" {
				out.WriteString("[Image URL: " + value + "]")
			} else {
				return "", fmt.Errorf("image URL or base64 source is required")
			}
		case "audio", "input_audio":
			return "", fmt.Errorf("qoder CLI provider does not support %s message input", kind)
		default:
			return "", fmt.Errorf("unsupported message content type %q", kind)
		}
	}
	return out.String(), nil
}
