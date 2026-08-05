package chat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestSessionSteerRequiresActiveExpectedTurnAndOptInModel(t *testing.T) {
	unsupported := newSteerTestModel(false)
	session, err := NewSession(unsupported, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, ok := session.ActiveTurnID(); ok {
		t.Fatal("new session unexpectedly reports an active turn")
	}
	if err := session.Steer(context.Background(), "missing", "input"); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("Steer without active turn = %v, want ErrNoActiveTurn", err)
	}

	askDone := make(chan error, 1)
	go func() { askDone <- session.Ask(context.Background(), "question", nil) }()
	waitForSteerModelStart(t, unsupported)
	if _, ok := session.ActiveTurnID(); ok {
		t.Fatal("unsupported model reports a steerable active turn")
	}
	session.mu.RLock()
	turnID := session.activeSteerTurn.id
	session.mu.RUnlock()
	if err := session.Steer(context.Background(), "wrong-turn", "input"); !errors.Is(err, ErrSteerTurnMismatch) {
		t.Fatalf("Steer with wrong ID = %v, want ErrSteerTurnMismatch", err)
	}
	if err := session.Steer(context.Background(), turnID, "input"); !errors.Is(err, ErrSteerUnsupported) {
		t.Fatalf("Steer on non-opt-in model = %v, want ErrSteerUnsupported", err)
	}
	close(unsupported.release)
	if err := <-askDone; err != nil {
		t.Fatalf("Ask: %v", err)
	}
}

func TestSessionSteerSafePointPersistsExactlyOnce(t *testing.T) {
	model := newSteerTestModel(true)
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	askDone := make(chan error, 1)
	go func() { askDone <- session.Ask(context.Background(), "original", nil) }()
	waitForSteerModelStart(t, model)
	turnID, ok := session.ActiveTurnID()
	if !ok {
		t.Fatal("Ask did not publish a durable active turn ID")
	}
	if err := session.Steer(context.Background(), turnID, "change direction"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	close(model.release)
	if err := <-askDone; err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, ok := session.ActiveTurnID(); ok {
		t.Fatal("completed Ask left an active turn ID")
	}

	model.mu.Lock()
	requests := cloneTestMessages(model.requests[0])
	sequences := append([]uint64(nil), model.consumedSequences...)
	model.mu.Unlock()
	assertMessages(t, requests, []messageExpectation{
		{role: schema.System, content: "system"},
		{role: schema.User, content: "original"},
		{role: schema.User, content: "change direction"},
	})
	if len(sequences) != 1 || sequences[0] != 1 {
		t.Fatalf("consumed sequences = %v, want exactly [1]", sequences)
	}
	transcript := session.Transcript()
	count := 0
	for _, message := range transcript {
		if message != nil && message.Role == schema.User && message.Content == "change direction" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("committed steer count = %d, want exactly 1; transcript=%#v", count, transcript)
	}
}

func TestSessionSteerWithReceiptReturnsMailboxSequenceAndContent(t *testing.T) {
	model := newSteerTestModel(true)
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	askDone := make(chan error, 1)
	go func() { askDone <- session.Ask(context.Background(), "original", nil) }()
	waitForSteerModelStart(t, model)
	turnID, ok := session.ActiveTurnID()
	if !ok {
		t.Fatal("missing active turn ID")
	}
	first, err := session.SteerWithReceipt(context.Background(), turnID, "first steer")
	if err != nil {
		t.Fatalf("first SteerWithReceipt: %v", err)
	}
	second, err := session.SteerWithReceipt(context.Background(), turnID, "second steer")
	if err != nil {
		t.Fatalf("second SteerWithReceipt: %v", err)
	}
	if first.Sequence != 1 || first.Content != "first steer" {
		t.Fatalf("first receipt = %#v, want sequence 1 and original content", first)
	}
	if second.Sequence != 2 || second.Content != "second steer" {
		t.Fatalf("second receipt = %#v, want sequence 2 and original content", second)
	}
	close(model.release)
	if err := <-askDone; err != nil {
		t.Fatalf("Ask: %v", err)
	}
}

func TestSessionSteerConsumedEventWaitsForModelBoundary(t *testing.T) {
	model := newSteerTestModel(true)
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	events := make(chan TurnEvent, 4)
	askDone := make(chan error, 1)
	go func() {
		askDone <- session.AskWithEvents(context.Background(), "original", nil, func(event TurnEvent) {
			events <- event
		})
	}()
	waitForSteerModelStart(t, model)
	turnID, ok := session.ActiveTurnID()
	if !ok {
		t.Fatal("missing active turn ID")
	}
	if err := session.Steer(context.Background(), turnID, "change direction"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	select {
	case event := <-events:
		t.Fatalf("steer was reported before the model boundary: %#v", event)
	default:
	}
	close(model.release)
	if err := <-askDone; err != nil {
		t.Fatalf("AskWithEvents: %v", err)
	}

	var consumed []TurnEvent
	for {
		select {
		case event := <-events:
			if event.Kind == TurnEventSteerConsumed {
				consumed = append(consumed, event)
			}
		default:
			if len(consumed) != 1 {
				t.Fatalf("consumed events = %#v, want one", consumed)
			}
			if consumed[0].SteerSequence != 1 || consumed[0].SteerContent != "change direction" {
				t.Fatalf("consumed event = %#v", consumed[0])
			}
			return
		}
	}
}

func TestTurnSteerMailboxObserverRunsOutsideMailboxMutex(t *testing.T) {
	mailbox := newTurnSteerMailbox()
	if err := mailbox.Enqueue(context.Background(), "first"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	observed := make(chan TurnSteerInput, 2)
	mailbox.setConsumedObserver(func(input TurnSteerInput) {
		if input.Sequence == 1 {
			if err := mailbox.Enqueue(context.Background(), "from observer"); err != nil {
				t.Errorf("observer Enqueue: %v", err)
			}
		}
		observed <- input
	})

	firstDone := make(chan []TurnSteerInput, 1)
	go func() { firstDone <- mailbox.TakeTurnSteers() }()
	select {
	case batch := <-firstDone:
		if len(batch) != 1 || batch[0].Sequence != 1 || batch[0].Content != "first" {
			t.Fatalf("first batch = %#v", batch)
		}
	case <-time.After(time.Second):
		t.Fatal("TakeTurnSteers deadlocked while observer tried to enqueue")
	}
	second := mailbox.TakeTurnSteers()
	if len(second) != 1 || second[0].Sequence != 2 || second[0].Content != "from observer" {
		t.Fatalf("second batch = %#v", second)
	}
	firstObserved := <-observed
	secondObserved := <-observed
	if firstObserved.Sequence != 1 || secondObserved.Sequence != 2 {
		t.Fatalf("observed inputs = %#v, %#v", firstObserved, secondObserved)
	}
}

func TestSessionSteerConcurrentAdmissionUsesOneOrderedMailbox(t *testing.T) {
	model := newSteerTestModel(true)
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	askDone := make(chan error, 1)
	go func() { askDone <- session.Ask(context.Background(), "original", nil) }()
	waitForSteerModelStart(t, model)
	turnID, ok := session.ActiveTurnID()
	if !ok {
		t.Fatal("missing active turn ID")
	}

	const count = 32
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- session.Steer(context.Background(), turnID, fmt.Sprintf("steer-%02d", i))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Steer: %v", err)
		}
	}
	close(model.release)
	if err := <-askDone; err != nil {
		t.Fatalf("Ask: %v", err)
	}

	model.mu.Lock()
	sequences := append([]uint64(nil), model.consumedSequences...)
	request := cloneTestMessages(model.requests[0])
	model.mu.Unlock()
	if len(sequences) != count {
		t.Fatalf("consumed sequence count = %d, want %d", len(sequences), count)
	}
	for i, sequence := range sequences {
		if sequence != uint64(i+1) {
			t.Fatalf("consumed sequences = %v, want each sequence exactly once", sequences)
		}
	}
	steerCount := 0
	for _, message := range request {
		if message != nil && message.Role == schema.User && len(message.Content) >= len("steer-") && message.Content[:len("steer-")] == "steer-" {
			steerCount++
		}
	}
	if steerCount != count {
		t.Fatalf("safe-point request steer count = %d, want %d", steerCount, count)
	}
}

func TestSessionSteerPendingInputIsDiscardedOnFailure(t *testing.T) {
	model := newSteerTestModel(true)
	model.consume = false
	model.streamErr = errors.New("model failed")
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	askDone := make(chan error, 1)
	go func() { askDone <- session.Ask(context.Background(), "original", nil) }()
	waitForSteerModelStart(t, model)
	turnID, ok := session.ActiveTurnID()
	if !ok {
		t.Fatal("missing active turn ID")
	}
	if err := session.Steer(context.Background(), turnID, "pending only"); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	close(model.release)
	if err := <-askDone; !errors.Is(err, model.streamErr) {
		t.Fatalf("Ask error = %v, want %v", err, model.streamErr)
	}
	if err := session.Steer(context.Background(), turnID, "late"); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("late Steer after failed turn = %v, want ErrNoActiveTurn", err)
	}
	for _, message := range session.Transcript() {
		if message != nil && message.Content == "pending only" {
			t.Fatalf("failed-turn pending steer leaked into transcript: %#v", message)
		}
	}
}

func TestSessionSteerCancellationClosesMailbox(t *testing.T) {
	model := newSteerTestModel(true)
	model.consume = false
	model.waitForContext = true
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	askDone := make(chan error, 1)
	go func() { askDone <- session.Ask(ctx, "original", nil) }()
	waitForSteerModelStart(t, model)
	turnID, ok := session.ActiveTurnID()
	if !ok {
		t.Fatal("missing active turn ID")
	}
	cancel()
	if err := session.Steer(context.Background(), turnID, "late cancellation input"); !errors.Is(err, ErrTurnNotSteerable) {
		t.Fatalf("Steer after cancellation request = %v, want ErrTurnNotSteerable", err)
	}
	if err := <-askDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Ask error = %v, want context.Canceled", err)
	}
	if _, ok := session.ActiveTurnID(); ok {
		t.Fatal("cancelled Ask left an active turn ID")
	}
}

func waitForSteerModelStart(t *testing.T, model *steerTestModel) {
	t.Helper()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for model call")
	}
}

type steerTestModel struct {
	mu sync.Mutex

	supported         bool
	mailboxes         map[string]TurnSteerMailbox
	requests          [][]*schema.Message
	consumedSequences []uint64

	started        chan struct{}
	release        chan struct{}
	startedOnce    sync.Once
	consume        bool
	waitForContext bool
	streamErr      error
}

func newSteerTestModel(supported bool) *steerTestModel {
	return &steerTestModel{
		supported: supported,
		mailboxes: make(map[string]TurnSteerMailbox),
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		consume:   supported,
	}
}

func (m *steerTestModel) RegisterTurnSteer(turnID string, mailbox TurnSteerMailbox) error {
	if !m.supported {
		return ErrSteerUnsupported
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mailboxes[turnID] = mailbox
	return nil
}

func (m *steerTestModel) UnregisterTurnSteer(turnID string) {
	m.mu.Lock()
	delete(m.mailboxes, turnID)
	m.mu.Unlock()
}

func (m *steerTestModel) Stream(ctx context.Context, messages []*schema.Message) (Stream, error) {
	m.startedOnce.Do(func() { close(m.started) })
	if m.waitForContext {
		<-ctx.Done()
	} else {
		select {
		case <-m.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	request := cloneTestMessages(messages)
	if m.consume {
		turnID, ok := TaskTurnIDFromContext(ctx)
		if !ok {
			return nil, errors.New("test model missing durable turn ID")
		}
		m.mu.Lock()
		mailbox := m.mailboxes[turnID]
		m.mu.Unlock()
		consumer, ok := mailbox.(TurnSteerConsumer)
		if !ok {
			return nil, errors.New("test model missing steer consumer")
		}
		for _, input := range consumer.TakeTurnSteers() {
			m.mu.Lock()
			m.consumedSequences = append(m.consumedSequences, input.Sequence)
			m.mu.Unlock()
			request = append(request, schema.UserMessage(input.Content))
		}
	}
	m.mu.Lock()
	m.requests = append(m.requests, request)
	streamErr := m.streamErr
	m.mu.Unlock()
	if streamErr != nil {
		return nil, streamErr
	}
	return &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("answer", nil)}}}, nil
}

func cloneTestMessages(messages []*schema.Message) []*schema.Message {
	result := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			result = append(result, nil)
			continue
		}
		copyMessage := *message
		result = append(result, &copyMessage)
	}
	return result
}

var _ TurnSteerModel = (*steerTestModel)(nil)
