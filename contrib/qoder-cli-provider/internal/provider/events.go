package provider

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type qoderEvent struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	IsError bool            `json:"is_error"`
	Result  string          `json:"result"`
	Error   string          `json:"error"`
	Errors  []string        `json:"errors"`
	Event   json.RawMessage `json:"event"`
	Message qoderMessage    `json:"message"`
	Usage   qoderUsage      `json:"usage"`
	Request struct {
		RequestID string          `json:"request_id"`
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		ToolName  string          `json:"tool_name"`
		ToolUseID string          `json:"tool_use_id"`
		Input     json.RawMessage `json:"input"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"request"`
	RequestID string `json:"request_id"`
	Response  struct {
		Subtype string `json:"subtype"`
		Error   string `json:"error"`
	} `json:"response"`
}

func (event qoderEvent) controlRequestType() string {
	if event.Request.Subtype != "" {
		return event.Request.Subtype
	}
	return event.Request.Type
}

func (event qoderEvent) externalToolCall(plan ToolPlan) (ToolCall, bool, error) {
	input := event.Request.Input
	if len(input) == 0 || string(input) == "null" {
		input = event.Request.Arguments
	}
	return normalizeToolCall(qoderBlock{
		Type:  "tool_use",
		ID:    event.Request.ToolUseID,
		Name:  event.Request.ToolName,
		Input: input,
	}, plan)
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

func assistantResult(event qoderEvent, plan ToolPlan, acceptToolCalls bool) (string, []ToolCall, Usage, error) {
	var text strings.Builder
	calls := make([]ToolCall, 0)
	for _, block := range event.Message.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			if !acceptToolCalls {
				continue
			}
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
	if isWrappedToolName(rawName) {
		name = firstArgumentString(input, "tool_name", "toolName", "tool", "name")
		if nested, ok := input["function"].(map[string]any); ok {
			if name == "" {
				name, _ = nested["name"].(string)
			}
			if nestedArguments, exists := nested["arguments"]; exists {
				input = anyArguments(nestedArguments)
			}
		}
		for _, key := range []string{"arguments", "input", "params", "parameters"} {
			if nested, exists := input[key]; exists {
				input = anyArguments(nested)
				break
			}
		}
		if name == "" {
			name = inferWrappedToolName(input, plan)
		}
		if name == "" {
			return ToolCall{}, false, nil
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
	if !plan.PassThrough {
		input = normalizeArguments(input, schemaForTool(plan, name))
		if err := validateArguments(input, schemaForTool(plan, name), "arguments"); err != nil {
			return ToolCall{}, false, fmt.Errorf("qoder returned invalid arguments for tool %s: %w (received keys: %s)", original, err, argumentKeySummary(input))
		}
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

func isWrappedToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "mcp__openai_tools", "tool_use", "tool_calls", "function_call":
		return true
	default:
		return false
	}
}

// inferWrappedToolName recovers the concrete function when Qoder emits a generic
// tool wrapper rather than the declared function name. The recovery is deliberately
// conservative: arguments must validate against exactly one declared schema and
// must contain at least one property from that schema. This avoids turning a marker
// or an ambiguous payload into an arbitrary caller tool.
func inferWrappedToolName(input map[string]any, plan ToolPlan) string {
	if plan.SelectedSDK != "" {
		return plan.SelectedSDK
	}
	type candidate struct {
		name  string
		score int
	}
	candidates := make([]candidate, 0, len(plan.Specs))
	bestScore := 0
	for _, spec := range plan.Specs {
		normalized := normalizeArguments(cloneArguments(input), spec.Parameters)
		if err := validateArguments(normalized, spec.Parameters, "arguments"); err != nil {
			continue
		}
		score := argumentPropertyMatchScore(normalized, spec.Parameters)
		if score == 0 {
			continue
		}
		if score > bestScore {
			bestScore = score
			candidates = candidates[:0]
		}
		if score == bestScore {
			candidates = append(candidates, candidate{name: spec.SDKName, score: score})
		}
	}
	if len(candidates) == 1 {
		return candidates[0].name
	}
	return ""
}

func cloneArguments(input map[string]any) map[string]any {
	raw, err := json.Marshal(input)
	if err != nil {
		return input
	}
	var cloned map[string]any
	if json.Unmarshal(raw, &cloned) != nil {
		return input
	}
	return cloned
}

func argumentPropertyMatchScore(input map[string]any, schema map[string]any) int {
	properties, _ := schema["properties"].(map[string]any)
	score := 0
	for key := range input {
		if _, known := properties[key]; known {
			score++
		}
	}
	return score
}

func NormalizeExternalToolCall(id, name string, input any, plan ToolPlan) (ToolCall, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return ToolCall{}, err
	}
	call, keep, err := normalizeToolCall(qoderBlock{Type: "tool_use", ID: id, Name: name, Input: raw}, plan)
	if err != nil {
		return ToolCall{}, err
	}
	if !keep {
		return ToolCall{}, fmt.Errorf("tool call %q did not resolve to a concrete external function", name)
	}
	return call, nil
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
	normalized, _ := normalizeArgumentValue(value, schema).(map[string]any)
	if normalized == nil {
		return value
	}
	return normalized
}

func normalizeArgumentValue(value any, schema map[string]any) any {
	kind, _ := schema["type"].(string)
	switch kind {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return value
		}
		properties, _ := schema["properties"].(map[string]any)
		object = unwrapArgumentEnvelope(object, properties)
		object = normalizePropertyNames(object, properties)
		for key, current := range object {
			property, _ := properties[key].(map[string]any)
			object[key] = normalizeArgumentValue(current, property)
		}
		return object
	case "array":
		items, ok := value.([]any)
		if !ok {
			return value
		}
		itemSchema, _ := schema["items"].(map[string]any)
		for index, item := range items {
			items[index] = normalizeArgumentValue(item, itemSchema)
		}
		return items
	case "integer", "number":
		if text, ok := value.(string); ok {
			number := json.Number(strings.TrimSpace(text))
			if kind == "integer" {
				if parsed, err := number.Int64(); err == nil {
					return parsed
				}
			} else if parsed, err := number.Float64(); err == nil {
				return parsed
			}
		}
	case "boolean":
		if text, ok := value.(string); ok {
			switch strings.ToLower(strings.TrimSpace(text)) {
			case "true":
				return true
			case "false":
				return false
			}
		}
	}
	return value
}

func unwrapArgumentEnvelope(value map[string]any, properties map[string]any) map[string]any {
	for key := range value {
		if _, known := properties[key]; known {
			return value
		}
	}
	for _, key := range []string{"arguments", "input", "params", "parameters"} {
		if _, schemaProperty := properties[key]; schemaProperty {
			continue
		}
		nested, exists := value[key]
		if !exists {
			continue
		}
		if object, ok := argumentObject(nested); ok {
			return object
		}
	}
	return value
}

func normalizePropertyNames(value map[string]any, properties map[string]any) map[string]any {
	if len(value) == 0 || len(properties) == 0 {
		return value
	}
	aliases := make(map[string]string, len(properties))
	ambiguous := make(map[string]bool)
	for property := range properties {
		alias := normalizedParameterName(property)
		if existing, found := aliases[alias]; found && existing != property {
			ambiguous[alias] = true
			continue
		}
		aliases[alias] = property
	}
	normalized := make(map[string]any, len(value))
	for key, current := range value {
		if _, exact := properties[key]; exact {
			normalized[key] = current
		}
	}
	for key, current := range value {
		if _, exact := properties[key]; exact {
			continue
		}
		alias := normalizedParameterName(key)
		property, found := aliases[alias]
		if found && !ambiguous[alias] {
			if _, occupied := normalized[property]; !occupied {
				normalized[property] = current
				continue
			}
		}
		normalized[key] = current
	}
	return normalized
}

func normalizedParameterName(value string) string {
	var out strings.Builder
	for _, char := range strings.ToLower(value) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			out.WriteRune(char)
		}
	}
	return out.String()
}

func argumentObject(value any) (map[string]any, bool) {
	if object, ok := value.(map[string]any); ok {
		return object, true
	}
	if text, ok := value.(string); ok {
		var object map[string]any
		if json.Unmarshal([]byte(text), &object) == nil {
			return object, true
		}
	}
	return nil, false
}

func argumentKeySummary(value map[string]any) string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return "none"
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
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
