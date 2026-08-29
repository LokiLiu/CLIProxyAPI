package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

type anthropicRequest struct {
	Model      string              `json:"model"`
	System     json.RawMessage     `json:"system"`
	Messages   []anthropicMessage  `json:"messages"`
	Tools      []anthropicTool     `json:"tools"`
	ToolChoice anthropicToolChoice `json:"tool_choice"`
	MaxTokens  int                 `json:"max_tokens"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

func BuildAnthropicInvocation(raw []byte, routedModel string) (Invocation, error) {
	var request anthropicRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return Invocation{}, fmt.Errorf("decode Messages request: %w", err)
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

	plan, errPlan := buildAnthropicToolPlan(request.Tools, request.ToolChoice)
	if errPlan != nil {
		return Invocation{}, errPlan
	}
	systemText, errSystem := anthropicTextContent(request.System, "system")
	if errSystem != nil {
		return Invocation{}, errSystem
	}
	conversation := make([]map[string]any, 0, len(request.Messages))
	attachments := make([]Attachment, 0)
	for _, message := range request.Messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "user" && role != "assistant" {
			return Invocation{}, fmt.Errorf("unsupported Messages role %q", message.Role)
		}
		content, errContent := normalizeAnthropicContent(message.Content, role, &attachments)
		if errContent != nil {
			return Invocation{}, errContent
		}
		conversation = append(conversation, map[string]any{"role": role, "content": content})
	}

	systemPrompt := baseSystemPrompt
	if systemText != "" {
		systemPrompt += "\n\nSystem instructions:\n" + systemText
	}
	if instruction := toolInstruction(plan); instruction != "" {
		systemPrompt += "\n\n" + instruction
	}
	conversationJSON, errMarshal := json.MarshalIndent(map[string]any{"messages": conversation}, "", "  ")
	if errMarshal != nil {
		return Invocation{}, fmt.Errorf("encode Messages conversation: %w", errMarshal)
	}
	return Invocation{
		Model:        model,
		SystemPrompt: systemPrompt,
		Prompt:       "Return only the next assistant message for this Anthropic Messages conversation.\n\n" + string(conversationJSON),
		MaxTokens:    request.MaxTokens,
		Tools:        plan,
		Attachments:  attachments,
	}, nil
}

func buildAnthropicToolPlan(tools []anthropicTool, choice anthropicToolChoice) (ToolPlan, error) {
	plan := ToolPlan{NameBySDK: make(map[string]string), PassThrough: true}
	for _, item := range tools {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return ToolPlan{}, fmt.Errorf("tool name is required")
		}
		sdkName := invalidToolName.ReplaceAllString(name, "_")
		if sdkName == "" {
			return ToolPlan{}, fmt.Errorf("invalid tool name %q", name)
		}
		if _, duplicate := plan.NameBySDK[sdkName]; duplicate {
			return ToolPlan{}, fmt.Errorf("duplicate normalized tool name %q", sdkName)
		}
		parameters := map[string]any{"type": "object", "properties": map[string]any{}}
		if len(item.InputSchema) > 0 && string(item.InputSchema) != "null" {
			if err := json.Unmarshal(item.InputSchema, &parameters); err != nil {
				return ToolPlan{}, fmt.Errorf("invalid input_schema for tool %s: %w", name, err)
			}
		}
		plan.Specs = append(plan.Specs, ToolSpec{
			Name: name, SDKName: sdkName, Description: item.Description, Parameters: parameters,
		})
		plan.NameBySDK[sdkName] = name
	}

	switch strings.ToLower(strings.TrimSpace(choice.Type)) {
	case "", "auto":
		return plan, nil
	case "none":
		return ToolPlan{NameBySDK: make(map[string]string), PassThrough: true}, nil
	case "any":
		plan.Required = true
		return plan, nil
	case "tool":
		selected := strings.TrimSpace(choice.Name)
		for _, spec := range plan.Specs {
			if spec.Name == selected {
				plan.Selected = spec.Name
				plan.SelectedSDK = spec.SDKName
				plan.Required = true
				return plan, nil
			}
		}
		return ToolPlan{}, fmt.Errorf("tool_choice references unknown tool %q", selected)
	default:
		return ToolPlan{}, fmt.Errorf("unsupported tool_choice type %q", choice.Type)
	}
}

func normalizeAnthropicContent(raw json.RawMessage, role string, attachments *[]Attachment) (any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []any{}, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("%s message content must be text or an array of content blocks", role)
	}
	for index, block := range blocks {
		kind, _ := block["type"].(string)
		switch kind {
		case "text":
		case "tool_use":
			if role != "assistant" {
				return nil, fmt.Errorf("tool_use block is only valid in assistant messages")
			}
		case "tool_result":
			if role != "user" {
				return nil, fmt.Errorf("tool_result block is only valid in user messages")
			}
		case "thinking", "redacted_thinking":
			if role != "assistant" {
				return nil, fmt.Errorf("%s block is only valid in assistant messages", kind)
			}
		case "image":
			if role != "user" {
				return nil, fmt.Errorf("image block is only valid in user messages")
			}
			source, _ := block["source"].(map[string]any)
			sourceType, _ := source["type"].(string)
			switch strings.ToLower(strings.TrimSpace(sourceType)) {
			case "base64":
				mediaType, _ := source["media_type"].(string)
				data, _ := source["data"].(string)
				marker, err := appendBase64Image(attachments, mediaType, data)
				if err != nil {
					return nil, err
				}
				blocks[index] = map[string]any{"type": "text", "text": marker}
			case "url":
				url, _ := source["url"].(string)
				if strings.TrimSpace(url) == "" {
					return nil, fmt.Errorf("image URL is required")
				}
				blocks[index] = map[string]any{"type": "text", "text": "[Image URL: " + url + "]"}
			default:
				return nil, fmt.Errorf("unsupported image source type %q", sourceType)
			}
		case "document":
			return nil, fmt.Errorf("qoder CLI provider does not support document message input")
		default:
			return nil, fmt.Errorf("unsupported Anthropic message content type %q", kind)
		}
	}
	return blocks, nil
}

func anthropicTextContent(raw json.RawMessage, field string) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("%s must be text or an array of text blocks", field)
	}
	var out strings.Builder
	for _, block := range blocks {
		kind, _ := block["type"].(string)
		if kind != "text" {
			return "", fmt.Errorf("unsupported %s content type %q", field, kind)
		}
		value, _ := block["text"].(string)
		out.WriteString(value)
	}
	return out.String(), nil
}
