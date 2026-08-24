package kiroprovider

import "testing"

func TestParseAccount(t *testing.T) {
	account, handled, err := ParseAccount([]byte(`{
  "type":"kiro",
  "id":"work",
  "prefix":"kiro",
  "cli_path":"/Users/me/.local/bin/kiro-cli",
  "bridge_path":"/opt/cliproxy/qoder-mcp-bridge",
  "models":["claude-opus-5"]
}`), "kiro.json")
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if account.ID != "work" || len(account.Models) != 1 || account.Models[0].ID != "claude-opus-5" {
		t.Fatalf("unexpected account: %#v", account)
	}
}
