package kiroprovider

import "testing"

func TestExternalPermissionUsesMCPIdentity(t *testing.T) {
	client := &acpClient{tools: map[string]struct{}{"echo_number": {}}}
	var request permissionRequest
	request.Meta.MCPToolIdentity.ServerName = "openai_tools"
	request.Meta.MCPToolIdentity.ToolName = "echo_number"
	request.ToolCall.Title = "Running: @openai_tools/echo_number"
	if !client.isExternalPermission(request) {
		t.Fatal("expected caller MCP tool to be allowed")
	}

	request.Meta.MCPToolIdentity.ServerName = ""
	request.Meta.MCPToolIdentity.ToolName = ""
	request.ToolCall.Title = "Running: echo 42"
	if client.isExternalPermission(request) {
		t.Fatal("expected built-in shell tool to be rejected")
	}
}
