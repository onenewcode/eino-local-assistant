package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/runtimeguard"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/tools"

	"github.com/cloudwego/eino/schema"
)

type fakeExecSession struct {
	prompt string
	chunks []string
	err    error
	id     string
	usage  *chat.UsageSummary
}

func (s *fakeExecSession) ID() string {
	if s.id != "" {
		return s.id
	}
	return "exec-test-session"
}

func (s *fakeExecSession) execUsageSummary() (chat.UsageSummary, bool) {
	if s.usage == nil {
		return chat.UsageSummary{}, false
	}
	return *s.usage, true
}

func (s *fakeExecSession) Ask(_ context.Context, prompt string, onChunk func(string) error) error {
	s.prompt = prompt
	for _, chunk := range s.chunks {
		if err := onChunk(chunk); err != nil {
			return err
		}
	}
	return s.err
}

type failingExecReader struct{ err error }

func (r failingExecReader) Read([]byte) (int, error) { return 0, r.err }

type unreadExecReader struct{ reads int }

func (r *unreadExecReader) Read([]byte) (int, error) {
	r.reads++
	return 0, errors.New("stdin must not be read")
}

type failingExecWriter struct {
	err    error
	writes int
}

func (w *failingExecWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, w.err
}

type failingAfterExecWriter struct {
	bytes.Buffer
	err    error
	failAt int
	writes int
}

func (w *failingAfterExecWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, w.err
	}
	return w.Buffer.Write(p)
}

type shortExecWriter struct{ writes int }

func (w *shortExecWriter) Write(p []byte) (int, error) {
	w.writes++
	return len(p) - 1, nil
}

type eventExecSession struct {
	fakeExecSession
	events     []chat.TurnEvent
	concurrent bool
}

func (s *eventExecSession) AskWithEvents(ctx context.Context, prompt string, onChunk func(string) error, emit chat.EventEmitter) error {
	s.prompt = prompt
	if s.concurrent {
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, event := range s.events {
			event := event
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				emit(event)
			}()
		}
		close(start)
		wg.Wait()
	} else {
		for _, event := range s.events {
			emit(event)
		}
	}
	for _, chunk := range s.chunks {
		if err := onChunk(chunk); err != nil {
			return err
		}
	}
	return s.err
}

type blockingEventExecSession struct {
	fakeExecSession
	cancelled chan struct{}
}

func (s *blockingEventExecSession) AskWithEvents(ctx context.Context, prompt string, _ func(string) error, emit chat.EventEmitter) error {
	s.prompt = prompt
	emit(chat.TurnEvent{Kind: chat.TurnEventToolStart, Tool: "tool-secret", ToolCallID: "call-secret", Input: "args-secret"})
	<-ctx.Done()
	close(s.cancelled)
	return ctx.Err()
}

type commitCheckingExecWriter struct {
	bytes.Buffer
	check    func() error
	checkErr error
}

func (w *commitCheckingExecWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"event":"result"`)) && w.checkErr == nil {
		w.checkErr = w.check()
		if w.checkErr != nil {
			return 0, w.checkErr
		}
	}
	return w.Buffer.Write(p)
}

func executeExecForTest(input io.Reader, deps execCommandDeps, args ...string) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	err = executeExecWithWritersForTest(context.Background(), input, deps, &outBuf, &errBuf, args...)
	return outBuf.String(), errBuf.String(), err
}

func executeExecWithWritersForTest(ctx context.Context, input io.Reader, deps execCommandDeps, stdout, stderr io.Writer, args ...string) error {
	root := newRootCommandWithDeps(commandDeps{exec: deps})
	root.SetContext(ctx)
	root.SetIn(input)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	return root.Execute()
}

func decodeExecJSON(t *testing.T, stdout string) (execJSONEnvelope, map[string]json.RawMessage) {
	t.Helper()
	if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("JSON stdout must be one object plus one newline, got %q", stdout)
	}
	decoder := json.NewDecoder(strings.NewReader(stdout))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		t.Fatalf("decode JSON stdout: %v\n%s", err, stdout)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("JSON stdout contained a second record: %v\n%s", err, stdout)
	}
	for _, field := range []string{"contract_version", "status", "result", "error", "session"} {
		if _, ok := raw[field]; !ok {
			t.Fatalf("JSON stdout omitted required field %q: %s", field, stdout)
		}
	}
	for field := range raw {
		switch field {
		case "contract_version", "status", "result", "error", "session", "usage":
		default:
			t.Fatalf("JSON stdout exposed unsupported field %q: %s", field, stdout)
		}
	}
	var envelope execJSONEnvelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("unmarshal JSON stdout: %v", err)
	}
	if envelope.ContractVersion != execJSONContractVersion {
		t.Fatalf("contract_version = %d, want %d", envelope.ContractVersion, execJSONContractVersion)
	}
	switch envelope.Status {
	case execStatusCompleted:
		if envelope.Result == nil || envelope.Error != nil {
			t.Fatalf("completed envelope violates result/error invariant: %+v", envelope)
		}
	case execStatusFailed, execStatusCancelled:
		if envelope.Result != nil || envelope.Error == nil || envelope.Error.Code == "" || envelope.Error.Message == "" {
			t.Fatalf("failed envelope violates result/error invariant: %+v", envelope)
		}
	default:
		t.Fatalf("unexpected status %q", envelope.Status)
	}
	if envelope.Session.Persistent {
		if envelope.Session.ID == nil || *envelope.Session.ID == "" {
			t.Fatalf("persistent envelope has no session id: %+v", envelope.Session)
		}
	} else if envelope.Session.ID != nil {
		t.Fatalf("non-persistent envelope has a session id: %+v", envelope.Session)
	}
	return envelope, raw
}

func decodeExecStream(t *testing.T, stdout string) []map[string]json.RawMessage {
	t.Helper()
	if stdout == "" || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("stream stdout must contain newline-delimited records, got %q", stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	records := make([]map[string]json.RawMessage, 0, len(lines))
	for index, line := range lines {
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode stream record %d: %v\n%s", index, err, line)
		}
		var core struct {
			StreamVersion int    `json:"stream_version"`
			Sequence      uint64 `json:"sequence"`
			Event         string `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &core); err != nil {
			t.Fatalf("decode stream core %d: %v", index, err)
		}
		if core.StreamVersion != execStreamVersion || core.Sequence != uint64(index+1) || core.Event == "" {
			t.Fatalf("stream core %d = %+v", index, core)
		}
		records = append(records, record)
	}
	return records
}

func decodeExecStreamResult(t *testing.T, record map[string]json.RawMessage) execStreamResultRecord {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal stream result: %v", err)
	}
	var result execStreamResultRecord
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatalf("decode stream result: %v", err)
	}
	if result.Event != execStreamEventResult || result.ContractVersion != execJSONContractVersion {
		t.Fatalf("invalid stream result: %+v", result)
	}
	return result
}

func recordingExecDeps(session execSession, called *bool) execCommandDeps {
	return execCommandDeps{newSession: func(_ context.Context, _ string) (execSession, io.Closer, error) {
		*called = true
		return session, nil, nil
	}}
}

func TestExecPromptArgument(t *testing.T) {
	for _, args := range [][]string{
		{"exec", "explain this"},
		{"exec", "--output-format", "text", "explain this"},
	} {
		args := args
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			session := &fakeExecSession{chunks: []string{"done"}}
			called := false
			stdout, stderr, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(session, &called), args...)
			if err != nil {
				t.Fatalf("exec: %v", err)
			}
			if !called || session.prompt != "explain this" {
				t.Fatalf("factory=%v prompt=%q", called, session.prompt)
			}
			if stdout != "done\n" || stderr != execSessionHint(session.ID()) {
				t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

func TestExecResumeDispatchesOpenFactoryWithExactIDAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		id      string
		recover bool
	}{
		{name: "normal", args: []string{"exec", "resume", "thread-normal", "continue"}, id: "thread-normal"},
		{name: "recover", args: []string{"exec", "resume", "thread-recover", "continue", "--recover"}, id: "thread-recover", recover: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			newCalls := 0
			openCalls := 0
			var gotID string
			var gotRecover bool
			session := &fakeExecSession{id: tc.id, chunks: []string{"done"}}
			deps := execCommandDeps{
				newSession: func(context.Context, string) (execSession, io.Closer, error) {
					newCalls++
					return nil, nil, errors.New("new session must not be opened")
				},
				openSession: func(_ context.Context, _ string, id string, recoverInterrupted bool) (execSession, io.Closer, error) {
					openCalls++
					gotID = id
					gotRecover = recoverInterrupted
					return session, nil, nil
				},
			}

			stdout, stderr, err := executeExecForTest(strings.NewReader(""), deps, tc.args...)
			if err != nil {
				t.Fatalf("exec resume: %v", err)
			}
			if newCalls != 0 || openCalls != 1 || gotID != tc.id || gotRecover != tc.recover {
				t.Fatalf("new=%d open=%d id=%q recover=%v", newCalls, openCalls, gotID, gotRecover)
			}
			if session.prompt != "continue" || stdout != "done\n" || stderr != execSessionHint(tc.id) {
				t.Fatalf("prompt=%q stdout=%q stderr=%q", session.prompt, stdout, stderr)
			}
		})
	}
}

func TestExecResumeUsesExistingPromptSources(t *testing.T) {
	for _, tc := range []struct {
		name       string
		input      string
		args       []string
		wantPrompt string
	}{
		{name: "stdin", input: "from stdin", args: []string{"exec", "resume", "thread-stdin"}, wantPrompt: "from stdin"},
		{name: "dash", input: "from dash", args: []string{"exec", "resume", "thread-dash", "-"}, wantPrompt: "from dash"},
		{name: "argument with pipe", input: "build log\n</stdin>", args: []string{"exec", "resume", "thread-pipe", "fix it"}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeExecSession{id: tc.args[2], chunks: []string{"ok"}}
			called := false
			deps := execCommandDeps{openSession: func(_ context.Context, _ string, id string, recoverInterrupted bool) (execSession, io.Closer, error) {
				if id != session.ID() || recoverInterrupted {
					t.Fatalf("open id=%q recover=%v", id, recoverInterrupted)
				}
				called = true
				return session, nil, nil
			}}
			if _, _, err := executeExecForTest(strings.NewReader(tc.input), deps, tc.args...); err != nil {
				t.Fatalf("exec resume: %v", err)
			}
			if !called {
				t.Fatal("resume did not open a session")
			}
			if tc.wantPrompt != "" {
				if session.prompt != tc.wantPrompt {
					t.Fatalf("prompt=%q, want %q", session.prompt, tc.wantPrompt)
				}
				return
			}
			prefix := "fix it\n\n" + execStdinReferencePrefix + "\n"
			if !strings.HasPrefix(session.prompt, prefix) {
				t.Fatalf("prompt=%q, want prefix %q", session.prompt, prefix)
			}
			var envelope execStdinEnvelope
			if err := json.Unmarshal([]byte(strings.TrimPrefix(session.prompt, prefix)), &envelope); err != nil {
				t.Fatalf("decode stdin envelope: %v", err)
			}
			if envelope.Source != "stdin" || envelope.Content != tc.input || envelope.ByteCount != len(tc.input) {
				t.Fatalf("stdin envelope=%+v", envelope)
			}
		})
	}
}

func TestExecResumeRejectsInvalidIDAndPromptBeforeOpening(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		args  []string
		want  error
	}{
		{name: "missing id", args: []string{"exec", "resume"}},
		{name: "blank id", args: []string{"exec", "resume", "  ", "continue"}},
		{name: "too many arguments", args: []string{"exec", "resume", "thread", "one", "two"}},
		{name: "blank prompt", args: []string{"exec", "resume", "thread", " \n\t"}, want: chat.ErrEmptyInput},
		{name: "blank stdin", input: " \n\t", args: []string{"exec", "resume", "thread"}, want: chat.ErrEmptyInput},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			openCalls := 0
			deps := execCommandDeps{openSession: func(context.Context, string, string, bool) (execSession, io.Closer, error) {
				openCalls++
				return &fakeExecSession{}, nil, nil
			}}
			stdout, stderr, err := executeExecForTest(strings.NewReader(tc.input), deps, tc.args...)
			if err == nil {
				t.Fatal("expected input error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			if openCalls != 0 || stdout != "" || stderr != "" {
				t.Fatalf("open=%d stdout=%q stderr=%q", openCalls, stdout, stderr)
			}
		})
	}
}

func TestExecResumeJSONReportsPositionalInputFailuresBeforeStdinOrOpening(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing id",
			args:    []string{"exec", "resume", "--output-format", "json"},
			wantErr: "session id is required",
		},
		{
			name:    "blank id",
			args:    []string{"exec", "resume", "  ", "--output-format", "json", "continue"},
			wantErr: "session id is required",
		},
		{
			name:    "too many prompt arguments",
			args:    []string{"exec", "resume", "thread", "one", "two", "--output-format", "json"},
			wantErr: "exec resume accepts a session id and at most one prompt argument",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			stdin := &unreadExecReader{}
			newCalls := 0
			openCalls := 0
			deps := execCommandDeps{
				newSession: func(context.Context, string) (execSession, io.Closer, error) {
					newCalls++
					return nil, nil, errors.New("new session must not be opened")
				},
				openSession: func(context.Context, string, string, bool) (execSession, io.Closer, error) {
					openCalls++
					return nil, nil, errors.New("resumed session must not be opened")
				},
			}

			stdout, stderr, err := executeExecForTest(stdin, deps, tc.args...)
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("error=%v, want original input error %q", err, tc.wantErr)
			}
			if stdin.reads != 0 || newCalls != 0 || openCalls != 0 {
				t.Fatalf("stdin reads=%d new=%d open=%d, want no pre-run work", stdin.reads, newCalls, openCalls)
			}
			envelope, _ := decodeExecJSON(t, stdout)
			if envelope.Status != execStatusFailed || envelope.Error.Code != execErrorInput || envelope.Error.Message != execErrorMessageInput || envelope.Session.Persistent || envelope.Session.ID != nil {
				t.Fatalf("input failure envelope=%+v", envelope)
			}
			if stderr != "" {
				t.Fatalf("stderr=%q, want no session hint", stderr)
			}
		})
	}
}

func TestExecResumeOpenFailureUsesStartupEnvelopeAndRecoveryDispatch(t *testing.T) {
	wantErr := errors.New("durable session cannot be opened")
	for _, tc := range []struct {
		name    string
		args    []string
		recover bool
	}{
		{name: "normal", args: []string{"exec", "resume", "thread-normal", "--output-format", "json", "continue"}},
		{name: "recover", args: []string{"exec", "resume", "thread-recover", "continue", "--recover", "--output-format", "json"}, recover: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var gotID string
			var gotRecover bool
			deps := execCommandDeps{openSession: func(_ context.Context, _ string, id string, recoverInterrupted bool) (execSession, io.Closer, error) {
				gotID = id
				gotRecover = recoverInterrupted
				return nil, nil, wantErr
			}}
			stdout, stderr, err := executeExecForTest(strings.NewReader(""), deps, tc.args...)
			if !errors.Is(err, wantErr) {
				t.Fatalf("error=%v, want %v", err, wantErr)
			}
			envelope, _ := decodeExecJSON(t, stdout)
			if envelope.Status != execStatusFailed || envelope.Error.Code != execErrorStartup || envelope.Session.ID != nil || envelope.Session.Persistent {
				t.Fatalf("startup envelope=%+v", envelope)
			}
			if gotID == "" || gotRecover != tc.recover || stderr != "" {
				t.Fatalf("id=%q recover=%v stderr=%q", gotID, gotRecover, stderr)
			}
		})
	}
}

func TestExecResumeLeavesActiveTurnToOpenSessionRecovery(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	state, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: "thread-active"}, "stored system prompt")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := threadStore.StartTurn(context.Background(), state.ID, state.Revision, store.TurnStart{TurnID: "crashed-turn", Input: "unfinished"}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	open := func(_ context.Context, _ string, id string, recoverInterrupted bool) (execSession, io.Closer, error) {
		session, err := chat.OpenSession(&localExecModel{stream: &localExecStream{
			messages: []*schema.Message{schema.AssistantMessage("recovered reply", nil)},
		}}, threadStore, id, chat.SessionOptions{Store: threadStore, RecoverInterrupted: recoverInterrupted})
		return session, nil, err
	}
	deps := execCommandDeps{openSession: open}

	stdout, stderr, err := executeExecForTest(strings.NewReader(""), deps, "exec", "resume", state.ID, "continue")
	if !errors.Is(err, chat.ErrThreadHasActiveTurn) {
		t.Fatalf("normal resume error=%v, want active turn rejection", err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("normal resume stdout=%q stderr=%q", stdout, stderr)
	}
	groups, err := threadStore.LoadTurnGroups(context.Background(), state.ID)
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Failed != nil {
		t.Fatalf("normal resume modified active turn: %#v", groups)
	}

	stdout, stderr, err = executeExecForTest(strings.NewReader(""), deps, "exec", "resume", state.ID, "continue", "--recover")
	if err != nil {
		t.Fatalf("recovered resume: %v", err)
	}
	if stdout != "recovered reply\n" || stderr != execSessionHint(state.ID) {
		t.Fatalf("recovered stdout=%q stderr=%q", stdout, stderr)
	}
	groups, err = threadStore.LoadTurnGroups(context.Background(), state.ID)
	if err != nil {
		t.Fatalf("LoadTurnGroups after recovery: %v", err)
	}
	if len(groups) != 2 || groups[0].Failed == nil || groups[0].Committed != nil || groups[1].Committed == nil {
		t.Fatalf("recovered turn groups=%#v", groups)
	}
}

func TestExecResumeJSONFinalOutputAndFailuresDoNotLeakChunks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		session    *fakeExecSession
		wantStatus execJSONStatus
		wantCode   string
	}{
		{name: "success", session: &fakeExecSession{id: "thread-success", chunks: []string{"final reply"}}, wantStatus: execStatusCompleted},
		{name: "stream failure", session: &fakeExecSession{id: "thread-failure", chunks: []string{"partial reply"}, err: errors.New("stream failed")}, wantStatus: execStatusFailed, wantCode: execErrorRun},
		{name: "cancelled", session: &fakeExecSession{id: "thread-cancelled", chunks: []string{"partial reply"}, err: context.Canceled}, wantStatus: execStatusCancelled, wantCode: execErrorCancelled},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			newCalls := 0
			openCalls := 0
			deps := execCommandDeps{
				newSession: func(context.Context, string) (execSession, io.Closer, error) {
					newCalls++
					return nil, nil, errors.New("new session must not be opened")
				},
				openSession: func(_ context.Context, _ string, id string, recoverInterrupted bool) (execSession, io.Closer, error) {
					if id != tc.session.ID() || recoverInterrupted {
						t.Fatalf("open id=%q recover=%v", id, recoverInterrupted)
					}
					openCalls++
					return tc.session, nil, nil
				},
			}
			stdout, stderr, err := executeExecForTest(strings.NewReader(""), deps, "exec", "resume", tc.session.ID(), "--output-format", "json", "continue")
			if tc.wantStatus == execStatusCompleted {
				if err != nil {
					t.Fatalf("exec resume: %v", err)
				}
			} else if err == nil {
				t.Fatal("expected resumed run error")
			}
			envelope, _ := decodeExecJSON(t, stdout)
			if newCalls != 0 || openCalls != 1 || envelope.Status != tc.wantStatus || envelope.Session.ID == nil || *envelope.Session.ID != tc.session.ID() || !envelope.Session.Persistent {
				t.Fatalf("new=%d open=%d envelope=%+v", newCalls, openCalls, envelope)
			}
			if tc.wantStatus == execStatusCompleted {
				if envelope.Result == nil || *envelope.Result != "final reply" {
					t.Fatalf("success envelope=%+v", envelope)
				}
			} else if envelope.Error.Code != tc.wantCode || strings.Contains(stdout, "partial reply") {
				t.Fatalf("failure envelope=%+v stdout=%q", envelope, stdout)
			}
			if stderr != execSessionHint(tc.session.ID()) {
				t.Fatalf("stderr=%q", stderr)
			}
		})
	}
}

func TestExecJSONWritesOneFinalSuccessfulEnvelope(t *testing.T) {
	usage := chat.UsageSummary{
		PromptTokens:     21,
		CompletionTokens: 8,
		CachedTokens:     5,
		ReasoningTokens:  3,
		TotalTokens:      29,
		ModelCallCount:   2,
		Status:           store.UsageStatusExact,
	}
	reply := "first line\nquoted: \\\"<data>\\\"\n\x1b[31mnot terminal output\x1b[0m"
	session := &fakeExecSession{chunks: []string{reply[:15], reply[15:]}, id: "opaque-session-id", usage: &usage}
	called := false
	stdout, stderr, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(session, &called), "exec", "--output-format", "json", "say something")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !called || session.prompt != "say something" {
		t.Fatalf("factory=%v prompt=%q", called, session.prompt)
	}
	if strings.Contains(stdout, "\x1b") {
		t.Fatalf("JSON stdout contained an ANSI control sequence: %q", stdout)
	}
	envelope, raw := decodeExecJSON(t, stdout)
	if envelope.Status != execStatusCompleted || envelope.Result == nil || *envelope.Result != reply || envelope.Error != nil {
		t.Fatalf("success envelope = %+v, want result %q", envelope, reply)
	}
	if !envelope.Session.Persistent || envelope.Session.ID == nil || *envelope.Session.ID != session.ID() {
		t.Fatalf("session = %+v", envelope.Session)
	}
	if envelope.Usage == nil || envelope.Usage.PromptTokens != 21 || envelope.Usage.CompletionTokens != 8 || envelope.Usage.CachedTokens != 5 || envelope.Usage.ReasoningTokens != 3 || envelope.Usage.TotalTokens != 29 || envelope.Usage.ModelCallCount != 2 || envelope.Usage.Status != string(store.UsageStatusExact) {
		t.Fatalf("usage = %+v", envelope.Usage)
	}
	if _, ok := raw["tool"]; ok {
		t.Fatalf("JSON envelope leaked tool data: %s", stdout)
	}
	if stderr != execSessionHint(session.ID()) {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestExecStreamJSONNewAndResumeHaveOrderedTerminalRecords(t *testing.T) {
	tests := []struct {
		name string
		args []string
		deps func(session *fakeExecSession) execCommandDeps
	}{
		{
			name: "new",
			args: []string{"exec", "--output-format", "stream-json", "say hi"},
			deps: func(session *fakeExecSession) execCommandDeps {
				return recordingExecDeps(session, new(bool))
			},
		},
		{
			name: "resume",
			args: []string{"exec", "resume", "stream-resume", "--output-format", "stream-json", "continue"},
			deps: func(session *fakeExecSession) execCommandDeps {
				return execCommandDeps{openSession: func(_ context.Context, _ string, id string, recoverInterrupted bool) (execSession, io.Closer, error) {
					if id != session.ID() || recoverInterrupted {
						t.Fatalf("open id=%q recover=%v", id, recoverInterrupted)
					}
					return session, nil, nil
				}}
			},
		},
		{
			name: "resume recover",
			args: []string{"exec", "resume", "stream-recover", "--recover", "--output-format", "stream-json", "continue"},
			deps: func(session *fakeExecSession) execCommandDeps {
				return execCommandDeps{openSession: func(_ context.Context, _ string, id string, recoverInterrupted bool) (execSession, io.Closer, error) {
					if id != session.ID() || !recoverInterrupted {
						t.Fatalf("open id=%q recover=%v", id, recoverInterrupted)
					}
					return session, nil, nil
				}}
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeExecSession{id: "stream-" + tc.name, chunks: []string{"final reply"}}
			if tc.name == "resume" {
				session.id = "stream-resume"
			}
			if tc.name == "resume recover" {
				session.id = "stream-recover"
			}
			stdout, stderr, err := executeExecForTest(strings.NewReader(""), tc.deps(session), tc.args...)
			if err != nil {
				t.Fatalf("exec stream: %v", err)
			}
			records := decodeExecStream(t, stdout)
			if len(records) != 2 {
				t.Fatalf("record count=%d, want session.started plus result: %s", len(records), stdout)
			}
			var started struct {
				Session execJSONSession `json:"session"`
			}
			if err := json.Unmarshal(mustMarshal(t, records[0]), &started); err != nil {
				t.Fatalf("decode session.started: %v", err)
			}
			if eventName(t, records[0]) != execStreamEventSessionStarted || !started.Session.Persistent || started.Session.ID == nil || *started.Session.ID != session.ID() {
				t.Fatalf("session.started=%s", mustMarshal(t, records[0]))
			}
			result := decodeExecStreamResult(t, records[1])
			if result.Status != execStatusCompleted || result.Result == nil || *result.Result != "final reply" || result.Error != nil || !result.Session.Persistent || result.Session.ID == nil || started.Session.ID == nil || *result.Session.ID != *started.Session.ID {
				t.Fatalf("result=%+v started=%+v", result, started)
			}
			if stderr != execSessionHint(session.ID()) {
				t.Fatalf("stderr=%q", stderr)
			}
		})
	}
}

func TestExecStreamJSONProjectsOnlySanitizedToolActivity(t *testing.T) {
	const (
		toolName   = "tool-name-secret"
		callID     = "call-id-secret"
		toolInput  = "input-secret"
		toolOutput = "output-secret"
		toolError  = "error-secret"
		reasoning  = "reasoning-secret"
		chunk      = "chunk-secret"
	)
	session := &eventExecSession{
		fakeExecSession: fakeExecSession{chunks: []string{"final reply"}},
		events: []chat.TurnEvent{
			{Kind: chat.TurnEventToolStart, Tool: toolName, ToolCallID: callID, Input: toolInput},
			{Kind: chat.TurnEventToolEnd, Tool: toolName, ToolCallID: callID, Output: toolOutput},
			{Kind: chat.TurnEventToolError, Tool: toolName, ToolCallID: callID, Err: errors.New(toolError)},
			{Kind: chat.TurnEventReasoning, Chunk: reasoning},
			{Kind: chat.TurnEventChunk, Chunk: chunk},
			{Kind: chat.TurnEventModelUsage, ModelUsage: &chat.ModelUsageEvent{}},
		},
	}
	stdout, _, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(session, new(bool)), "exec", "--output-format", "stream-json", "inspect")
	if err != nil {
		t.Fatalf("exec stream: %v", err)
	}
	for _, secret := range []string{toolName, callID, toolInput, toolOutput, toolError, reasoning, chunk} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("stream leaked %q: %s", secret, stdout)
		}
	}
	records := decodeExecStream(t, stdout)
	if len(records) != 5 || eventName(t, records[0]) != execStreamEventSessionStarted || eventName(t, records[len(records)-1]) != execStreamEventResult {
		t.Fatalf("unexpected records: %s", stdout)
	}
	states := []string{"started", "completed", "failed"}
	for index, state := range states {
		var activity execStreamActivity
		if err := json.Unmarshal(records[index+1]["activity"], &activity); err != nil {
			t.Fatalf("decode activity %d: %v", index, err)
		}
		if eventName(t, records[index+1]) != execStreamEventActivity || activity.Kind != "tool" || activity.State != state || len(records[index+1]) != 4 {
			t.Fatalf("activity %d = %s", index, mustMarshal(t, records[index+1]))
		}
	}
}

func TestExecStreamJSONSerializesConcurrentActivities(t *testing.T) {
	events := make([]chat.TurnEvent, 0, 24)
	for index := 0; index < cap(events); index++ {
		events = append(events, chat.TurnEvent{Kind: chat.TurnEventToolStart, Tool: "private", ToolCallID: fmt.Sprintf("call-%d", index), Input: "private"})
	}
	session := &eventExecSession{
		fakeExecSession: fakeExecSession{chunks: []string{"done"}},
		events:          events,
		concurrent:      true,
	}
	stdout, _, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(session, new(bool)), "exec", "--output-format", "stream-json", "run")
	if err != nil {
		t.Fatalf("exec stream: %v", err)
	}
	records := decodeExecStream(t, stdout)
	if len(records) != len(events)+2 {
		t.Fatalf("record count=%d, want %d", len(records), len(events)+2)
	}
	terminalCount := 0
	for _, record := range records {
		switch eventName(t, record) {
		case execStreamEventActivity:
			var activity execStreamActivity
			if err := json.Unmarshal(record["activity"], &activity); err != nil || activity != (execStreamActivity{Kind: "tool", State: "started"}) {
				t.Fatalf("activity=%s err=%v", mustMarshal(t, record), err)
			}
		case execStreamEventResult:
			terminalCount++
		}
	}
	if terminalCount != 1 || eventName(t, records[len(records)-1]) != execStreamEventResult {
		t.Fatalf("terminal count=%d records=%s", terminalCount, stdout)
	}
}

func TestExecStreamJSONFailureCancellationAndStartupHaveOneTerminal(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		deps       execCommandDeps
		wantStatus execJSONStatus
		wantCode   string
		wantStart  bool
	}{
		{
			name:       "input",
			args:       []string{"exec", "--output-format", "stream-json", "  "},
			deps:       recordingExecDeps(&fakeExecSession{}, new(bool)),
			wantStatus: execStatusFailed,
			wantCode:   execErrorInput,
		},
		{
			name: "startup",
			args: []string{"exec", "--output-format", "stream-json", "run"},
			deps: execCommandDeps{newSession: func(context.Context, string) (execSession, io.Closer, error) {
				return nil, nil, errors.New("startup-secret")
			}},
			wantStatus: execStatusFailed,
			wantCode:   execErrorStartup,
		},
		{
			name:       "run failure",
			args:       []string{"exec", "--output-format", "stream-json", "run"},
			deps:       recordingExecDeps(&fakeExecSession{chunks: []string{"partial-secret"}, err: errors.New("run-secret")}, new(bool)),
			wantStatus: execStatusFailed,
			wantCode:   execErrorRun,
			wantStart:  true,
		},
		{
			name:       "cancelled",
			args:       []string{"exec", "--output-format", "stream-json", "run"},
			deps:       recordingExecDeps(&fakeExecSession{chunks: []string{"partial-secret"}, err: context.Canceled}, new(bool)),
			wantStatus: execStatusCancelled,
			wantCode:   execErrorCancelled,
			wantStart:  true,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := executeExecForTest(strings.NewReader(""), tc.deps, tc.args...)
			if tc.wantStatus == execStatusCompleted && err != nil {
				t.Fatalf("exec stream: %v", err)
			}
			if tc.wantStatus != execStatusCompleted && err == nil {
				t.Fatal("expected run error")
			}
			if strings.Contains(stdout, "secret") {
				t.Fatalf("stream leaked runtime detail: %s", stdout)
			}
			records := decodeExecStream(t, stdout)
			if len(records) != 1+boolToInt(tc.wantStart) || eventName(t, records[len(records)-1]) != execStreamEventResult {
				t.Fatalf("records=%s", stdout)
			}
			if tc.wantStart && eventName(t, records[0]) != execStreamEventSessionStarted {
				t.Fatalf("first record=%s", mustMarshal(t, records[0]))
			}
			result := decodeExecStreamResult(t, records[len(records)-1])
			if result.Status != tc.wantStatus || result.Error == nil || result.Error.Code != tc.wantCode || result.Result != nil {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestExecStreamJSONResultIsWrittenOnlyAfterDurableCommit(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	session, err := chat.NewSession(&localExecModel{stream: &localExecStream{messages: []*schema.Message{schema.AssistantMessage("committed", nil)}}}, "system", chat.SessionOptions{Store: threadStore})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	writer := &commitCheckingExecWriter{check: func() error {
		groups, loadErr := threadStore.LoadTurnGroups(context.Background(), session.ID())
		if loadErr != nil {
			return loadErr
		}
		if len(groups) != 1 || groups[0].Committed == nil {
			return fmt.Errorf("result arrived before durable commit: %#v", groups)
		}
		return nil
	}}
	var stderr bytes.Buffer
	err = executeExecWithWritersForTest(context.Background(), strings.NewReader(""), recordingExecDeps(session, new(bool)), writer, &stderr, "exec", "--output-format", "stream-json", "run")
	if err != nil || writer.checkErr != nil {
		t.Fatalf("exec=%v commit check=%v", err, writer.checkErr)
	}
	records := decodeExecStream(t, writer.String())
	if eventName(t, records[len(records)-1]) != execStreamEventResult {
		t.Fatalf("last record=%s", mustMarshal(t, records[len(records)-1]))
	}
}

func TestExecStreamJSONBrokenAndShortWritersCancelWithoutRetry(t *testing.T) {
	t.Run("broken after active turn starts", func(t *testing.T) {
		wantErr := errors.New("broken stream stdout")
		session := &blockingEventExecSession{cancelled: make(chan struct{})}
		writer := &failingAfterExecWriter{err: wantErr, failAt: 2}
		var stderr bytes.Buffer
		err := executeExecWithWritersForTest(context.Background(), strings.NewReader(""), recordingExecDeps(session, new(bool)), writer, &stderr, "exec", "--output-format", "stream-json", "run")
		if !errors.Is(err, wantErr) || writer.writes != 2 {
			t.Fatalf("error=%v writes=%d", err, writer.writes)
		}
		select {
		case <-session.cancelled:
		case <-time.After(time.Second):
			t.Fatal("broken stdout did not cancel active turn")
		}
		if strings.Contains(writer.String(), `"event":"result"`) {
			t.Fatalf("broken stream retried a terminal record: %s", writer.String())
		}
	})

	t.Run("short first record", func(t *testing.T) {
		writer := &shortExecWriter{}
		session := &fakeExecSession{}
		called := false
		err := executeExecWithWritersForTest(context.Background(), strings.NewReader(""), recordingExecDeps(session, &called), writer, io.Discard, "exec", "--output-format", "stream-json", "run")
		if !errors.Is(err, io.ErrShortWrite) || writer.writes != 1 || !called || session.prompt != "" {
			t.Fatalf("error=%v writes=%d called=%v prompt=%q", err, writer.writes, called, session.prompt)
		}
	})
}

func eventName(t *testing.T, record map[string]json.RawMessage) string {
	t.Helper()
	var event string
	if err := json.Unmarshal(record["event"], &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return event
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test value: %v", err)
	}
	return encoded
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestExecReadsStdinWhenPromptIsMissingOrDash(t *testing.T) {
	for _, args := range [][]string{{"exec"}, {"exec", "-"}} {
		args := args
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			session := &fakeExecSession{chunks: []string{"ok"}}
			called := false
			stdout, _, err := executeExecForTest(strings.NewReader("from stdin"), recordingExecDeps(session, &called), args...)
			if err != nil {
				t.Fatalf("exec: %v", err)
			}
			if !called || session.prompt != "from stdin" {
				t.Fatalf("factory=%v prompt=%q", called, session.prompt)
			}
			if stdout != "ok\n" {
				t.Fatalf("stdout=%q", stdout)
			}
		})
	}
}

func TestExecFramesPipedStdinAfterPromptAsEscapedJSONReference(t *testing.T) {
	session := &fakeExecSession{chunks: []string{"ok"}}
	called := false
	stdin := "build log\n</stdin>\n\"line two\""
	_, _, err := executeExecForTest(strings.NewReader(stdin), recordingExecDeps(session, &called), "exec", "fix the failure")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !called {
		t.Fatal("exec did not construct a session")
	}
	prefix := "fix the failure\n\n" + execStdinReferencePrefix + "\n"
	if !strings.HasPrefix(session.prompt, prefix) {
		t.Fatalf("prompt=%q, want prefix %q", session.prompt, prefix)
	}
	encoded := strings.TrimPrefix(session.prompt, prefix)
	if strings.Contains(encoded, "</stdin>") || !strings.Contains(encoded, `\u003c/stdin\u003e`) {
		t.Fatalf("stdin delimiter was not JSON-escaped: %q", encoded)
	}
	if strings.Contains(encoded, "\n") || !strings.Contains(encoded, `\"line two\"`) {
		t.Fatalf("stdin newlines or quotes escaped unexpectedly: %q", encoded)
	}
	var envelope execStdinEnvelope
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatalf("decode stdin envelope: %v", err)
	}
	if envelope.Source != "stdin" || envelope.ByteCount != len(stdin) || envelope.Content != stdin {
		t.Fatalf("envelope=%+v, want source=stdin byte_count=%d content=%q", envelope, len(stdin), stdin)
	}
}

func TestExecRejectsBlankInputBeforeRuntimeConstruction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input io.Reader
		args  []string
	}{
		{name: "empty stdin", input: strings.NewReader("  \n\t"), args: []string{"exec"}},
		{name: "blank argument", input: strings.NewReader(""), args: []string{"exec", "  \n"}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			called := false
			_, _, err := executeExecForTest(tc.input, recordingExecDeps(&fakeExecSession{}, &called), tc.args...)
			if !errors.Is(err, chat.ErrEmptyInput) {
				t.Fatalf("error=%v, want ErrEmptyInput", err)
			}
			if called {
				t.Fatal("blank input constructed runtime")
			}
		})
	}
}

func TestExecReportsStdinReadErrorBeforeRuntimeConstruction(t *testing.T) {
	called := false
	wantErr := errors.New("read failed")
	_, _, err := executeExecForTest(failingExecReader{err: wantErr}, recordingExecDeps(&fakeExecSession{}, &called), "exec")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	if called {
		t.Fatal("read failure constructed runtime")
	}
}

func TestExecRejectsStdinOverLimitBeforeRuntimeConstruction(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "stdin only", args: []string{"exec"}},
		{name: "explicit prompt with stdin", args: []string{"exec", "summarize this"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := io.MultiReader(
				strings.NewReader(strings.Repeat("x", maxExecStdinBytes)),
				strings.NewReader("x"),
			)
			called := false
			_, _, err := executeExecForTest(input, recordingExecDeps(&fakeExecSession{}, &called), tc.args...)
			if !errors.Is(err, errExecStdinTooLarge) || err.Error() != "exec stdin exceeds the 10 MiB limit" {
				t.Fatalf("error=%v, want deterministic 10 MiB limit error", err)
			}
			if called {
				t.Fatal("oversized stdin constructed runtime")
			}
		})
	}
}

func TestExecJSONReportsInputAndStartupFailuresBeforeAUsableSession(t *testing.T) {
	readErr := errors.New("read failed")
	for _, tc := range []struct {
		name    string
		input   io.Reader
		args    []string
		wantErr string
	}{
		{name: "blank stdin", input: strings.NewReader("  \n\t"), args: []string{"exec", "--output-format", "json"}, wantErr: chat.ErrEmptyInput.Error()},
		{name: "stdin reader", input: failingExecReader{err: readErr}, args: []string{"exec", "--output-format", "json"}, wantErr: readErr.Error()},
		{name: "multiple prompts", input: strings.NewReader(""), args: []string{"exec", "--output-format", "json", "first", "second"}, wantErr: "exec accepts at most one prompt argument"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			called := false
			stdout, stderr, err := executeExecForTest(tc.input, recordingExecDeps(&fakeExecSession{}, &called), tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error=%v, want %q", err, tc.wantErr)
			}
			if called {
				t.Fatal("input failure constructed a session")
			}
			envelope, _ := decodeExecJSON(t, stdout)
			if envelope.Status != execStatusFailed || envelope.Error.Code != execErrorInput || envelope.Error.Message != execErrorMessageInput || envelope.Session.Persistent || envelope.Session.ID != nil {
				t.Fatalf("input failure envelope = %+v", envelope)
			}
			if stderr != "" {
				t.Fatalf("stderr=%q, want no session hint", stderr)
			}
		})
	}

	wantErr := errors.New("open session failed")
	deps := execCommandDeps{newSession: func(context.Context, string) (execSession, io.Closer, error) {
		return nil, nil, wantErr
	}}
	stdout, stderr, err := executeExecForTest(strings.NewReader(""), deps, "exec", "--output-format", "json", "say hi")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	envelope, _ := decodeExecJSON(t, stdout)
	if envelope.Status != execStatusFailed || envelope.Error.Code != execErrorStartup || envelope.Error.Message != execErrorMessageStartup || envelope.Session.Persistent || envelope.Session.ID != nil {
		t.Fatalf("startup failure envelope = %+v", envelope)
	}
	if stderr != "" {
		t.Fatalf("stderr=%q, want no session hint", stderr)
	}
}

func TestExecRejectsInvalidOutputFormatBeforeMachineOutput(t *testing.T) {
	for _, args := range [][]string{
		{"exec", "--output-format", "not-a-format", "say hi"},
		{"exec", "resume", "--output-format", "not-a-format"},
	} {
		called := false
		stdout, stderr, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(&fakeExecSession{}, &called), args...)
		if err == nil || !strings.Contains(err.Error(), "invalid output format \"not-a-format\"") {
			t.Fatalf("args=%q error=%v", args, err)
		}
		if stdout != "" || stderr != "" || called {
			t.Fatalf("args=%q stdout=%q stderr=%q factory=%v, want standard pre-run failure", args, stdout, stderr, called)
		}
	}
}

func TestExecEmitsOnlyCommittedFinalReplyAndSessionHint(t *testing.T) {
	session := &fakeExecSession{chunks: []string{"final ", "reply"}}
	called := false
	stdout, stderr, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(session, &called), "exec", "say hi")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if stdout != "final reply\n" {
		t.Fatalf("stdout=%q", stdout)
	}
	if stderr != execSessionHint(session.ID()) {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestExecJSONStreamFailureDoesNotLeakPartialReplyAndKeepsSession(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &localExecModel{stream: &localExecStream{
		messages: []*schema.Message{schema.AssistantMessage("partial reply", nil)},
		err:      errors.New("stream disconnected"),
	}}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: threadStore})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	before, err := threadStore.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread before exec: %v", err)
	}
	called := false
	stdout, stderr, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(session, &called), "exec", "--output-format", "json", "fail")
	if err == nil || !strings.Contains(err.Error(), "stream disconnected") {
		t.Fatalf("error=%v", err)
	}
	envelope, raw := decodeExecJSON(t, stdout)
	if !called || envelope.Status != execStatusFailed || envelope.Error.Code != execErrorRun || envelope.Result != nil {
		t.Fatalf("factory=%v envelope=%+v", called, envelope)
	}
	if envelope.Session.ID == nil || *envelope.Session.ID != session.ID() || !envelope.Session.Persistent {
		t.Fatalf("session=%+v", envelope.Session)
	}
	if strings.Contains(stdout, "partial reply") {
		t.Fatalf("JSON stdout leaked a partial model reply: %q", stdout)
	}
	if _, ok := raw["usage"]; ok {
		t.Fatalf("direct injected session unexpectedly supplied optional usage: %s", stdout)
	}
	if stderr != execSessionHint(session.ID()) {
		t.Fatalf("stderr=%q", stderr)
	}
	state, err := threadStore.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.MessageCount != before.Meta.MessageCount {
		t.Fatalf("failed stream changed committed messages from %d to %d", before.Meta.MessageCount, state.Meta.MessageCount)
	}
}

func TestExecJSONUsesStablePublicFailureDetails(t *testing.T) {
	const sensitiveDiagnostic = "provider diagnostic: api_key=should-not-reach-json"
	session := &fakeExecSession{err: errors.New(sensitiveDiagnostic)}
	called := false

	stdout, stderr, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(session, &called), "exec", "--output-format", "json", "fail")
	if !called || err == nil || !strings.Contains(err.Error(), sensitiveDiagnostic) {
		t.Fatalf("factory=%v error=%v, want returned diagnostic", called, err)
	}
	if strings.Contains(stdout, sensitiveDiagnostic) {
		t.Fatalf("JSON stdout leaked sensitive diagnostic: %q", stdout)
	}
	envelope, _ := decodeExecJSON(t, stdout)
	if envelope.Status != execStatusFailed || envelope.Error.Code != execErrorRun || envelope.Error.Message != execErrorMessageRun {
		t.Fatalf("runtime failure envelope = %+v", envelope)
	}
	if stderr != execSessionHint(session.ID()) {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestExecJSONCancellationHasDurableCancelledSession(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &localExecModel{stream: &localExecStream{err: context.Canceled}}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: threadStore})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	called := false
	stdout, stderr, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(session, &called), "exec", "--output-format", "json", "cancel")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context cancellation", err)
	}
	envelope, _ := decodeExecJSON(t, stdout)
	if !called || envelope.Status != execStatusCancelled || envelope.Error.Code != execErrorCancelled || envelope.Error.Message != execErrorMessageCancelled || envelope.Result != nil {
		t.Fatalf("factory=%v envelope=%+v", called, envelope)
	}
	if envelope.Session.ID == nil || *envelope.Session.ID != session.ID() || !envelope.Session.Persistent {
		t.Fatalf("session=%+v", envelope.Session)
	}
	if stderr != execSessionHint(session.ID()) {
		t.Fatalf("stderr=%q", stderr)
	}
	groups, err := threadStore.LoadTurnGroups(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Cancelled == nil || groups[0].Committed != nil {
		t.Fatalf("durable cancellation groups=%#v", groups)
	}
}

func TestExecJSONGuardedDeadlineCancelsDurableTurn(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &blockingExecModel{stream: &blockingExecStream{closed: make(chan struct{})}}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: threadStore})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	before, err := threadStore.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread before exec: %v", err)
	}
	guarded := &guardedExecSession{
		session: session,
		options: runtimeguard.TurnOptions{
			Timeout: 25 * time.Millisecond,
		},
	}
	called := false

	stdout, stderr, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(guarded, &called), "exec", "--output-format", "json", "timeout")
	if !called || !errors.Is(err, runtimeguard.ErrTurnDeadlineExceeded) {
		t.Fatalf("factory=%v error=%v, want runtime guard deadline", called, err)
	}
	envelope, _ := decodeExecJSON(t, stdout)
	if envelope.Status != execStatusCancelled || envelope.Error.Code != execErrorCancelled || envelope.Error.Message != execErrorMessageCancelled || envelope.Result != nil {
		t.Fatalf("deadline envelope = %+v", envelope)
	}
	if stderr != execSessionHint(session.ID()) {
		t.Fatalf("stderr=%q", stderr)
	}
	state, err := threadStore.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread after exec: %v", err)
	}
	if state.Meta.MessageCount != before.Meta.MessageCount {
		t.Fatalf("deadline committed messages from %d to %d", before.Meta.MessageCount, state.Meta.MessageCount)
	}
	groups, err := threadStore.LoadTurnGroups(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Cancelled == nil || groups[0].Committed != nil {
		t.Fatalf("deadline must persist turn.cancelled without a committed final: %#v", groups)
	}
}

func TestExecJSONClassifiesCancellationBeforeSessionCreation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, runtimeguard.ErrTurnDeadlineExceeded} {
		cause := cause
		t.Run(cause.Error(), func(t *testing.T) {
			deps := execCommandDeps{newSession: func(context.Context, string) (execSession, io.Closer, error) {
				return nil, nil, cause
			}}

			stdout, stderr, err := executeExecForTest(strings.NewReader(""), deps, "exec", "--output-format", "json", "say hi")
			if !errors.Is(err, cause) {
				t.Fatalf("error=%v, want %v", err, cause)
			}
			envelope, _ := decodeExecJSON(t, stdout)
			if envelope.Status != execStatusCancelled || envelope.Error.Code != execErrorCancelled || envelope.Error.Message != execErrorMessageCancelled || envelope.Session.Persistent || envelope.Session.ID != nil {
				t.Fatalf("startup cancellation envelope = %+v", envelope)
			}
			if stderr != "" {
				t.Fatalf("stderr=%q, want no session hint", stderr)
			}
		})
	}
}

func TestExecJSONStdoutWriterFailureDoesNotRetry(t *testing.T) {
	wantErr := errors.New("broken stdout")
	writer := &failingExecWriter{err: wantErr}
	var stderr bytes.Buffer
	session := &fakeExecSession{chunks: []string{"done"}}
	called := false
	err := executeExecWithWritersForTest(context.Background(), strings.NewReader(""), recordingExecDeps(session, &called), writer, &stderr, "exec", "--output-format", "json", "say hi")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	if !called || writer.writes != 1 {
		t.Fatalf("factory=%v JSON writes=%d, want one attempted final record", called, writer.writes)
	}
	if stderr.String() != execSessionHint(session.ID()) {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestExecStreamFailureDoesNotLeakPartialReplyOrCommitLedger(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &localExecModel{stream: &localExecStream{
		messages: []*schema.Message{schema.AssistantMessage("partial", nil)},
		err:      errors.New("stream disconnected"),
	}}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: threadStore})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	before, err := threadStore.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread before exec: %v", err)
	}
	called := false
	stdout, stderr, err := executeExecForTest(strings.NewReader(""), recordingExecDeps(session, &called), "exec", "fail")
	if err == nil || !strings.Contains(err.Error(), "stream disconnected") {
		t.Fatalf("error=%v", err)
	}
	if !called || stdout != "" || stderr != execSessionHint(session.ID()) {
		t.Fatalf("factory=%v stdout=%q stderr=%q", called, stdout, stderr)
	}
	state, err := threadStore.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.MessageCount != before.Meta.MessageCount {
		t.Fatalf("failed stream changed committed messages from %d to %d", before.Meta.MessageCount, state.Meta.MessageCount)
	}
}

func TestExecDoesNotEmitSessionHintWhenSessionCreationFails(t *testing.T) {
	wantErr := errors.New("open session failed")
	deps := execCommandDeps{newSession: func(context.Context, string) (execSession, io.Closer, error) {
		return nil, nil, wantErr
	}}
	stdout, stderr, err := executeExecForTest(strings.NewReader(""), deps, "exec", "say hi")
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want %v", err, wantErr)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want no command output", stdout, stderr)
	}
}

func TestExecOnRequestPermissionFailsClosedWithoutApprover(t *testing.T) {
	// Headless exec passes no approver into the shared production tool wiring.
	// The same tool boundary must soft-deny an ask decision without executing it.
	shell, err := tools.NewShell(tools.ShellOptions{Approval: tools.ApprovalOnRequest})
	if err != nil {
		t.Fatalf("NewShell: %v", err)
	}
	raw, err := shell.InvokableRun(context.Background(), `{"command":"echo needs approval"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var output tools.ShellOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if !output.Denied || output.Reason != tools.ReasonApproverMissing {
		t.Fatalf("expected fail-closed approval result, got %+v", output)
	}
}

func TestGuardedExecSessionAppliesTurnBudget(t *testing.T) {
	inner := &observingExecSession{}
	guarded := &guardedExecSession{
		session: inner,
		options: runtimeguard.TurnOptions{
			MaxToolCalls: 3,
		},
	}
	if err := guarded.Ask(context.Background(), "prompt", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if inner.maxToolCalls != 3 {
		t.Fatalf("max tool calls = %d, want 3", inner.maxToolCalls)
	}
}

type observingExecSession struct{ maxToolCalls int }

func (s *observingExecSession) ID() string { return "observing-exec-session" }

func (s *observingExecSession) Ask(ctx context.Context, _ string, _ func(string) error) error {
	budget, ok := runtimeguard.FromContext(ctx)
	if !ok {
		return errors.New("missing turn budget")
	}
	s.maxToolCalls = budget.MaxToolCalls()
	return nil
}

type localExecModel struct{ stream chat.Stream }

func (m *localExecModel) Stream(context.Context, []*schema.Message) (chat.Stream, error) {
	return m.stream, nil
}

type localExecStream struct {
	messages []*schema.Message
	index    int
	err      error
}

func (s *localExecStream) Recv() (*schema.Message, error) {
	if s.index < len(s.messages) {
		message := s.messages[s.index]
		s.index++
		return message, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	return nil, io.EOF
}

func (s *localExecStream) Close() {}

type blockingExecModel struct{ stream chat.Stream }

func (m *blockingExecModel) Stream(context.Context, []*schema.Message) (chat.Stream, error) {
	return m.stream, nil
}

type blockingExecStream struct{ closed chan struct{} }

func (s *blockingExecStream) Recv() (*schema.Message, error) {
	<-s.closed
	return nil, context.DeadlineExceeded
}

func (s *blockingExecStream) Close() { close(s.closed) }
