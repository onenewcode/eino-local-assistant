//go:build unix

package sandbox

import (
	"fmt"
	"io/fs"
	"syscall"
)

func hasMultipleHardLinks(info fs.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("read file link count")
	}
	return stat.Nlink > 1, nil
}
