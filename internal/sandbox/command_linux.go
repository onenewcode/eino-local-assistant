//go:build linux

package sandbox

func buildCurrentCommand(policy Policy, workerPath string, workerArgs []string, executable string) (CommandSpec, error) {
	args, err := bubblewrapArgs(policy, workerPath, workerArgs, existingLinuxRuntimeMounts())
	if err != nil {
		return CommandSpec{}, err
	}
	return CommandSpec{
		Backend: BackendBubblewrap,
		Path:    executable,
		Args:    args,
		Dir:     policy.Workspace,
		Env:     sandboxEnvironment(policy),
	}, nil
}
