package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"eino-local-assistant/internal/sandbox"
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

	relay, err := startSandboxProxyRelay()
	if err != nil {
		return writeSandboxWorkerResponse(out, SandboxWorkerResponse{Error: "start sandbox network relay: " + err.Error()})
	}
	if relay != nil {
		defer relay.Close()
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

// sandboxProxyRelay exposes the host-side Unix proxy socket on the worker's
// private loopback interface. It is used only inside Linux bwrap's network
// namespace; macOS workers receive neither relay environment variable.
type sandboxProxyRelay struct {
	listener net.Listener
	socket   string
	done     chan struct{}
	once     sync.Once
}

func startSandboxProxyRelay() (*sandboxProxyRelay, error) {
	socket := strings.TrimSpace(os.Getenv(sandbox.EnvSandboxProxySocket))
	portRaw := strings.TrimSpace(os.Getenv(sandbox.EnvSandboxProxyPort))
	if socket == "" && portRaw == "" {
		return nil, nil
	}
	if socket == "" || portRaw == "" {
		return nil, errors.New("incomplete relay configuration")
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid relay port")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	relay := &sandboxProxyRelay{listener: listener, socket: socket, done: make(chan struct{})}
	go relay.acceptLoop()
	return relay, nil
}

func (r *sandboxProxyRelay) acceptLoop() {
	defer close(r.done)
	for {
		client, err := r.listener.Accept()
		if err != nil {
			return
		}
		go r.relay(client)
	}
}

func (r *sandboxProxyRelay) relay(client net.Conn) {
	defer client.Close()
	upstream, err := net.DialTimeout("unix", r.socket, 10*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

func (r *sandboxProxyRelay) Close() error {
	if r == nil {
		return nil
	}
	var err error
	r.once.Do(func() {
		err = r.listener.Close()
		<-r.done
	})
	return err
}

func writeSandboxWorkerResponse(out io.Writer, response SandboxWorkerResponse) error {
	return json.NewEncoder(out).Encode(response)
}
