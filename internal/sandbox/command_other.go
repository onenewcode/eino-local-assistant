//go:build !darwin && !linux

package sandbox

func buildCurrentCommand(policy Policy, workerPath string, workerArgs []string, executable string) (CommandSpec, error) {
	return CommandSpec{}, ErrUnsupportedPlatform
}
