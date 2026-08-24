package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

type qoderEvent struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Result  string          `json:"result"`
	Error   string          `json:"error"`
	Errors  []string        `json:"errors"`
	Event   json.RawMessage `json:"event"`
	Message qoderMessage    `json:"message"`
	Usage   qoderUsage      `json:"usage"`
	Request struct {
		RequestID string `json:"request_id"`
	} `json:"request"`
	RequestID string `json:"request_id"`
	Response  struct {
		Subtype string `json:"subtype"`
		Error   string `json:"error"`
	} `json:"response"`
}

type qoderMessage struct {
	Content []qoderBlock `json:"content"`
	Usage   qoderUsage   `json:"usage"`
}

type qoderBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type qoderUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

func textDelta(event qoderEvent) string {
	if event.Type != "stream_event" || len(event.Event) == 0 {
		return ""
	}
	var value struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if json.Unmarshal(event.Event, &value) != nil || value.Delta.Type != "text_delta" {
		return ""
	}
	return value.Delta.Text
}

func assistantResult(event qoderEvent, plan ToolPlan) (string, []ToolCall, Usage, error) {
	var text strings.Builder
	calls := make([]ToolCall, 0)
	for _, block := range event.Message.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			call, keep, err := normalizeToolCall(block, plan)
			if err != nil {
				return "", nil, Usage{}, err
			}
			if keep {
				calls = append(calls, call)
			}
		}
	}
	return text.String(), calls, usageFromQoder(event.Message.Usage), nil
}

func normalizeToolCall(block qoderBlock, plan ToolPlan) (ToolCall, bool, error) {
	rawName := strings.TrimSpace(block.Name)
	input := decodeArguments(block.Input)
	if rawName == "tool_calls" && len(input) == 0 {
		return ToolCall{}, false, nil
	}

	name := unprefixToolName(rawName)
	if rawName == "mcp__openai_tools" {
		name = firstArgumentString(input, "tool_name", "toolName", "tool", "name")
		if nested, ok := input["function"].(map[string]any); ok && name == "" {
			name, _ = nested["name"].(string)
		}
		if name == "" {
			return ToolCall{}, false, nil
		}
		for _, key := range []string{"arguments", "input", "params", "parameters"} {
			if nested, exists := input[key]; exists {
				input = anyArguments(nested)
				break
			}
		}
	}
	name = unprefixToolName(name)
	original, known := plan.NameBySDK[name]
	if !known {
		return ToolCall{}, false, fmt.Errorf("qoder returned unknown external tool %q", rawName)
	}
	if plan.Selected != "" && original != plan.Selected {
		return ToolCall{}, false, fmt.Errorf("qoder returned tool %q instead of required tool %q", original, plan.Selected)
	}
	input = normalizeArguments(input, schemaForTool(plan, name))
	if err := validateArguments(input, schemaForTool(plan, name), "arguments"); err != nil {
		return ToolCall{}, false, fmt.Errorf("qoder returned invalid arguments for tool %s: %w", original, err)
	}
	arguments, err := json.Marshal(input)
	if err != nil {
		return ToolCall{}, false, fmt.Errorf("encode arguments for tool %s: %w", original, err)
	}
	id := strings.TrimSpace(block.ID)
	if id == "" {
		id = completionID("call")
	}
	return ToolCall{ID: id, Name: original, Arguments: arguments}, true, nil
}

func schemaForTool(plan ToolPlan, sdkName string) map[string]any {
	for _, spec := range plan.Specs {
		if spec.SDKName == sdkName {
			return spec.Parameters
		}
	}
	return nil
}

func unprefixToolName(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"mcp__openai_tools__", "openai_tools__"} {
		name = strings.TrimPrefix(name, prefix)
	}
	return name
}

func decodeArguments(raw json.RawMessage) map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			_ = json.Unmarshal([]byte(text), &value)
		}
	}
	return anyArguments(value)
}

func anyArguments(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	if text, ok := value.(string); ok {
		var object map[string]any
		if json.Unmarshal([]byte(text), &object) == nil {
			return object
		}
	}
	return map[string]any{}
}

func firstArgumentString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func normalizeArguments(value map[string]any, schema map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	for key, rawSchema := range properties {
		property, _ := rawSchema.(map[string]any)
		kind, _ := property["type"].(string)
		current, exists := value[key]
		if !exists {
			continue
		}
		switch kind {
		case "integer", "number":
			if text, ok := current.(string); ok {
				var number json.Number = json.Number(strings.TrimSpace(text))
				if kind == "integer" {
					if parsed, err := number.Int64(); err == nil {
						value[key] = parsed
					}
				} else if parsed, err := number.Float64(); err == nil {
					value[key] = parsed
				}
			}
		case "boolean":
			if text, ok := current.(string); ok {
				switch strings.ToLower(strings.TrimSpace(text)) {
				case "true":
					value[key] = true
				case "false":
					value[key] = false
				}
			}
		}
	}
	return value
}

func validateArguments(value any, schema map[string]any, path string) error {
	if len(schema) == 0 {
		return nil
	}
	kind, _ := schema["type"].(string)
	switch kind {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		for _, required := range stringSlice(schema["required"]) {
			if _, exists := object[required]; !exists {
				return fmt.Errorf("%s.%s is required", path, required)
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, item := range object {
			childSchema, _ := properties[name].(map[string]any)
			if err := validateArguments(item, childSchema, path+"."+name); err != nil {
				return err
			}
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for index, item := range items {
			if err := validateArguments(item, itemSchema, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	case "integer":
		switch number := value.(type) {
		case int, int32, int64, uint, uint32, uint64:
			return nil
		case float64:
			if number != float64(int64(number)) {
				return fmt.Errorf("%s must be an integer", path)
			}
		default:
			return fmt.Errorf("%s must be an integer", path)
		}
	case "number":
		switch value.(type) {
		case int, int32, int64, uint, uint32, uint64, float32, float64:
			return nil
		default:
			return fmt.Errorf("%s must be a number", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	}
	return nil
}

func stringSlice(value any) []string {
	raw, _ := value.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func usageFromQoder(value qoderUsage) Usage {
	return Usage{PromptTokens: value.InputTokens, CompletionTokens: value.OutputTokens, TotalTokens: value.InputTokens + value.OutputTokens}
}

func resultError(event qoderEvent) string {
	parts := make([]string, 0, len(event.Errors)+2)
	for _, item := range event.Errors {
		if strings.TrimSpace(item) != "" {
			parts = append(parts, strings.TrimSpace(item))
		}
	}
	for _, item := range []string{event.Error, event.Response.Error} {
		if strings.TrimSpace(item) != "" {
			parts = append(parts, strings.TrimSpace(item))
		}
	}
	if len(parts) == 0 && event.Subtype != "" && event.Subtype != "success" {
		parts = append(parts, event.Subtype)
	}
	return strings.Join(parts, "; ")
}
