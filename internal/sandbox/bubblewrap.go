package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BubblewrapArgs builds a strict bwrap argument vector without checking that
// bwrap is installed. The filesystem namespace deliberately keeps the host
// network namespace so package managers and developer tools can connect.
func BubblewrapArgs(policy Policy, workerPath string, workerArgs []string) ([]string, error) {
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		return nil, err
	}
	worker, err := normalizeWorkerPath(workerPath)
	if err != nil {
		return nil, err
	}
	return bubblewrapArgs(normalized, worker, workerArgs, existingLinuxRuntimeMounts())
}

// BuildBubblewrapCommand builds a bwrap invocation without checking that bwrap
// is installed. BuildCommand performs that availability check for Linux.
func BuildBubblewrapCommand(policy Policy, workerPath string, workerArgs []string) (CommandSpec, error) {
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		return CommandSpec{}, err
	}
	worker, err := normalizeWorkerPath(workerPath)
	if err != nil {
		return CommandSpec{}, err
	}
	args, err := bubblewrapArgs(normalized, worker, workerArgs, existingLinuxRuntimeMounts())
	if err != nil {
		return CommandSpec{}, err
	}
	return CommandSpec{
		Backend: BackendBubblewrap,
		Path:    "bwrap",
		Args:    args,
		Dir:     policy.Workspace,
		Env:     sandboxEnvironment(policy),
	}, nil
}

type bindMount struct {
	source   string
	dest     string
	writable bool
}

func bubblewrapArgs(policy Policy, workerPath string, workerArgs []string, runtimeRoots []string) ([]string, error) {
	mounts := bubblewrapMounts(policy, workerPath, runtimeRoots)
	protected, err := protectedMasks(policy)
	if err != nil {
		return nil, err
	}

	args := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-user",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--cap-drop",
		"ALL",
		"--hostname",
		"eino-sandbox",
		"--tmpfs",
		"/",
	}
	for _, directory := range mountParentDirectories(mounts) {
		args = append(args, "--dir", directory)
	}
	args = append(args, "--dir", "/dev", "--dir", "/proc")

	for _, mount := range mounts {
		if mount.writable {
			args = append(args, "--bind", mount.source, mount.dest)
		} else {
			args = append(args, "--ro-bind", mount.source, mount.dest)
		}
	}
	args = append(args, "--proc", "/proc", "--dev", "/dev")

	for _, mask := range protected {
		args = append(args, mask...)
	}

	args = append(args, "--clearenv")
	for _, entry := range sandboxEnvironment(policy) {
		name, value, _ := strings.Cut(entry, "=")
		args = append(args, "--setenv", name, value)
	}
	args = append(args, "--chdir", policy.Workspace, "--", workerPath)
	args = append(args, workerArgs...)
	return args, nil
}

func bubblewrapMounts(policy Policy, workerPath string, runtimeRoots []string) []bindMount {
	mounts := make([]bindMount, 0, len(runtimeRoots)+len(policy.ReadOnlyRoots)+3)
	for _, root := range uniqueSortedPaths(runtimeRoots) {
		mounts = append(mounts, bindMount{source: root, dest: root})
	}
	for _, root := range policy.ReadOnlyRoots {
		mounts = append(mounts, bindMount{source: root, dest: root})
	}

	mounts = append(mounts, bindMount{
		source:   policy.Workspace,
		dest:     policy.Workspace,
		writable: policy.Mode == WorkspaceWrite,
	})
	mounts = append(mounts, bindMount{source: policy.TempDir, dest: policy.TempDir, writable: true})

	if !pathCoveredByMount(workerPath, mounts) {
		mounts = append(mounts, bindMount{source: workerPath, dest: workerPath})
	}

	// Duplicate destinations make mount order security-sensitive. Keep the
	// first entry, which is always the narrowest intended source mount.
	seen := make(map[string]struct{}, len(mounts))
	result := make([]bindMount, 0, len(mounts))
	for _, mount := range mounts {
		if _, ok := seen[mount.dest]; ok {
			continue
		}
		seen[mount.dest] = struct{}{}
		result = append(result, mount)
	}
	return result
}

func pathCoveredByMount(path string, mounts []bindMount) bool {
	for _, mount := range mounts {
		if pathWithin(mount.dest, path) {
			return true
		}
	}
	return false
}

func mountParentDirectories(mounts []bindMount) []string {
	directories := map[string]struct{}{}
	for _, mount := range mounts {
		for dir := filepath.Dir(mount.dest); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
			directories[dir] = struct{}{}
		}
	}

	result := make([]string, 0, len(directories))
	for directory := range directories {
		result = append(result, directory)
	}
	sort.Slice(result, func(i, j int) bool {
		leftDepth := strings.Count(filepath.Clean(result[i]), string(filepath.Separator))
		rightDepth := strings.Count(filepath.Clean(result[j]), string(filepath.Separator))
		if leftDepth == rightDepth {
			return result[i] < result[j]
		}
		return leftDepth < rightDepth
	})
	return result
}

func protectedMasks(policy Policy) ([][]string, error) {
	paths := staticProtectedAbsolutePaths(policy)
	result := make([][]string, 0, len(paths))
	for _, path := range paths {
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// An empty tmpfs mount reserves an absent protected name, so a worker
			// cannot create the host path later in a workspace-write sandbox.
			result = append(result, []string{"--tmpfs", path})
		case err != nil:
			return nil, fmt.Errorf("inspect protected path %q: %w", path, err)
		case info.IsDir():
			result = append(result, []string{"--tmpfs", path})
		case info.Mode().IsRegular():
			result = append(result, []string{"--ro-bind", "/dev/null", path})
		default:
			return nil, fmt.Errorf("protected path %q must be a regular file or directory", path)
		}
	}
	return result, nil
}

func existingLinuxRuntimeMounts() []string {
	// These are runtime dependencies, not user-configured read roots. Missing
	// optional files are omitted rather than replaced with a broad /etc or /
	// mount; a worker that needs more must receive an explicit read-only root.
	candidates := []string{
		"/bin",
		"/etc/group",
		"/etc/ld.so.cache",
		"/etc/ld.so.conf",
		"/etc/localtime",
		"/etc/nsswitch.conf",
		"/etc/passwd",
		"/lib",
		"/lib64",
		"/sbin",
		"/usr/bin",
		"/usr/lib",
		"/usr/lib64",
		"/usr/sbin",
	}
	roots := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, err := os.Lstat(candidate); err == nil {
			roots = append(roots, candidate)
		}
	}
	return roots
}
