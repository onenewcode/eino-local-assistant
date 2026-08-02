//go:build linux

package sandbox

func buildCurrentCommand(policy Policy, workerPath string, workerArgs []string, proxyPort int, executable string) (CommandSpec, error) {
	args, err := bubblewrapArgs(policy, workerPath, workerArgs, proxyPort, existingLinuxRuntimeMounts())
	if err != nil {
		return CommandSpec{}, err
	}
	return CommandSpec{
		Backend:     BackendBubblewrap,
		Path:        executable,
		Args:        args,
		Dir:         policy.Workspace,
		Env:         bubblewrapEnvironment(policy, proxyPort),
		ProxySocket: proxySocketPathWhenEnabled(policy, proxyPort),
		ProxyPort:   proxyPort,
	}, nil
}
