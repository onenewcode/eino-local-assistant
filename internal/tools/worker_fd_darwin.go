//go:build darwin

package tools

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// sealWorkerInheritedDescriptors closes non-CLOEXEC descriptors inherited
// across sandbox-exec. Seatbelt cannot revoke an already-open host file or
// socket, so the private worker must remove that capability before it handles
// model-controlled tool input or starts a shell.
func sealWorkerInheritedDescriptors() error {
	limit := unix.Getdtablesize()
	if limit < 3 {
		return nil
	}
	for fd := 3; fd < limit; fd++ {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if errors.Is(err, unix.EBADF) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect fd %d: %w", fd, err)
		}
		if flags&unix.FD_CLOEXEC != 0 {
			continue
		}
		if err := unix.Close(fd); err != nil && !errors.Is(err, unix.EBADF) {
			return fmt.Errorf("close inherited fd %d: %w", fd, err)
		}
	}
	return nil
}
