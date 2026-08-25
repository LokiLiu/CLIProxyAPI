package provider

import (
	"encoding/json"
	"fmt"
	"time"
)

func completionID(prefix string) string {
	return fmt.Sprintf("%s_%x", prefix, time.Now().UnixNano())
}

func EncodeResponse(result Result) ([]byte, error) {
	message := map[string]any{"role": "assistant", "content": result.Text}
	if len(result.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(result.ToolCalls))
		for _, call := range result.ToolCalls {
			calls = append(calls, map[string]any{
				"id": call.ID, "type": "function",
				"function": map[string]any{"name": call.Name, "arguments": string(call.Arguments)},
			})
		}
		message["tool_calls"] = calls
	}
	return json.Marshal(map[string]any{
		"id": completionID("chatcmpl"), "object": "chat.completion", "created": time.Now().Unix(), "model": result.Model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": result.FinishReason}},
		"usage":   map[string]any{"prompt_tokens": result.Usage.PromptTokens, "completion_tokens": result.Usage.CompletionTokens, "total_tokens": result.Usage.TotalTokens},
	})
}

func EncodeAnthropicResponse(result Result) ([]byte, error) {
	content := make([]map[string]any, 0, 1+len(result.ToolCalls))
	if result.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": result.Text})
	}
	for _, call := range result.ToolCalls {
		var input any = map[string]any{}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &input); err != nil {
				return nil, fmt.Errorf("decode arguments for Anthropic tool %s: %w", call.Name, err)
			}
		}
		content = append(content, map[string]any{
			"type": "tool_use", "id": call.ID, "name": call.Name, "input": input,
		})
	}
	stopReason := "end_turn"
	if len(result.ToolCalls) > 0 {
		stopReason = "tool_use"
	}
	return json.Marshal(map[string]any{
		"id": completionID("msg"), "type": "message", "role": "assistant", "model": result.Model,
		"content": content, "stop_reason": stopReason, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": result.Usage.PromptTokens, "output_tokens": result.Usage.CompletionTokens},
	})
}

func EncodeStreamRole(id, model string) []byte {
	return encodeStreamJSON(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil}},
	})
}

func EncodeStreamText(id, model, text string) []byte {
	return encodeStreamJSON(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}, "finish_reason": nil}},
	})
}

func EncodeStreamFinish(id string, result Result) []byte {
	delta := map[string]any{}
	if len(result.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(result.ToolCalls))
		for index, call := range result.ToolCalls {
			calls = append(calls, map[string]any{
				"index": index, "id": call.ID, "type": "function",
				"function": map[string]any{"name": call.Name, "arguments": string(call.Arguments)},
			})
		}
		delta["tool_calls"] = calls
	}
	return encodeStreamJSON(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": result.Model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": result.FinishReason}},
		"usage":   map[string]any{"prompt_tokens": result.Usage.PromptTokens, "completion_tokens": result.Usage.CompletionTokens, "total_tokens": result.Usage.TotalTokens},
	})
}

func encodeStreamJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func EncodeAnthropicStreamEvent(event string, value any) []byte {
	raw, _ := json.Marshal(value)
	return []byte("event: " + event + "\ndata: " + string(raw) + "\n\n")
}
