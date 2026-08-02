package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Availability reports whether the current platform can create a strict
// sandbox command without falling back to host execution.
type Availability struct {
	Backend    Backend
	Available  bool
	Executable string
	Reason     string
}

// Available reports whether the strict sandbox backend is currently usable.
func Available() bool {
	return CurrentAvailability().Available
}

// CurrentAvailability checks the current OS backend and its required binary.
func CurrentAvailability() Availability {
	return currentAvailability()
}

func availabilityFor(backend Backend, executable string, lookup func(string) (string, error)) Availability {
	path, err := lookup(executable)
	if err != nil {
		return Availability{
			Backend: backend,
			Reason:  executable + " is not available: " + err.Error(),
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Availability{Backend: backend, Reason: executable + " path is invalid: " + err.Error()}
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Availability{Backend: backend, Reason: executable + " cannot be resolved: " + err.Error()}
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return Availability{Backend: backend, Reason: executable + " cannot be inspected: " + err.Error()}
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return Availability{Backend: backend, Reason: fmt.Sprintf("%s is not an executable regular file", executable)}
	}
	multiple, err := hasMultipleHardLinks(info)
	if err != nil {
		return Availability{Backend: backend, Reason: executable + " link count cannot be inspected: " + err.Error()}
	}
	if multiple {
		return Availability{Backend: backend, Reason: executable + " has multiple hard links"}
	}
	return Availability{Backend: backend, Available: true, Executable: filepath.Clean(resolved)}
}

func executableLookup(name string) (string, error) {
	return exec.LookPath(name)
}
