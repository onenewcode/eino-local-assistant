//go:build windows

package tools

import "os/exec"

func configureCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
}

func terminateCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func killCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
