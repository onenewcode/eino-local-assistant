//go:build darwin

package sandbox

func buildCurrentCommand(policy Policy, workerPath string, workerArgs []string, executable string) (CommandSpec, error) {
	return buildSeatbeltCommand(policy, workerPath, workerArgs, executable), nil
}
