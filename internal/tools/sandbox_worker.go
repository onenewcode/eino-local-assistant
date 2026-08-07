package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// SandboxWorkerRequest is the private parent-to-worker protocol used for
// side-effecting tools. The worker is expected to be started inside an OS
// sandbox; it repeats workspace validation instead of trusting the parent.
type SandboxWorkerRequest struct {
	Kind string `json:"kind"`

	WorkspaceRoot string `json:"workspace_root"`
	WorkingDir    string `json:"working_dir,omitempty"`

	Command        string `json:"command,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MaxOutputBytes int    `json:"max_output_bytes,omitempty"`
	// ReadOnly is parent-only execution metadata. The model cannot select this
	// protocol directly; SandboxRunner maps it to the OS policy before launch.
	ReadOnly bool `json:"read_only,omitempty"`

	Operations    []PatchOperation `json:"operations,omitempty"`
	PatchMaxBytes int              `json:"patch_max_bytes,omitempty"`
}

// SandboxWorkerResponse is deliberately JSON-only so a worker's command
// stdout cannot be confused with the control protocol.
type SandboxWorkerResponse struct {
	Shell *ShellOutput      `json:"shell,omitempty"`
	Patch *ApplyPatchOutput `json:"patch,omitempty"`
	Error string            `json:"error,omitempty"`
}

const (
	sandboxWorkerShell = "shell"
	sandboxWorkerPatch = "apply_patch"
)

// RunSandboxWorker serves one request from in and writes one response to out.
// It is called only by the hidden executable entrypoint; normal tool calls go
// through the parent process so approval remains in the TUI.
func RunSandboxWorker(in io.Reader, out io.Writer) error {
	if err := sealWorkerInheritedDescriptors(); err != nil {
		return fmt.Errorf("seal sandbox worker file descriptors: %w", err)
	}
	ctx, stop := workerSignalContext(context.Background())
	defer stop()
	return runSandboxWorker(ctx, in, out)
}

func runSandboxWorker(ctx context.Context, in io.Reader, out io.Writer) error {
	if in == nil || out == nil {
		return errors.New("sandbox worker requires input and output")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var req SandboxWorkerRequest
	if err := json.NewDecoder(io.LimitReader(in, 8<<20)).Decode(&req); err != nil {
		return writeSandboxWorkerResponse(out, SandboxWorkerResponse{Error: "decode request: " + err.Error()})
	}

	root := strings.TrimSpace(req.WorkspaceRoot)
	if root == "" {
		return writeSandboxWorkerResponse(out, SandboxWorkerResponse{Error: "workspace_root is required"})
	}

	switch strings.TrimSpace(req.Kind) {
	case sandboxWorkerShell:
		// Parent already applied product permissions and human approval. The
		// worker reuses runShell only as an execution helper: ApprovalNever
		// turns remaining "ask" into allow, while built-in cautious denies
		// remain a secondary fail-closed filter. Custom parent rules are not
		// re-sent here; parent is the sole policy authority for allow/ask.
		result, err := runShell(ctx, ShellOptions{
			TimeoutSeconds: req.TimeoutSeconds,
			MaxOutputBytes: req.MaxOutputBytes,
			WorkingDir:     req.WorkingDir,
			Approval:       ApprovalNever,
			WorkspaceOnly:  true,
			WorkspaceRoot:  root,
		}, ShellInput{
			Command:        req.Command,
			WorkingDir:     req.WorkingDir,
			TimeoutSeconds: req.TimeoutSeconds,
		})
		if err != nil {
			return writeSandboxWorkerResponse(out, SandboxWorkerResponse{Error: err.Error()})
		}
		return writeSandboxWorkerResponse(out, SandboxWorkerResponse{Shell: &result})
	case sandboxWorkerPatch:
		if err := ctx.Err(); err != nil {
			return writeSandboxWorkerResponse(out, SandboxWorkerResponse{Error: err.Error()})
		}
		result, err := applyPatch(ctx, ApplyPatchOptions{
			WorkspaceRoot: root,
			MaxBytes:      req.PatchMaxBytes,
			Approval:      ApprovalNever,
		}, ApplyPatchInput{Operations: req.Operations})
		if err != nil {
			return writeSandboxWorkerResponse(out, SandboxWorkerResponse{Error: err.Error()})
		}
		return writeSandboxWorkerResponse(out, SandboxWorkerResponse{Patch: &result})
	default:
		return writeSandboxWorkerResponse(out, SandboxWorkerResponse{Error: fmt.Sprintf("unsupported worker kind %q", req.Kind)})
	}
}
func writeSandboxWorkerResponse(out io.Writer, response SandboxWorkerResponse) error {
	return json.NewEncoder(out).Encode(response)
}
