//go:build !darwin && !linux

package tools

func sealWorkerInheritedDescriptors() error {
	return nil
}
