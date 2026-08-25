package provider

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

const ProviderID = "qoder"

type Account struct {
	ID         string
	Label      string
	Prefix     string
	CLIPath    string
	ConfigDir  string
	CWD        string
	BridgePath string
	Models     []string
	Raw        []byte
}

type ToolSpec struct {
	Name        string
	SDKName     string
	Description string
	Parameters  map[string]any
}

type ToolPlan struct {
	Specs       []ToolSpec
	NameBySDK   map[string]string
	Selected    string
	SelectedSDK string
	Required    bool
	PassThrough bool
}

type Invocation struct {
	Model        string
	SystemPrompt string
	Prompt       string
	MaxTokens    int
	Tools        ToolPlan
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

type Result struct {
	Model        string
	Text         string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
}

func ParseAccount(raw []byte, fileName string) (Account, bool, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return Account{}, false, nil
	}
	typeName := firstString(value, "type", "provider")
	if !isQoderType(typeName) {
		return Account{}, false, nil
	}

	cliPath := firstString(value, "cli_path", "cli-path", "qodercli", "path")
	if strings.TrimSpace(cliPath) == "" {
		return Account{}, true, fmt.Errorf("qoder auth %q is missing cli_path", fileName)
	}
	if !filepath.IsAbs(cliPath) {
		return Account{}, true, fmt.Errorf("qoder auth %q cli_path must be absolute", fileName)
	}

	id := firstString(value, "id", "account_id", "account-id")
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	}
	label := firstString(value, "label", "name", "email")
	if label == "" {
		label = id
	}
	prefix := sanitizePrefix(firstString(value, "prefix"))
	if prefix == "" {
		prefix = ProviderID
	}
	cwd := firstString(value, "cwd", "working_directory", "working-directory")
	if cwd != "" && !filepath.IsAbs(cwd) {
		return Account{}, true, fmt.Errorf("qoder auth %q cwd must be absolute", fileName)
	}
	configDir := firstString(value, "config_dir", "config-dir")
	if configDir != "" && !filepath.IsAbs(configDir) {
		return Account{}, true, fmt.Errorf("qoder auth %q config_dir must be absolute", fileName)
	}
	bridgePath := firstString(value, "bridge_path", "bridge-path")
	if bridgePath != "" && !filepath.IsAbs(bridgePath) {
		return Account{}, true, fmt.Errorf("qoder auth %q bridge_path must be absolute", fileName)
	}

	return Account{
		ID:         id,
		Label:      label,
		Prefix:     prefix,
		CLIPath:    cliPath,
		ConfigDir:  configDir,
		CWD:        cwd,
		BridgePath: bridgePath,
		Models:     stringList(value["models"]),
		Raw:        append([]byte(nil), raw...),
	}, true, nil
}

func isQoderType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "qoder", "qoder-cli", "qodercli":
		return true
	default:
		return false
	}
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if item, ok := value[key].(string); ok {
			if normalized := strings.TrimSpace(item); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

func stringList(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		if text, okText := value.(string); okText {
			raw = make([]any, 0)
			for _, item := range strings.Split(text, ",") {
				raw = append(raw, item)
			}
		} else {
			return nil
		}
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		text, okItem := item.(string)
		if !okItem {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		key := strings.ToLower(text)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, text)
	}
	return out
}

func sanitizePrefix(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if value == "" {
		return ""
	}
	var out strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '-', char == '_':
			out.WriteRune(char)
		}
	}
	return strings.ToLower(out.String())
}
