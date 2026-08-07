package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Mode controls whether a worker may modify the workspace. The session
// temporary directory remains writable in both modes so normal tools can make
// private temporary files without writing elsewhere on the host.
type Mode string

const (
	// ReadOnly permits reads from the workspace but not host-visible workspace
	// changes.
	ReadOnly Mode = "read-only"
	// WorkspaceWrite permits writes to the workspace, except protected paths.
	WorkspaceWrite Mode = "workspace-write"
)

// Policy defines the filesystem boundary for one worker process. Network access
// remains available to support normal development tools and package managers.
// All roots must name existing directories. ProtectedPaths are anchored,
// workspace-relative literals or a literal directory followed by /**. A
// protected directory protects its descendants.
type Policy struct {
	Mode           Mode
	Workspace      string
	TempDir        string
	Environment    EnvironmentSnapshot
	ReadOnlyRoots  []string
	ProtectedPaths []string
}

// NormalizePolicy validates a policy and returns a canonical, deterministic
// copy. An empty mode defaults to WorkspaceWrite, matching the CLI default.
func NormalizePolicy(policy Policy) (Policy, error) {
	mode, err := normalizeMode(policy.Mode)
	if err != nil {
		return Policy{}, err
	}

	workspace, err := normalizeDirectory("workspace", policy.Workspace)
	if err != nil {
		return Policy{}, err
	}
	tempDir, err := normalizeDirectory("temp_dir", policy.TempDir)
	if err != nil {
		return Policy{}, err
	}
	if pathsOverlap(workspace, tempDir) {
		return Policy{}, errors.New("workspace and temp_dir must not overlap")
	}

	environment, err := normalizeEnvironmentSnapshot(policy.Environment)
	if err != nil {
		return Policy{}, err
	}
	readOnlyInputs := append([]string(nil), policy.ReadOnlyRoots...)
	readOnlyInputs = append(readOnlyInputs, environment.ReadOnlyRoots...)
	readOnlyRoots, err := normalizeReadOnlyRoots(readOnlyInputs, workspace, tempDir)
	if err != nil {
		return Policy{}, err
	}
	if err := rejectWorkspaceHardLinks(workspace); err != nil {
		return Policy{}, err
	}
	protectedPaths, err := normalizeProtectedPaths(workspace, policy.ProtectedPaths)
	if err != nil {
		return Policy{}, err
	}
	return Policy{
		Mode:           mode,
		Workspace:      workspace,
		TempDir:        tempDir,
		Environment:    environment,
		ReadOnlyRoots:  readOnlyRoots,
		ProtectedPaths: protectedPaths,
	}, nil
}

// rejectWorkspaceHardLinks prevents macOS pathname-policy escapes. Seatbelt
// evaluates reads and writes through the workspace alias, not the external
// pathname of a pre-existing hard-linked inode. Linux does not expose
// unmounted inodes, but applying the same conservative rule keeps both
// backends and both modes equivalent.
func rejectWorkspaceHardLinks(workspace string) error {
	return filepath.Walk(workspace, func(path string, candidate os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect workspace path: %w", walkErr)
		}
		if !candidate.Mode().IsRegular() {
			return nil
		}
		multiple, err := hasMultipleHardLinks(candidate)
		if err != nil {
			return fmt.Errorf("inspect workspace regular file %q: %w", path, err)
		}
		if multiple {
			return errors.New("workspace regular file has multiple hard links")
		}
		return nil
	})
}

// Normalize returns a validated, canonical copy of the policy.
func (policy Policy) Normalize() (Policy, error) {
	return NormalizePolicy(policy)
}

// WithoutTempDir returns a copy with TempDir cleared. Session runners store
// this form after a full NormalizePolicy so each execution can rebind a private
// temporary directory without repeating the workspace hard-link scan.
func (policy Policy) WithoutTempDir() Policy {
	policy.TempDir = ""
	return policy
}

// WithTempDir rebinds a session-validated policy to a fresh absolute temporary
// directory. It validates only the new temp root and overlap rules; it does
// not re-walk the workspace for hard links. Callers must start from a policy
// produced by NormalizePolicy (optionally after WithoutTempDir).
func (policy Policy) WithTempDir(tempDir string) (Policy, error) {
	if strings.TrimSpace(policy.Workspace) == "" {
		return Policy{}, errors.New("sandbox workspace is required")
	}
	if policy.Mode != ReadOnly && policy.Mode != WorkspaceWrite {
		return Policy{}, fmt.Errorf("sandbox mode must be %q or %q", ReadOnly, WorkspaceWrite)
	}
	temp, err := normalizeDirectory("temp_dir", tempDir)
	if err != nil {
		return Policy{}, err
	}
	if pathsOverlap(policy.Workspace, temp) {
		return Policy{}, errors.New("workspace and temp_dir must not overlap")
	}
	for i, root := range policy.ReadOnlyRoots {
		if pathsOverlap(root, temp) {
			return Policy{}, fmt.Errorf("sandbox read_only_roots[%d] overlaps workspace or temp_dir", i)
		}
	}
	out := policy
	out.TempDir = temp
	return out, nil
}

func normalizeMode(mode Mode) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case "", WorkspaceWrite:
		return WorkspaceWrite, nil
	case ReadOnly:
		return ReadOnly, nil
	default:
		return "", fmt.Errorf("sandbox mode must be %q or %q", ReadOnly, WorkspaceWrite)
	}
}

func normalizeDirectory(name, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("sandbox %s is required", name)
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("sandbox %s must be an absolute path", name)
	}

	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox %s: %w", name, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox %s: %w", name, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat sandbox %s: %w", name, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sandbox %s must be a directory", name)
	}
	return filepath.Clean(resolved), nil
}

func normalizeReadOnlyRoots(rawRoots []string, workspace, tempDir string) ([]string, error) {
	if len(rawRoots) == 0 {
		return nil, nil
	}

	roots := make(map[string]struct{}, len(rawRoots))
	for i, raw := range rawRoots {
		root, err := normalizeDirectory(fmt.Sprintf("read_only_roots[%d]", i), raw)
		if err != nil {
			return nil, err
		}
		if pathsOverlap(root, workspace) || pathsOverlap(root, tempDir) {
			return nil, fmt.Errorf("sandbox read_only_roots[%d] overlaps workspace or temp_dir", i)
		}
		roots[root] = struct{}{}
	}

	result := make([]string, 0, len(roots))
	for root := range roots {
		result = append(result, root)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeProtectedPaths(workspace string, rawPaths []string) ([]string, error) {
	if len(rawPaths) == 0 {
		return nil, nil
	}

	paths := make(map[string]struct{}, len(rawPaths))
	for i, raw := range rawPaths {
		pattern, err := parseProtectedPattern(raw)
		if err != nil {
			return nil, fmt.Errorf("sandbox protected_paths[%d]: %w", i, err)
		}
		if err := rejectSymlinkComponent(workspace, pattern.literalPrefix()); err != nil {
			return nil, fmt.Errorf("sandbox protected_paths[%d]: %w", i, err)
		}
		if err := rejectProtectedHardLink(workspace, pattern.literalPrefix()); err != nil {
			return nil, fmt.Errorf("sandbox protected_paths[%d]: %w", i, err)
		}
		paths[pattern.raw] = struct{}{}
	}

	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

// rejectProtectedHardLink closes a pathname-mask bypass: a pre-existing hard
// link to a protected regular file would otherwise expose the same inode under
// an unprotected workspace name on both Seatbelt and bubblewrap. Directories
// are masked as a whole, so every regular descendant must be checked as well.
func rejectProtectedHardLink(workspace, relative string) error {
	protected := filepath.Join(workspace, filepath.FromSlash(relative))
	_, err := os.Lstat(protected)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect path: %w", err)
	}

	return filepath.Walk(protected, func(path string, candidate os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect protected path: %w", walkErr)
		}
		if !candidate.Mode().IsRegular() {
			return nil
		}
		multiple, err := hasMultipleHardLinks(candidate)
		if err != nil {
			return fmt.Errorf("inspect protected regular file %q: %w", path, err)
		}
		if multiple {
			return errors.New("protected regular file has multiple hard links")
		}
		return nil
	})
}

type protectedPattern struct {
	raw       string
	recursive bool
}

func parseProtectedPattern(raw string) (protectedPattern, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return protectedPattern{}, errors.New("pattern is required")
	}
	if strings.ContainsRune(raw, 0) || strings.ContainsAny(raw, "\r\n") {
		return protectedPattern{}, errors.New("pattern contains an unsupported control character")
	}
	if strings.ContainsRune(raw, '\\') {
		return protectedPattern{}, errors.New("pattern must use slash separators without escapes")
	}
	if filepath.IsAbs(raw) {
		return protectedPattern{}, errors.New("pattern must be workspace-relative")
	}

	parts := strings.Split(raw, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return protectedPattern{}, errors.New("pattern must not contain empty, dot, or parent segments")
		}
	}

	pattern := protectedPattern{raw: strings.Join(parts, "/")}
	if strings.HasSuffix(pattern.raw, "/**") {
		prefix := strings.TrimSuffix(pattern.raw, "/**")
		if strings.ContainsAny(prefix, "*?[]") {
			return protectedPattern{}, errors.New("recursive patterns must use a literal directory prefix")
		}
		pattern.raw = prefix + "/**"
		pattern.recursive = true
		return pattern, nil
	}
	if strings.ContainsAny(pattern.raw, "*?[]") {
		return protectedPattern{}, errors.New("glob metacharacters are unsupported; use a literal path or literal /** subtree")
	}
	return pattern, nil
}

func (pattern protectedPattern) literalPrefix() string {
	return strings.TrimSuffix(pattern.raw, "/**")
}

// rejectSymlinkComponent prevents a protected path declaration from resolving
// through a workspace symlink before the OS backend can install its mask.
func rejectSymlinkComponent(workspace, relative string) error {
	if relative == "" {
		return nil
	}
	parts := strings.Split(relative, "/")
	current := workspace
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if i != len(parts)-1 {
				return errors.New("path parent does not exist")
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("path traverses a symlink")
		}
		if i != len(parts)-1 && !info.IsDir() {
			return errors.New("path parent is not a directory")
		}
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	return pathWithin(left, right) || pathWithin(right, left)
}

func pathWithin(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// ProtectedMatcher matches candidate paths relative to the workspace. Matching
// is always anchored; V1 accepts literal paths and literal /** subtree paths
// only, which keeps the same protection semantics on macOS and Linux.
type ProtectedMatcher struct {
	patterns []protectedPattern
}

// NewProtectedMatcher compiles protected path patterns independently of a
// concrete workspace. It is useful to guard worker-side file operations before
// an OS backend is started.
func NewProtectedMatcher(patterns []string) (ProtectedMatcher, error) {
	compiled := make([]protectedPattern, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for i, raw := range patterns {
		pattern, err := parseProtectedPattern(raw)
		if err != nil {
			return ProtectedMatcher{}, fmt.Errorf("protected pattern[%d]: %w", i, err)
		}
		if _, ok := seen[pattern.raw]; ok {
			continue
		}
		seen[pattern.raw] = struct{}{}
		compiled = append(compiled, pattern)
	}
	sort.Slice(compiled, func(i, j int) bool { return compiled[i].raw < compiled[j].raw })
	return ProtectedMatcher{patterns: compiled}, nil
}

// ProtectedMatcher returns a matcher for this policy's normalized patterns.
func (policy Policy) ProtectedMatcher() (ProtectedMatcher, error) {
	return NewProtectedMatcher(policy.ProtectedPaths)
}

// Matches reports whether a relative workspace path is protected. Invalid or
// non-relative input does not match; callers can reject it separately.
func (matcher ProtectedMatcher) Matches(relativePath string) bool {
	candidate, err := normalizeMatchPath(relativePath)
	if err != nil {
		return false
	}
	for _, pattern := range matcher.patterns {
		base := pattern.literalPrefix()
		if candidate == base || strings.HasPrefix(candidate, base+"/") {
			return true
		}
	}
	return false
}

// Patterns returns the normalized patterns in stable order.
func (matcher ProtectedMatcher) Patterns() []string {
	patterns := make([]string, 0, len(matcher.patterns))
	for _, pattern := range matcher.patterns {
		patterns = append(patterns, pattern.raw)
	}
	return patterns
}

func normalizeMatchPath(raw string) (string, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.ContainsRune(raw, 0) || strings.ContainsAny(raw, "\r\n\\") {
		return "", errors.New("invalid relative path")
	}
	if filepath.IsAbs(raw) {
		return "", errors.New("path is absolute")
	}
	parts := strings.Split(raw, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("invalid relative path")
		}
	}
	return strings.Join(parts, "/"), nil
}

func staticProtectedAbsolutePaths(policy Policy) []string {
	paths := make([]string, 0, len(policy.ProtectedPaths))
	for _, raw := range policy.ProtectedPaths {
		// Callers use a normalized policy, so only a literal or literal /** can
		// reach this backend helper.
		prefix := strings.TrimSuffix(raw, "/**")
		paths = append(paths, filepath.Join(policy.Workspace, filepath.FromSlash(prefix)))
	}
	return paths
}
