package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type toolSpec struct {
	Name        string         `json:"Name"`
	SDKName     string         `json:"SDKName"`
	Description string         `json:"Description"`
	Parameters  map[string]any `json:"Parameters"`
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func main() {
	tools, err := loadTools()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message request
		if json.Unmarshal([]byte(line), &message) != nil || len(message.ID) == 0 {
			continue
		}
		response := handle(message, tools)
		_ = encoder.Encode(response)
	}
}

func loadTools() ([]toolSpec, error) {
	encoded := strings.TrimSpace(os.Getenv("QODER_EXTERNAL_TOOLS"))
	if encoded == "" {
		return nil, fmt.Errorf("QODER_EXTERNAL_TOOLS is required")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode QODER_EXTERNAL_TOOLS: %w", err)
	}
	var tools []toolSpec
	if err = json.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("parse QODER_EXTERNAL_TOOLS: %w", err)
	}
	return tools, nil
}

func handle(message request, tools []toolSpec) map[string]any {
	base := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message.ID)}
	switch message.Method {
	case "initialize":
		base["result"] = map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "openai_tools", "version": "1.0.0"},
		}
	case "ping":
		base["result"] = map[string]any{}
	case "tools/list":
		list := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			description := tool.Description
			if description == "" {
				description = "Call external function " + tool.Name
			}
			list = append(list, map[string]any{
				"name": tool.SDKName, "description": description, "inputSchema": tool.Parameters,
			})
		}
		base["result"] = map[string]any{"tools": list}
	case "tools/call":
		base["result"] = map[string]any{
			"isError": false,
			"content": []map[string]any{{
				"type": "text",
				"text": "Tool call captured by the upstream harness. End this turn now without further output.",
			}},
		}
	default:
		base["error"] = map[string]any{"code": -32601, "message": "Method not found"}
	}
	return base
}
