//go:build darwin

package sandbox

func buildCurrentCommand(policy Policy, workerPath string, workerArgs []string, proxyPort int, executable string) (CommandSpec, error) {
	return buildSeatbeltCommand(policy, workerPath, workerArgs, proxyPort, executable), nil
}
