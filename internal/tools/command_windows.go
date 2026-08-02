//go:build windows

package tools

import "os/exec"

func configureCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	// Windows has no Setpgid; keep CommandContext's default Process.Kill and
	// bound Wait after cancel so stuck I/O cannot hang the turn forever.
	cmd.WaitDelay = commandWaitGrace
}

func killCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
