package kiroprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/contrib/qoder-cli-provider/internal/provider"
)

type acpClient struct {
	encoder *json.Encoder
	writeMu sync.Mutex
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[string]chan acpMessage
	updates chan json.RawMessage
	done    chan error
	tools   map[string]struct{}
}

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type permissionRequest struct {
	ToolCall struct {
		Name  string `json:"name"`
		Title string `json:"title"`
	} `json:"toolCall"`
	Options []struct {
		OptionID string `json:"optionId"`
		Kind     string `json:"kind"`
	} `json:"options"`
	Meta struct {
		MCPToolIdentity struct {
			ServerName string `json:"serverName"`
			ToolName   string `json:"toolName"`
		} `json:"mcpToolIdentity"`
	} `json:"_meta"`
}

func newACPClient(reader io.Reader, writer io.Writer, plan provider.ToolPlan) *acpClient {
	tools := make(map[string]struct{}, len(plan.Specs))
	for _, spec := range plan.Specs {
		tools[spec.SDKName] = struct{}{}
		tools["mcp__openai_tools__"+spec.SDKName] = struct{}{}
	}
	client := &acpClient{
		encoder: json.NewEncoder(writer),
		pending: make(map[string]chan acpMessage),
		updates: make(chan json.RawMessage, 128),
		done:    make(chan error, 1),
		tools:   tools,
	}
	go client.readLoop(reader)
	return client
}

func (c *acpClient) call(ctx context.Context, method string, params any, result any) error {
	id := c.nextID.Add(1)
	idKey := strconv.FormatInt(id, 10)
	response := make(chan acpMessage, 1)
	c.mu.Lock()
	c.pending[idKey] = response
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, idKey)
		c.mu.Unlock()
	}()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-c.done:
		return err
	case message := <-response:
		if message.Error != nil {
			return fmt.Errorf("ACP %s failed (%d): %s", method, message.Error.Code, message.Error.Message)
		}
		if result != nil && len(message.Result) > 0 {
			if err := json.Unmarshal(message.Result, result); err != nil {
				return fmt.Errorf("decode ACP %s response: %w", method, err)
			}
		}
		return nil
	}
}

func (c *acpClient) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message acpMessage
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			c.finish(fmt.Errorf("decode ACP message: %w", err))
			return
		}
		if message.Method != "" {
			c.handleServerMessage(message)
			continue
		}
		key := strings.Trim(string(message.ID), `"`)
		c.mu.Lock()
		pending := c.pending[key]
		c.mu.Unlock()
		if pending != nil {
			pending <- message
		}
	}
	if err := scanner.Err(); err != nil {
		c.finish(err)
	} else {
		c.finish(io.EOF)
	}
}

func (c *acpClient) handleServerMessage(message acpMessage) {
	switch message.Method {
	case "session/update":
		select {
		case c.updates <- message.Params:
		default:
		}
	case "session/request_permission":
		select {
		case c.updates <- message.Params:
		default:
		}
		var params permissionRequest
		_ = json.Unmarshal(message.Params, &params)
		external := c.isExternalPermission(params)
		selected := ""
		desiredKinds := []string{"reject_once", "reject_always"}
		if external {
			desiredKinds = []string{"allow_once", "allow_always"}
		}
		for _, desired := range desiredKinds {
			for _, option := range params.Options {
				if option.Kind == desired {
					selected = option.OptionID
					break
				}
			}
			if selected != "" {
				break
			}
		}
		result := map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
		if selected != "" {
			result = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": selected}}
		}
		_ = c.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message.ID), "result": result})
	default:
		if len(message.ID) > 0 {
			_ = c.write(map[string]any{
				"jsonrpc": "2.0", "id": json.RawMessage(message.ID),
				"error": map[string]any{"code": -32601, "message": "Method not supported"},
			})
		}
	}
}

func (c *acpClient) isExternalPermission(params permissionRequest) bool {
	if params.Meta.MCPToolIdentity.ServerName == "openai_tools" {
		_, ok := c.tools[params.Meta.MCPToolIdentity.ToolName]
		return ok
	}
	if _, ok := c.tools[params.ToolCall.Name]; ok {
		return true
	}
	_, ok := c.tools[params.ToolCall.Title]
	return ok
}

func (c *acpClient) write(value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.encoder.Encode(value)
}

func (c *acpClient) finish(err error) {
	select {
	case c.done <- err:
	default:
	}
}
