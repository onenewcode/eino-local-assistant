//go:build !unix

package sandbox

import "io/fs"

func hasMultipleHardLinks(_ fs.FileInfo) (bool, error) {
	return false, nil
}
