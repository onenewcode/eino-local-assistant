package sandbox

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// SeatbeltProfile builds a macOS Seatbelt profile for a worker. It is exposed
// separately so callers can inspect the policy recorded with an execution.
func SeatbeltProfile(policy Policy, workerPath string) (string, error) {
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		return "", err
	}
	worker, err := normalizeWorkerPath(workerPath)
	if err != nil {
		return "", err
	}
	return seatbeltProfile(normalized, worker), nil
}

// BuildSeatbeltCommand builds a sandbox-exec invocation without checking that
// sandbox-exec is installed. BuildCommand performs that availability check for
// the current platform.
func BuildSeatbeltCommand(policy Policy, workerPath string, workerArgs []string) (CommandSpec, error) {
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		return CommandSpec{}, err
	}
	worker, err := normalizeWorkerPath(workerPath)
	if err != nil {
		return CommandSpec{}, err
	}
	return buildSeatbeltCommand(normalized, worker, workerArgs, "sandbox-exec"), nil
}

func buildSeatbeltCommand(policy Policy, workerPath string, workerArgs []string, sandboxExec string) CommandSpec {
	args := make([]string, 0, len(workerArgs)+3)
	args = append(args, "-p", seatbeltProfile(policy, workerPath), workerPath)
	args = append(args, workerArgs...)
	return CommandSpec{
		Backend: BackendSeatbelt,
		Path:    sandboxExec,
		Args:    args,
		Dir:     policy.Workspace,
		Env:     sandboxEnvironment(policy),
	}
}

func seatbeltProfile(policy Policy, workerPath string) string {
	var profile strings.Builder
	profile.WriteString("(version 1)\n")
	profile.WriteString("(deny default)\n")
	writeMacOSWorkerRuntimeRules(&profile)
	profile.WriteString("(allow sysctl-read)\n")
	// dyld reads the root vnode while resolving its loader paths. This is a
	// literal-only grant: it does not permit reads beneath /, and metadata-only
	// access was insufficient for the Go worker to start on macOS.
	writeSeatbeltRule(&profile, "allow", "file-read*", "literal", "/")
	// /etc, /tmp, and /var are compatibility symlinks rather than directories.
	// The directory-metadata rule below cannot traverse a symlink vnode, so
	// allow only each alias inode before the corresponding narrow subpath rule.
	for _, alias := range macOSPathAliases {
		writeSeatbeltRule(&profile, "allow", "file-read*", "literal", alias.alias)
	}
	// dyld and Go's runtime resolve every ancestor while loading the worker.
	// Directory metadata does not grant file contents or directory enumeration,
	// but allows that path traversal under a deny-default profile.
	profile.WriteString("(allow file-read-metadata (vnode-type DIRECTORY))\n")

	// Dynamic libraries and the Go runtime need a small, read-only system view.
	// Additional language/toolchain roots are granted only via ReadOnlyRoots.
	readRoots := append([]string{}, macOSRuntimeRoots...)
	readRoots = append(readRoots, policy.Workspace, policy.TempDir)
	readRoots = append(readRoots, policy.ReadOnlyRoots...)
	readRoots = append(readRoots, workerPath)
	for _, root := range macOSPathVariants(uniqueSortedPaths(readRoots)) {
		writeSeatbeltPathRules(&profile, "allow", "file-read*", root)
	}

	writeRoots := []string{policy.TempDir}
	if policy.Mode == WorkspaceWrite {
		writeRoots = append(writeRoots, policy.Workspace)
	}
	for _, root := range macOSPathVariants(uniqueSortedPaths(writeRoots)) {
		writeSeatbeltPathRules(&profile, "allow", "file-write*", root)
	}

	// Denials are explicit so protected files stay inaccessible even though the
	// workspace itself is readable (and, in workspace-write mode, writable).
	for _, protected := range macOSPathVariants(staticProtectedAbsolutePaths(policy)) {
		writeSeatbeltPathRules(&profile, "deny", "file-read*", protected)
		writeSeatbeltPathRules(&profile, "deny", "file-write*", protected)
		writeSeatbeltPathRules(&profile, "deny", "process-exec*", protected)
	}

	// The sandbox boundary is filesystem-only. Developer tools and package
	// managers must retain ordinary host network access in every filesystem mode.
	profile.WriteString("(allow network*)\n")
	return profile.String()
}

// writeMacOSWorkerRuntimeRules allows the local kernel and Mach primitives
// needed by a Go worker and its same-sandbox subprocesses. It deliberately
// avoids wildcard Mach lookup and general system sockets.
func writeMacOSWorkerRuntimeRules(profile *strings.Builder) {
	profile.WriteString("(allow process-exec)\n")
	profile.WriteString("(allow process-fork)\n")
	profile.WriteString("(allow process-info* (target same-sandbox))\n")
	profile.WriteString("(allow signal (target same-sandbox))\n")
	profile.WriteString("(allow mach-priv-task-port (target same-sandbox))\n")
	profile.WriteString("(allow ipc-posix-shm)\n")
	profile.WriteString("(allow ipc-posix-sem)\n")
	profile.WriteString("(allow system-socket (require-all (socket-domain AF_SYSTEM) (socket-protocol 2)))\n")
	profile.WriteString("(allow mach-lookup\n")
	for _, service := range macOSWorkerMachServices {
		profile.WriteString("  (global-name ")
		profile.WriteString(seatbeltQuote(service))
		profile.WriteString(")\n")
	}
	profile.WriteString(")\n")
	for _, device := range macOSWorkerIOCTLDevices {
		writeSeatbeltRule(profile, "allow", "file-ioctl", "literal", device)
	}
	// os/exec opens /dev/null for child stdin/stdout/stderr setup. Restrict the
	// data access to that character device rather than granting /dev broadly.
	profile.WriteString("(allow file-ioctl file-read-data file-write-data\n")
	profile.WriteString("  (require-all\n")
	profile.WriteString("    (literal \"/dev/null\")\n")
	profile.WriteString("    (vnode-type CHARACTER-DEVICE)))\n")
}

var macOSWorkerMachServices = []string{
	"com.apple.bsd.dirhelper",
	"com.apple.logd",
	"com.apple.system.logger",
	"com.apple.system.opendirectoryd.libinfo",
	"com.apple.system.opendirectoryd.membership",
}

var macOSWorkerIOCTLDevices = []string{
	"/dev/random",
	"/dev/urandom",
	"/dev/zero",
}

var macOSRuntimeRoots = []string{
	"/System",
	"/bin",
	"/private/etc",
	"/private/var/db/timezone",
	"/private/var/select",
	"/sbin",
	"/usr/bin",
	"/usr/lib",
}

func uniqueSortedPaths(paths []string) []string {
	set := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if path != "." && path != "" {
			set[path] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for path := range set {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

// macOSPathVariants keeps strict rules valid across Darwin's documented
// compatibility aliases (/var -> /private/var, /tmp -> /private/tmp, and
// /etc -> /private/etc). The policy stores canonical paths, while a tool may
// still receive the caller's alias spelling in its working-directory input.
// Each variant preserves the original narrow suffix; this never grants an
// entire alias root such as /var.
func macOSPathVariants(paths []string) []string {
	set := make(map[string]struct{}, len(paths)*2)
	for _, path := range paths {
		path = filepath.Clean(path)
		if path == "." || path == "" {
			continue
		}
		set[path] = struct{}{}
		for _, alias := range macOSPathAliases {
			if path == alias.canonical {
				set[alias.alias] = struct{}{}
				continue
			}
			if strings.HasPrefix(path, alias.canonical+"/") {
				set[alias.alias+strings.TrimPrefix(path, alias.canonical)] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(set))
	for path := range set {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

type macOSPathAlias struct {
	canonical string
	alias     string
}

var macOSPathAliases = []macOSPathAlias{
	{canonical: "/private/etc", alias: "/etc"},
	{canonical: "/private/tmp", alias: "/tmp"},
	{canonical: "/private/var", alias: "/var"},
}

func writeSeatbeltRule(profile *strings.Builder, verdict, operation, filter, value string) {
	fmt.Fprintf(profile, "(%s %s (%s %s))\n", verdict, operation, filter, seatbeltQuote(value))
}

// writeSeatbeltPathRules covers the target vnode and descendants separately.
// Seatbelt's subpath filter is not a substitute for a literal rule when a
// worker stats, creates, renames, or protects the root path itself.
func writeSeatbeltPathRules(profile *strings.Builder, verdict, operation, value string) {
	writeSeatbeltRule(profile, verdict, operation, "literal", value)
	if filepath.Clean(value) != string(filepath.Separator) {
		writeSeatbeltRule(profile, verdict, operation, "subpath", value)
	}
}

func seatbeltQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\r", "\\r")
	return "\"" + value + "\""
}
