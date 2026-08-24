# Qoder CLI Provider

This contribution adds Qoder CLI as an auth-bound CLIProxyAPI provider. CLIProxyAPI owns the public OpenAI, Anthropic, Gemini, scheduling, usage, and streaming protocols. The plugin only runs a logged-in `qodercli` process and converts its native events to canonical OpenAI chat completions.

The provider deliberately disables Qoder built-in tools, settings, project context, and built-in skills. Request tools are exposed through a temporary MCP server and returned to the upstream agent as standard tool calls. The MCP bridge never executes those tools.

## Build on macOS

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
go build -buildmode=c-shared \
  -o plugins/darwin/$(go env GOARCH)/qoder.dylib \
  ./contrib/qoder-cli-provider/plugin
go build \
  -o plugins/darwin/$(go env GOARCH)/qoder-mcp-bridge \
  ./contrib/qoder-cli-provider/cmd/qoder-mcp-bridge
```

Enable the plugin:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    qoder:
      enabled: true
      priority: 1
```

## Accounts

Run each CLI login against its own configuration root, then create one auth JSON per account under CLIProxyAPI's `auth-dir`.

```json
{
  "type": "qoder",
  "id": "qoder-personal",
  "label": "Qoder personal",
  "prefix": "qoder",
  "cli_path": "/Applications/Qoder.app/Contents/Resources/bin/qodercli",
  "config_dir": "/Users/me/.qoder-personal",
  "bridge_path": "/absolute/path/to/plugins/darwin/arm64/qoder-mcp-bridge"
}
```

```json
{
  "type": "qoder",
  "id": "qoder-work",
  "label": "QoderWork",
  "prefix": "qoderwork",
  "cli_path": "/Applications/QoderWork.app/Contents/Resources/bin/qodercli",
  "config_dir": "/Users/me/.qoder-work",
  "bridge_path": "/absolute/path/to/plugins/darwin/arm64/qoder-mcp-bridge"
}
```

Models are discovered with `qodercli --list-models`. An optional `models` array can pin the advertised list. Clients select models such as `qoder/Aria` and `qoderwork/Cantus`.

The plugin removes `QODER_AGENT_SDK_*` and Qoder SDK auth-payload variables from child processes so each CLI uses its persistent login state instead of a desktop application's one-shot credential file.
