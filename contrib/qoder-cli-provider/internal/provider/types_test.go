package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAccountSupportsMultiplePrefixes(t *testing.T) {
	raw := []byte(`{"type":"qoder","id":"work","label":"Work Account","prefix":"qoderwork","cli_path":"/Applications/QoderWork.app/Contents/Resources/bin/qodercli","config_dir":"/tmp/qoderwork","models":["Aria","Cantus","Aria"]}`)
	account, handled, err := ParseAccount(raw, "qoder-work.json")
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected qoder auth to be handled")
	}
	if account.Prefix != "qoderwork" || account.ID != "work" || len(account.Models) != 2 {
		t.Fatalf("unexpected account: %#v", account)
	}
}

func TestParseAccountIgnoresOtherProviders(t *testing.T) {
	_, handled, err := ParseAccount([]byte(`{"type":"codex"}`), "codex.json")
	if err != nil || handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}

func TestBuildInvocationPreservesConversationAndTools(t *testing.T) {
	raw := []byte(`{
  "model":"qoder/Aria",
  "messages":[
    {"role":"system","content":"Be concise."},
    {"role":"user","content":"Read it"},
    {"role":"assistant","content":"","tool_calls":[{"id":"call_old","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"old\"}"}}]},
    {"role":"tool","tool_call_id":"call_old","content":"old contents"}
  ],
  "tools":[{"type":"function","function":{"name":"read.file","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],
  "tool_choice":"required"
}`)
	invocation, err := BuildInvocation(raw, "Aria")
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Model != "Aria" || !invocation.Tools.Required || len(invocation.Tools.Specs) != 1 {
		t.Fatalf("unexpected invocation: %#v", invocation)
	}
	if invocation.Tools.Specs[0].SDKName != "read_file" {
		t.Fatalf("sdk name = %q", invocation.Tools.Specs[0].SDKName)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(invocation.Prompt[len("Complete the conversation below and return only the next assistant message.\n\n"):]), &payload); err != nil {
		t.Fatalf("prompt does not contain JSON conversation: %v", err)
	}
}

func TestBuildInvocationConvertsDataImageToAttachment(t *testing.T) {
	invocation, err := BuildInvocation([]byte(`{
  "model":"Aria",
  "messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]
}`), "Aria")
	if err != nil {
		t.Fatal(err)
	}
	if len(invocation.Attachments) != 1 || invocation.Attachments[0].MediaType != "image/png" {
		t.Fatalf("attachments = %#v", invocation.Attachments)
	}
	if !strings.Contains(invocation.Prompt, "[Attached image: image-001.png]") || strings.Contains(invocation.Prompt, "iVBOR") {
		t.Fatalf("prompt did not replace image data: %s", invocation.Prompt)
	}
}

func TestBuildAnthropicInvocationPreservesToolBlocks(t *testing.T) {
	raw := []byte(`{
  "model":"qoder/Aria",
  "system":[{"type":"text","text":"Be concise."}],
  "messages":[
    {"role":"user","content":[{"type":"text","text":"Read it"}]},
    {"role":"assistant","content":[{"type":"tool_use","id":"toolu_old","name":"read_file","input":{"path":"old"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_old","content":"old contents"}]}
  ],
  "tools":[{"name":"read.file","description":"Read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}],
  "tool_choice":{"type":"any"},
  "max_tokens":123
}`)
	invocation, err := BuildAnthropicInvocation(raw, "Aria")
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Model != "Aria" || invocation.MaxTokens != 123 || !invocation.Tools.Required || !invocation.Tools.PassThrough {
		t.Fatalf("unexpected invocation: %#v", invocation)
	}
	if len(invocation.Tools.Specs) != 1 || invocation.Tools.Specs[0].SDKName != "read_file" {
		t.Fatalf("unexpected tools: %#v", invocation.Tools)
	}
	if !strings.Contains(invocation.SystemPrompt, "never call wrappers") || !strings.Contains(invocation.SystemPrompt, "full_turn") {
		t.Fatalf("system prompt lost external-tool protocol guidance: %s", invocation.SystemPrompt)
	}
	if !strings.Contains(invocation.Prompt, `"type": "tool_result"`) || !strings.Contains(invocation.Prompt, `"tool_use_id": "toolu_old"`) {
		t.Fatalf("prompt lost Anthropic tool history: %s", invocation.Prompt)
	}
}

func TestBuildAnthropicInvocationConvertsBase64ImageToAttachment(t *testing.T) {
	invocation, err := BuildAnthropicInvocation([]byte(`{
  "model":"Aria",
  "messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},{"type":"text","text":"describe it"}]}]
}`), "Aria")
	if err != nil {
		t.Fatal(err)
	}
	if len(invocation.Attachments) != 1 || invocation.Attachments[0].FileName != "image-001.png" {
		t.Fatalf("attachments = %#v", invocation.Attachments)
	}
	if !strings.Contains(invocation.Prompt, "[Attached image: image-001.png]") || strings.Contains(invocation.Prompt, "iVBOR") {
		t.Fatalf("prompt did not replace image data: %s", invocation.Prompt)
	}
}
