# Qoder and Kiro CLI Providers

This contribution adds Qoder CLI and Kiro CLI as auth-bound CLIProxyAPI providers. CLIProxyAPI owns the public OpenAI, Anthropic, Gemini, scheduling, usage, and streaming protocols. The plugins only run logged-in CLI processes and convert their native events to canonical completions.

For Qoder, prefer the Anthropic Messages API at `/v1/messages`. It preserves native `tool_use` and `tool_result` blocks, including streaming `input_json_delta` events. Every request contains the complete conversation and starts a fresh CLI invocation: the gateway does not resume Qoder sessions, execute caller tools, or repair tool arguments. The caller's agent harness remains responsible for the tool loop.

Qoder built-in tools, settings, project context, and built-in skills are disabled. Kiro runs in an isolated temporary workspace and its ACP permission handler rejects tools not supplied by the caller. Request tools are exposed through a temporary MCP server and returned to the upstream agent as standard tool calls. The bridge never executes those tools.

## Build on macOS

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
go build -buildmode=c-shared \
  -o plugins/darwin/$(go env GOARCH)/qoder.dylib \
  ./contrib/qoder-cli-provider/plugin
go build -buildmode=c-shared \
  -o plugins/darwin/$(go env GOARCH)/kiro.dylib \
  ./contrib/qoder-cli-provider/plugin-kiro
go build \
  -o plugins/darwin/$(go env GOARCH)/cli-proxy-tool-bridge \
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
    kiro:
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
  "bridge_path": "/absolute/path/to/plugins/darwin/arm64/cli-proxy-tool-bridge"
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
  "bridge_path": "/absolute/path/to/plugins/darwin/arm64/cli-proxy-tool-bridge"
}
```

The account prefix routes a model to a CLI installation, for example `qoder/Aria` or `qoderwork/Aria`. The suffix is passed to `qodercli --model` and the CLI remains the source of truth for whether that model is currently accepted. Model discovery is used only to populate `/v1/models`; it does not reject an otherwise routable request.

The plugin removes `QODER_AGENT_SDK_*` and Qoder SDK auth-payload variables from child processes so each CLI uses its persistent login state instead of a desktop application's one-shot credential file.

Kiro uses its normal persistent login and does not need a separate credential directory:

```json
{
  "type": "kiro",
  "id": "kiro-work",
  "label": "Kiro",
  "prefix": "kiro",
  "cli_path": "/Users/me/.local/bin/kiro-cli",
  "bridge_path": "/absolute/path/to/plugins/darwin/arm64/cli-proxy-tool-bridge"
}
```

Kiro models are discovered with `kiro-cli chat --list-models --format json`. An optional `models` array can pin the advertised list. Clients select models such as `kiro/claude-opus-5`.
