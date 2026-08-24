package kiroprovider

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

const ProviderID = "kiro"

type Account struct {
	ID         string
	Label      string
	Prefix     string
	CLIPath    string
	BridgePath string
	Models     []Model
	Raw        []byte
}

type Model struct {
	Name          string  `json:"model_name"`
	ID            string  `json:"model_id"`
	Description   string  `json:"description"`
	ContextWindow int64   `json:"context_window_tokens"`
	Rate          float64 `json:"rate_multiplier"`
	RateUnit      string  `json:"rate_unit"`
}

func ParseAccount(raw []byte, fileName string) (Account, bool, error) {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return Account{}, false, nil
	}
	typeName := firstString(value, "type", "provider")
	if typeName != "kiro" && typeName != "kiro-cli" && typeName != "kirocli" {
		return Account{}, false, nil
	}
	cliPath := firstString(value, "cli_path", "cli-path", "kiro_cli", "path")
	if cliPath == "" || !filepath.IsAbs(cliPath) {
		return Account{}, true, fmt.Errorf("kiro auth %q requires an absolute cli_path", fileName)
	}
	bridgePath := firstString(value, "bridge_path", "bridge-path")
	if bridgePath == "" || !filepath.IsAbs(bridgePath) {
		return Account{}, true, fmt.Errorf("kiro auth %q requires an absolute bridge_path", fileName)
	}
	id := firstString(value, "id", "account_id", "account-id")
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	}
	label := firstString(value, "label", "name", "email")
	if label == "" {
		label = id
	}
	prefix := firstString(value, "prefix")
	if prefix == "" {
		prefix = ProviderID
	}
	var models []Model
	if rawModels, ok := value["models"]; ok {
		switch list := rawModels.(type) {
		case []any:
			for _, item := range list {
				switch model := item.(type) {
				case string:
					models = append(models, Model{Name: model, ID: model})
				case map[string]any:
					rawModel, _ := json.Marshal(model)
					var decoded Model
					if json.Unmarshal(rawModel, &decoded) == nil {
						models = append(models, decoded)
					}
				}
			}
		}
	}
	return Account{ID: id, Label: label, Prefix: prefix, CLIPath: cliPath, BridgePath: bridgePath, Models: models, Raw: append([]byte(nil), raw...)}, true, nil
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}
