package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ToolchainVisibility controls how much of the parent process environment is
// made available to a sandbox worker.
type ToolchainVisibility string

const (
	// ToolchainVisibilityAuto exposes safe toolchain paths discovered from the
	// parent environment and mounts them read-only.
	ToolchainVisibilityAuto ToolchainVisibility = "auto"
	// ToolchainVisibilityExplicit keeps only the sandbox's built-in runtime
	// environment and caller-supplied read-only roots.
	ToolchainVisibilityExplicit ToolchainVisibility = "explicit"
)

// EnvironmentSnapshot is a session-stable, filtered view of the host
// environment. Values are never copied from arbitrary host variables.
type EnvironmentSnapshot struct {
	Mode          ToolchainVisibility
	Variables     []string
	PathEntries   []string
	ReadOnlyRoots []string
	CacheRoots    []string
}

// NormalizeToolchainVisibility validates the configured environment mode.
// Empty values use the development-friendly automatic mode.
func NormalizeToolchainVisibility(raw ToolchainVisibility) (ToolchainVisibility, error) {
	switch ToolchainVisibility(strings.ToLower(strings.TrimSpace(string(raw)))) {
	case "", ToolchainVisibilityAuto:
		return ToolchainVisibilityAuto, nil
	case ToolchainVisibilityExplicit:
		return ToolchainVisibilityExplicit, nil
	default:
		return "", fmt.Errorf("sandbox toolchain visibility must be %q or %q", ToolchainVisibilityAuto, ToolchainVisibilityExplicit)
	}
}

func normalizeEnvironmentSnapshot(snapshot EnvironmentSnapshot) (EnvironmentSnapshot, error) {
	mode := snapshot.Mode
	if mode == "" {
		mode = ToolchainVisibilityExplicit
	}
	mode, err := NormalizeToolchainVisibility(mode)
	if err != nil {
		return EnvironmentSnapshot{}, err
	}

	paths := make([]string, 0, len(snapshot.PathEntries))
	seenPaths := make(map[string]struct{}, len(snapshot.PathEntries))
	for _, raw := range snapshot.PathEntries {
		path := filepath.Clean(strings.TrimSpace(raw))
		if path == "." || !filepath.IsAbs(path) {
			return EnvironmentSnapshot{}, errors.New("sandbox environment PATH entries must be absolute")
		}
		if _, ok := seenPaths[path]; ok {
			continue
		}
		seenPaths[path] = struct{}{}
		paths = append(paths, path)
	}
	paths = uniquePaths(paths)

	variables := make([]string, 0, len(snapshot.Variables))
	seenVariables := make(map[string]struct{}, len(snapshot.Variables))
	for _, raw := range snapshot.Variables {
		name, value, ok := strings.Cut(raw, "=")
		if !ok || !safeInheritedEnvironmentName(name) || name == "PATH" || strings.ContainsAny(value, "\x00\r\n") {
			return EnvironmentSnapshot{}, fmt.Errorf("sandbox environment variable %q is not allowed", name)
		}
		if _, ok := seenVariables[name]; ok {
			continue
		}
		seenVariables[name] = struct{}{}
		variables = append(variables, name+"="+value)
	}
	sort.Strings(variables)

	return EnvironmentSnapshot{
		Mode:          mode,
		Variables:     variables,
		PathEntries:   paths,
		ReadOnlyRoots: sortedUniquePaths(snapshot.ReadOnlyRoots),
		CacheRoots:    sortedUniquePaths(snapshot.CacheRoots),
	}, nil
}

// DiscoverHostEnvironment builds a deterministic environment snapshot without
// executing any host tool. Filesystem inspection is limited to PATH and a
// fixed allowlist of toolchain/cache variables.
func DiscoverHostEnvironment(raw []string, workspace string, mode ToolchainVisibility) (EnvironmentSnapshot, error) {
	visibility, err := NormalizeToolchainVisibility(mode)
	if err != nil {
		return EnvironmentSnapshot{}, err
	}
	workspace, err = canonicalExistingDirectory("workspace", workspace)
	if err != nil {
		return EnvironmentSnapshot{}, err
	}

	values := parseEnvironment(raw)
	pathEntries := defaultSandboxPathEntries()
	variables := make(map[string]string)
	readOnlyRoots := make(map[string]struct{})
	cacheRoots := make(map[string]struct{})
	hostHome := values["HOME"]
	if hostHome != "" {
		hostHome, _ = canonicalExistingDirectory("HOME", hostHome)
	}

	if visibility == ToolchainVisibilityAuto {
		pathEntries = append(splitPathList(values["PATH"]), pathEntries...)
		for name, value := range values {
			if !safeInheritedEnvironmentName(name) || name == "PATH" {
				continue
			}
			if strings.ContainsAny(value, "\x00\r\n") {
				continue
			}
			variables[name] = value
		}
	}

	pathEntries = normalizePathEntries(pathEntries)
	for _, entry := range pathEntries {
		addEnvironmentRoot(readOnlyRoots, entry, workspace, hostHome)
		addSymlinkTargetRoots(readOnlyRoots, entry, workspace, hostHome)
	}

	if visibility == ToolchainVisibilityAuto {
		for name, value := range values {
			if !toolchainPathVariable(name) {
				continue
			}
			for _, candidate := range splitPathList(value) {
				root, ok := existingDirectoryOrParent(candidate)
				if !ok {
					continue
				}
				if addEnvironmentRoot(readOnlyRoots, root, workspace, hostHome) && cachePathVariable(name) {
					cacheRoots[filepath.Clean(root)] = struct{}{}
				}
			}
		}
	}

	return EnvironmentSnapshot{
		Mode:          visibility,
		Variables:     sortedEnvironment(variables),
		PathEntries:   uniquePaths(pathEntries),
		ReadOnlyRoots: sortedMapKeys(readOnlyRoots),
		CacheRoots:    sortedMapKeys(cacheRoots),
	}, nil
}

// ForExecution renders the snapshot with per-execution writable locations. It
// intentionally remains a complete replacement environment for the child.
func (snapshot EnvironmentSnapshot) ForExecution(tempDir string) []string {
	values := make(map[string]string, len(snapshot.Variables)+16)
	for _, entry := range snapshot.Variables {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	values["HOME"] = tempDir
	values["TMPDIR"] = tempDir
	values["TMP"] = tempDir
	values["TEMP"] = tempDir
	pathEntries := snapshot.PathEntries
	if len(pathEntries) == 0 {
		pathEntries = defaultSandboxPathEntries()
	}
	values["PATH"] = strings.Join(pathEntries, string(os.PathListSeparator))
	values["PWD"] = ""
	values["OLDPWD"] = ""
	values["XDG_CACHE_HOME"] = filepath.Join(tempDir, "cache")
	values["GOCACHE"] = filepath.Join(tempDir, "go-build")
	values["CARGO_TARGET_DIR"] = filepath.Join(tempDir, "cargo-target")
	values["NPM_CONFIG_CACHE"] = filepath.Join(tempDir, "npm-cache")
	values["npm_config_cache"] = filepath.Join(tempDir, "npm-cache")
	values["PIP_CACHE_DIR"] = filepath.Join(tempDir, "pip-cache")
	values["POETRY_CACHE_DIR"] = filepath.Join(tempDir, "poetry-cache")

	return sortedEnvironment(values)
}

func parseEnvironment(raw []string) map[string]string {
	values := make(map[string]string, len(raw))
	for _, entry := range raw {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" || strings.ContainsAny(name, "=\x00\r\n") {
			continue
		}
		values[name] = value
	}
	return values
}

func safeInheritedEnvironmentName(name string) bool {
	if strings.HasPrefix(name, "LC_") {
		return true
	}
	switch name {
	case "LANG", "LANGUAGE", "TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "TZ",
		"GOPATH", "GOROOT", "GOMODCACHE", "GOTOOLCHAIN", "CARGO_HOME", "RUSTUP_HOME", "RUSTC_WRAPPER",
		"JAVA_HOME", "M2_HOME", "MAVEN_USER_HOME", "GRADLE_USER_HOME", "PNPM_HOME", "NPM_CONFIG_CACHE",
		"npm_config_cache", "PIP_CACHE_DIR", "POETRY_CACHE_DIR", "CC", "CXX", "AR", "PKG_CONFIG_PATH",
		"CGO_ENABLED", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy",
		"NO_PROXY", "no_proxy":
		return true
	default:
		return false
	}
}

func toolchainPathVariable(name string) bool {
	switch name {
	case "GOPATH", "GOROOT", "GOMODCACHE", "CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME", "M2_HOME",
		"MAVEN_USER_HOME", "GRADLE_USER_HOME", "PNPM_HOME", "NPM_CONFIG_CACHE", "npm_config_cache",
		"PIP_CACHE_DIR", "POETRY_CACHE_DIR", "CC", "CXX", "AR", "PKG_CONFIG_PATH":
		return true
	default:
		return false
	}
}

func cachePathVariable(name string) bool {
	switch name {
	case "GOPATH", "GOMODCACHE", "CARGO_HOME", "RUSTUP_HOME", "MAVEN_USER_HOME", "NPM_CONFIG_CACHE",
		"npm_config_cache", "PIP_CACHE_DIR", "POETRY_CACHE_DIR", "GRADLE_USER_HOME":
		return true
	default:
		return false
	}
}

func defaultSandboxPathEntries() []string {
	if runtime.GOOS == "darwin" {
		return []string{"/usr/local/bin", "/usr/local/sbin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	}
	return []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}
}

func splitPathList(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, string(os.PathListSeparator))
}

func normalizePathEntries(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	result := make([]string, 0, len(raw))
	for _, candidate := range raw {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" || !filepath.IsAbs(candidate) {
			continue
		}
		resolved, err := canonicalExistingDirectory("PATH", candidate)
		if err != nil || resolved == string(filepath.Separator) {
			continue
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		result = append(result, resolved)
	}
	return result
}

func addEnvironmentRoot(roots map[string]struct{}, raw, workspace, hostHome string) bool {
	root, err := canonicalExistingDirectory("environment root", raw)
	if err != nil || root == string(filepath.Separator) || root == hostHome || pathWithin(workspace, root) {
		return false
	}
	roots[root] = struct{}{}

	// A tool installed as <prefix>/bin/tool commonly loads its runtime from
	// the sibling prefix. Expose only that prefix, never the user's home root.
	if base := filepath.Base(root); (base == "bin" || base == "sbin") && filepath.Dir(root) != hostHome {
		parent := filepath.Dir(root)
		if parent != string(filepath.Separator) && parent != "/usr" {
			if !pathWithin(workspace, parent) {
				if _, err := canonicalExistingDirectory("environment prefix", parent); err == nil {
					roots[parent] = struct{}{}
				}
			}
		}
	}
	return true
}

func addSymlinkTargetRoots(roots map[string]struct{}, directory, workspace, hostHome string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			continue
		}
		if info.IsDir() {
			addEnvironmentRoot(roots, resolved, workspace, hostHome)
			continue
		}
		addEnvironmentRoot(roots, filepath.Dir(resolved), workspace, hostHome)
		addEnvironmentRoot(roots, filepath.Dir(filepath.Dir(resolved)), workspace, hostHome)
	}
}

func existingDirectoryOrParent(raw string) (string, bool) {
	if !filepath.IsAbs(raw) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		return resolved, true
	}
	return filepath.Dir(resolved), true
}

func canonicalExistingDirectory(name, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !filepath.IsAbs(raw) {
		return "", fmt.Errorf("%s must be an absolute directory", name)
	}
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", name, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", name, err)
	}
	if !info.IsDir() {
		return "", errors.New(name + " must be a directory")
	}
	return filepath.Clean(resolved), nil
}

func sortedEnvironment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func uniquePaths(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = filepath.Clean(value)
		if value == "." || value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func sortedUniquePaths(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[filepath.Clean(value)] = struct{}{}
		}
	}
	return sortedMapKeys(set)
}

func sortedMapKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
