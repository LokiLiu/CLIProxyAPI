package provider

import (
	"encoding/json"
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

func TestBuildInvocationRejectsImageInput(t *testing.T) {
	_, err := BuildInvocation([]byte(`{
  "model":"Aria",
  "messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/a.png"}}]}]
}`), "Aria")
	if err == nil {
		t.Fatal("expected image input to be rejected")
	}
}
