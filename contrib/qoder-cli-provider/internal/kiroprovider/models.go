package kiroprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

func DiscoverModels(ctx context.Context, account Account) ([]Model, error) {
	if len(account.Models) > 0 {
		return append([]Model(nil), account.Models...), nil
	}
	cmd := exec.CommandContext(ctx, account.CLIPath, "chat", "--list-models", "--format", "json")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list Kiro models: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	var response struct {
		Models []Model `json:"models"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode Kiro models: %w", err)
	}
	return response.Models, nil
}
