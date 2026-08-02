//go:build linux

package tools

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// sealWorkerInheritedDescriptors makes every non-standard descriptor close on
// the worker's child exec. bubblewrap already constrains the worker namespace;
// this additionally prevents an ambient host descriptor from reaching shell.
func sealWorkerInheritedDescriptors() error {
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err == nil {
		return nil
	} else if !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) {
		return fmt.Errorf("mark descriptors close-on-exec: %w", err)
	}

	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return fmt.Errorf("read descriptor limit: %w", err)
	}
	const maxDescriptorScan = 1 << 20
	if limit.Cur > maxDescriptorScan {
		return fmt.Errorf("descriptor limit %d exceeds secure fallback scan", limit.Cur)
	}
	for fd := uint64(3); fd < limit.Cur; fd++ {
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
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
			return fmt.Errorf("mark fd %d close-on-exec: %w", fd, err)
		}
	}
	return nil
}
