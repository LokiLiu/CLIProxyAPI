//go:build windows

package provider

import (
	"os/exec"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return killProcessTree(cmd) }
}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
