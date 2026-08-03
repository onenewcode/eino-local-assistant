package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"eino-local-assistant/internal/chat"
)

const (
	execStreamVersion       = 1
	execStreamQueueCapacity = 32

	execStreamEventSessionStarted = "session.started"
	execStreamEventActivity       = "activity"
	execStreamEventResult         = "result"
)

var errExecStreamClosed = errors.New("exec stream output is closed")

// execStreamActivity is intentionally smaller than a chat TurnEvent. Tool
// names, call IDs, arguments, outputs, and errors belong in the durable ledger,
// not the public stdout protocol.
type execStreamActivity struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
}

type execStreamRecord struct {
	StreamVersion int                 `json:"stream_version"`
	Sequence      uint64              `json:"sequence"`
	Event         string              `json:"event"`
	Session       *execJSONSession    `json:"session,omitempty"`
	Activity      *execStreamActivity `json:"activity,omitempty"`
}

// execStreamResultRecord is deliberately a flat extension of the existing
// final JSON contract. Consumers get one stable terminal record without a
// second nested serialization format.
type execStreamResultRecord struct {
	StreamVersion   int             `json:"stream_version"`
	Sequence        uint64          `json:"sequence"`
	Event           string          `json:"event"`
	ContractVersion int             `json:"contract_version"`
	Status          execJSONStatus  `json:"status"`
	Result          *string         `json:"result"`
	Error           *execJSONError  `json:"error"`
	Session         execJSONSession `json:"session"`
	Usage           *execJSONUsage  `json:"usage,omitempty"`
}

type execStreamQueuedRecord struct {
	event    string
	session  *execJSONSession
	activity *execStreamActivity
	result   *execJSONEnvelope
	terminal bool
	ack      chan error
}

func (record execStreamQueuedRecord) marshal(sequence uint64) ([]byte, error) {
	if record.result != nil {
		return json.Marshal(execStreamResultRecord{
			StreamVersion:   execStreamVersion,
			Sequence:        sequence,
			Event:           record.event,
			ContractVersion: record.result.ContractVersion,
			Status:          record.result.Status,
			Result:          record.result.Result,
			Error:           record.result.Error,
			Session:         record.result.Session,
			Usage:           record.result.Usage,
		})
	}
	return json.Marshal(execStreamRecord{
		StreamVersion: execStreamVersion,
		Sequence:      sequence,
		Event:         record.event,
		Session:       record.session,
		Activity:      record.activity,
	})
}

// execStreamWriter is the sole stdout owner. Its bounded queue deliberately
// applies backpressure to event producers; a failed output path cancels the
// active durable turn instead of silently dropping observations.
type execStreamWriter struct {
	stdout    io.Writer
	cancelRun context.CancelFunc
	queue     chan execStreamQueuedRecord
	stopped   chan struct{}

	stateMu          sync.Mutex
	terminalEnqueued bool
	stopOnce         sync.Once
	errMu            sync.Mutex
	err              error
}

func newExecStreamWriter(stdout io.Writer, cancelRun context.CancelFunc) *execStreamWriter {
	writer := &execStreamWriter{
		stdout:    stdout,
		cancelRun: cancelRun,
		queue:     make(chan execStreamQueuedRecord, execStreamQueueCapacity),
		stopped:   make(chan struct{}),
	}
	go writer.run()
	return writer
}

func (writer *execStreamWriter) run() {
	var sequence uint64
	for record := range writer.queue {
		if sequence == ^uint64(0) {
			writer.finish(errors.New("exec stream sequence exhausted"))
			writer.ack(record, writer.outputErr())
			return
		}
		sequence++
		encoded, err := record.marshal(sequence)
		if err == nil {
			encoded = append(encoded, '\n')
			err = writeExecStreamLine(writer.stdout, encoded)
		}
		if err != nil {
			writer.finish(err)
			writer.ack(record, writer.outputErr())
			return
		}
		writer.ack(record, nil)
		if record.terminal {
			writer.finish(nil)
			return
		}
	}
}

func (writer *execStreamWriter) enqueue(ctx context.Context, record execStreamQueuedRecord) error {
	writer.stateMu.Lock()
	defer writer.stateMu.Unlock()
	if writer.terminalEnqueued && !record.terminal {
		return errExecStreamClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-writer.stopped:
		return writer.outputErr()
	case writer.queue <- record:
		return nil
	}
}

func (writer *execStreamWriter) enqueueAndWait(ctx context.Context, record execStreamQueuedRecord) error {
	record.ack = make(chan error, 1)
	if err := writer.enqueue(ctx, record); err != nil {
		return err
	}
	// The terminal write acknowledges immediately before the writer marks itself
	// stopped. Prefer that acknowledgement if both channels are ready.
	select {
	case err := <-record.ack:
		return err
	default:
	}
	select {
	case err := <-record.ack:
		return err
	case <-writer.stopped:
		select {
		case err := <-record.ack:
			return err
		default:
		}
		return writer.outputErr()
	}
}

func (writer *execStreamWriter) enqueueTerminal(result execJSONEnvelope) error {
	writer.stateMu.Lock()
	if writer.terminalEnqueued {
		writer.stateMu.Unlock()
		return errExecStreamClosed
	}
	writer.terminalEnqueued = true
	writer.stateMu.Unlock()
	return writer.enqueueAndWait(context.Background(), execStreamQueuedRecord{
		event:    execStreamEventResult,
		result:   &result,
		terminal: true,
	})
}

func (writer *execStreamWriter) finish(err error) {
	writer.stopOnce.Do(func() {
		if err != nil {
			writer.errMu.Lock()
			writer.err = err
			writer.errMu.Unlock()
			if writer.cancelRun != nil {
				writer.cancelRun()
			}
		}
		close(writer.stopped)
	})
}

func (writer *execStreamWriter) outputErr() error {
	writer.errMu.Lock()
	defer writer.errMu.Unlock()
	if writer.err != nil {
		return writer.err
	}
	return errExecStreamClosed
}

func (writer *execStreamWriter) ack(record execStreamQueuedRecord, err error) {
	if record.ack != nil {
		record.ack <- err
	}
}

func writeExecStreamLine(stdout io.Writer, encoded []byte) error {
	if stdout == nil {
		return errors.New("write exec stream: stdout is unavailable")
	}
	n, err := stdout.Write(encoded)
	if err != nil {
		return fmt.Errorf("write exec stream: %w", err)
	}
	if n != len(encoded) {
		return fmt.Errorf("write exec stream: %w", io.ErrShortWrite)
	}
	return nil
}

func writeExecStreamResult(stdout io.Writer, result execJSONEnvelope) error {
	writer := newExecStreamWriter(stdout, nil)
	return writer.enqueueTerminal(result)
}

func runExecStreamJSON(parent context.Context, prompt string, stdout, stderr io.Writer, session execSession, sessionInfo execJSONSession) error {
	ctx, cancelRun := context.WithCancel(parent)
	defer cancelRun()
	writer := newExecStreamWriter(stdout, cancelRun)

	// Confirm the first public record reached stdout before starting a durable
	// turn, so a broken pipe cannot create an unobserved active turn.
	if err := writer.enqueueAndWait(context.Background(), execStreamQueuedRecord{
		event:   execStreamEventSessionStarted,
		session: &sessionInfo,
	}); err != nil {
		return err
	}
	if err := writeExecSessionHint(stderr, *sessionInfo.ID); err != nil {
		return finishExecStreamRun(writer, session, sessionInfo, "", err)
	}

	var response string
	onChunk := func(chunk string) error {
		response += chunk
		return nil
	}
	emit := func(event chat.TurnEvent) {
		activity, ok := execStreamActivityForTurnEvent(event)
		if !ok {
			return
		}
		// Output failure cancels ctx; after that no raw event is retained or
		// written, and the durable session closes its active turn as cancelled.
		_ = writer.enqueue(ctx, execStreamQueuedRecord{
			event:    execStreamEventActivity,
			activity: activity,
		})
	}

	var err error
	if source, ok := session.(execSessionEventSource); ok {
		err = source.AskWithEvents(ctx, prompt, onChunk, emit)
	} else {
		err = session.Ask(ctx, prompt, onChunk)
	}
	return finishExecStreamRun(writer, session, sessionInfo, response, err)
}

func finishExecStreamRun(writer *execStreamWriter, session execSession, sessionInfo execJSONSession, response string, cause error) error {
	result := execJSONEnvelope{
		ContractVersion: execJSONContractVersion,
		Session:         sessionInfo,
		Usage:           execUsageForSession(session),
	}
	if cause == nil {
		result.Status = execStatusCompleted
		result.Result = &response
	} else {
		status, code := execFailureStatus(cause)
		result.Status = status
		result.Error = &execJSONError{Code: code, Message: execPublicErrorMessage(code)}
	}
	if err := writer.enqueueTerminal(result); err != nil {
		return err
	}
	return cause
}

func execStreamActivityForTurnEvent(event chat.TurnEvent) (*execStreamActivity, bool) {
	activity := &execStreamActivity{Kind: "tool"}
	switch event.Kind {
	case chat.TurnEventToolStart:
		activity.State = "started"
	case chat.TurnEventToolEnd:
		activity.State = "completed"
	case chat.TurnEventToolError:
		activity.State = "failed"
	default:
		return nil, false
	}
	return activity, true
}
