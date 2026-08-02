//go:build linux

package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"eino-local-assistant/internal/sandbox"
)

func TestSandboxRunnerLinuxRelayPreventsDirectNetworkAndUsesProxy(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("bwrap is unavailable")
	}

	workspace := t.TempDir()
	worker := buildSandboxWorkerBinary(t)
	probe := buildSandboxNetworkProbe(t, workspace)
	directTarget := startDirectNetworkTarget(t)
	observations := &relayObservations{}
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:          sandbox.WorkspaceWrite,
		WorkspaceRoot: workspace,
		AllowedHosts:  []string{"allowed.test"},
		WorkerPath:    worker,
		startUnixProxy: func(_ []string, socketPath string) (sandboxProxy, error) {
			return startRelayTestProxy(socketPath, observations)
		},
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	direct := executeLinuxSandboxProbe(t, ctx, runner, shellQuote(probe)+" direct "+shellQuote(directTarget))
	if direct.ExitCode == 0 {
		t.Fatalf("direct host network unexpectedly succeeded: %#v", direct)
	}

	allowed := executeLinuxSandboxProbe(t, ctx, runner, shellQuote(probe)+" proxy allowed.test")
	if allowed.ExitCode != 0 || !strings.Contains(allowed.Stdout, "allowed response") {
		t.Fatalf("allowlisted proxy request failed: %#v", allowed)
	}

	for _, target := range []string{"blocked.test", "127.0.0.1"} {
		blocked := executeLinuxSandboxProbe(t, ctx, runner, shellQuote(probe)+" proxy "+shellQuote(target))
		if blocked.ExitCode == 0 {
			t.Fatalf("proxy target %q unexpectedly succeeded: %#v", target, blocked)
		}
	}

	if got := observations.List(); !containsAllHosts(got, "allowed.test", "blocked.test", "127.0.0.1") {
		t.Fatalf("relay did not receive expected proxy targets: %#v", got)
	}
}

func TestSandboxRunnerLinuxFailsClosedWithoutBwrap(t *testing.T) {
	workspace := t.TempDir()
	marker := filepath.Join(t.TempDir(), "host-fallback-ran")
	worker := filepath.Join(t.TempDir(), "host-fallback")
	script := "#!/bin/sh\nprintf host > " + shellQuote(marker) + "\n"
	if err := os.WriteFile(worker, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:          sandbox.WorkspaceWrite,
		WorkspaceRoot: workspace,
		WorkerPath:    worker,
		currentAvailability: func() sandbox.Availability {
			return sandbox.Availability{Backend: sandbox.BackendBubblewrap, Reason: "bwrap unavailable for test"}
		},
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	_, _, err = runner.Execute(context.Background(), SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        "true",
		WorkingDir:     workspace,
		TimeoutSeconds: 1,
		MaxOutputBytes: 1024,
	})
	if !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("Execute() error = %v, want sandbox.ErrUnavailable", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unavailable sandbox executed host worker: stat marker = %v", statErr)
	}
}

func executeLinuxSandboxProbe(t *testing.T, ctx context.Context, runner *SandboxRunner, command string) ShellOutput {
	t.Helper()
	response, outcome, err := runner.Execute(ctx, SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        command,
		WorkingDir:     runner.basePolicy.Workspace,
		TimeoutSeconds: 10,
		MaxOutputBytes: 16 << 10,
	})
	if err != nil {
		if linuxSandboxCannotCreateNamespace(err) {
			t.Skipf("bwrap is present but cannot create a sandbox here: %v", err)
		}
		t.Fatalf("sandbox worker: %v", err)
	}
	if response.Error != "" {
		t.Fatalf("sandbox worker response error: %s", response.Error)
	}
	if response.Shell == nil {
		t.Fatalf("sandbox worker response = %#v", response)
	}
	if outcome.Backend != string(sandbox.BackendBubblewrap) || !outcome.Enforced || outcome.Network != "allow:1" {
		t.Fatalf("sandbox outcome = %#v", outcome)
	}
	return *response.Shell
}

func linuxSandboxCannotCreateNamespace(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"operation not permitted",
		"permission denied",
		"no permissions to create new namespace",
		"creating new namespace",
		"user namespace",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func buildSandboxWorkerBinary(t *testing.T) string {
	t.Helper()
	worker := filepath.Join(t.TempDir(), "eino-assistant-worker")
	command := exec.Command("go", "build", "-o", worker, "../../cmd/eino-assistant")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build sandbox worker: %v\n%s", err, output)
	}
	return worker
}

func buildSandboxNetworkProbe(t *testing.T, workspace string) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "probe.go")
	const program = `package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: probe direct|proxy target")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "direct":
		connection, err := net.DialTimeout("tcp", os.Args[2], 2*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		connection.Close()
		fmt.Println("direct response")
	case "proxy":
		proxyURL, err := url.Parse(os.Getenv("HTTP_PROXY"))
		if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
			fmt.Fprintln(os.Stderr, "missing HTTP_PROXY")
			os.Exit(2)
		}
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		response, err := client.Get("http://" + os.Args[2] + "/")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			fmt.Fprintln(os.Stderr, response.Status)
			os.Exit(1)
		}
		fmt.Print(string(body))
	default:
		fmt.Fprintln(os.Stderr, "unknown mode")
		os.Exit(2)
	}
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(workspace, "network-probe")
	command := exec.Command("go", "build", "-o", probe, source)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build network probe: %v\n%s", err, output)
	}
	return probe
}

func startDirectNetworkTarget(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	return listener.Addr().String()
}

type relayObservations struct {
	mu    sync.Mutex
	hosts []string
}

func (o *relayObservations) Add(host string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hosts = append(o.hosts, host)
}

func (o *relayObservations) List() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.hosts...)
}

type relayTestProxy struct {
	listener net.Listener
	done     chan struct{}
	wg       sync.WaitGroup
	seen     *relayObservations
	once     sync.Once
}

func startRelayTestProxy(socketPath string, seen *relayObservations) (sandboxProxy, error) {
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	proxy := &relayTestProxy{listener: listener, done: make(chan struct{}), seen: seen}
	go proxy.acceptLoop()
	return proxy, nil
}

func (p *relayTestProxy) acceptLoop() {
	defer close(p.done)
	for {
		connection, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer connection.Close()
			p.serve(connection)
		}()
	}
}

func (p *relayTestProxy) serve(connection net.Conn) {
	request, err := http.ReadRequest(bufio.NewReader(connection))
	if err != nil {
		return
	}
	defer request.Body.Close()
	host := request.URL.Hostname()
	p.seen.Add(host)
	status := http.StatusForbidden
	body := "blocked"
	if host == "allowed.test" {
		status = http.StatusOK
		body = "allowed response"
	}
	response := &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Close:         true,
	}
	response.Header.Set("Connection", "close")
	response.Header.Set("Content-Type", "text/plain")
	_ = response.Write(connection)
}

func (p *relayTestProxy) Close() error {
	if p == nil {
		return nil
	}
	var err error
	p.once.Do(func() {
		err = p.listener.Close()
		<-p.done
		p.wg.Wait()
	})
	return err
}

func containsAllHosts(haystack []string, want ...string) bool {
	seen := make(map[string]struct{}, len(haystack))
	for _, host := range haystack {
		seen[host] = struct{}{}
	}
	for _, host := range want {
		if _, ok := seen[host]; !ok {
			return false
		}
	}
	return true
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
