package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type resetFenceThreadRepository struct {
	store.ThreadRepository
	meta        store.ThreadMeta
	messages    []*schema.Message
	listStarted chan struct{}
	listRelease chan struct{}
}

func (r *resetFenceThreadRepository) ListThreads(ctx context.Context) ([]store.ThreadMeta, error) {
	if r.listStarted != nil {
		close(r.listStarted)
	}
	if r.listRelease != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.listRelease:
		}
	}
	return []store.ThreadMeta{r.meta}, nil
}

func (r *resetFenceThreadRepository) LoadRecentMessages(context.Context, string, int) ([]*schema.Message, error) {
	return append([]*schema.Message(nil), r.messages...), nil
}

type resetFenceModel struct {
	started  chan struct{}
	release  chan struct{}
	response *schema.Message
	err      error
}

func (m *resetFenceModel) Generate(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	close(m.started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.release:
		return m.response, m.err
	}
}

func (m *resetFenceModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.response}), m.err
}

func TestConsolidatorResetFenceRejectsLateModelResultsAcrossStoreInstances(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response *schema.Message
		err      error
	}{
		{
			name:     "candidate",
			response: schema.AssistantMessage(`{"memories":[{"key":"late","claim":"late candidate"}]}`, nil),
		},
		{
			name:     "processed",
			response: schema.AssistantMessage(`{"memories":[]}`, nil),
		},
		{
			name: "error",
			err:  errors.New("late extraction failure"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ws := t.TempDir()
			fixed := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
			consolidatorStore, err := Open(Options{
				WorkspaceRoot:   ws,
				UseEnabled:      true,
				GenerateEnabled: true,
				Now:             func() time.Time { return fixed },
			})
			if err != nil {
				t.Fatalf("Open consolidator store: %v", err)
			}
			resetStore, err := Open(Options{
				WorkspaceRoot:   ws,
				UseEnabled:      true,
				GenerateEnabled: true,
				Now:             func() time.Time { return fixed },
			})
			if err != nil {
				t.Fatalf("Open reset store: %v", err)
			}
			if _, err := consolidatorStore.AddUser("seed", "removed by reset"); err != nil {
				t.Fatalf("AddUser: %v", err)
			}

			blockingModel := &resetFenceModel{
				started:  make(chan struct{}),
				release:  make(chan struct{}),
				response: tc.response,
				err:      tc.err,
			}
			threadID := "thread-late-" + tc.name
			consolidator := &Consolidator{
				Store: consolidatorStore,
				Threads: &resetFenceThreadRepository{
					meta: store.ThreadMeta{
						ID:           threadID,
						UpdatedAt:    fixed.Add(-7 * time.Hour),
						MessageCount: 2,
					},
					messages: []*schema.Message{
						schema.UserMessage("remember the durable setting"),
						schema.AssistantMessage("noted", nil),
					},
				},
				Model:      blockingModel,
				IdleAfter:  6 * time.Hour,
				ScanMaxAge: 10 * 24 * time.Hour,
				MaxPerScan: 1,
				Now:        func() time.Time { return fixed },
			}

			type runResult struct {
				written int
				err     error
			}
			result := make(chan runResult, 1)
			go func() {
				written, runErr := consolidator.RunOnce(context.Background())
				result <- runResult{written: written, err: runErr}
			}()

			select {
			case <-blockingModel.started:
			case <-time.After(5 * time.Second):
				t.Fatal("consolidator model did not start")
			}
			if err := resetStore.Reset(); err != nil {
				t.Fatalf("Reset: %v", err)
			}
			close(blockingModel.release)

			select {
			case got := <-result:
				if got.err != nil || got.written != 0 {
					t.Fatalf("RunOnce = (%d, %v), want (0, nil)", got.written, got.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("consolidator did not return")
			}

			active, err := resetStore.ListActive()
			if err != nil {
				t.Fatalf("ListActive: %v", err)
			}
			if len(active) != 0 {
				t.Fatalf("late memories after reset = %+v", active)
			}
			processed, err := resetStore.IsProcessed(threadID)
			if err != nil {
				t.Fatalf("IsProcessed: %v", err)
			}
			report, err := resetStore.Report()
			if err != nil {
				t.Fatalf("Report: %v", err)
			}
			if processed || report.LastConsolidate != nil || report.LastError != "" {
				t.Fatalf("late consolidation state: processed=%v report=%+v", processed, report)
			}
		})
	}
}

func TestConsolidatorResetFenceInvalidatesWholeRunOnce(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	fixed := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	consolidatorStore, err := Open(Options{
		WorkspaceRoot:   ws,
		UseEnabled:      true,
		GenerateEnabled: true,
		Now:             func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("Open consolidator store: %v", err)
	}
	resetStore, err := Open(Options{
		WorkspaceRoot:   ws,
		UseEnabled:      true,
		GenerateEnabled: true,
		Now:             func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("Open reset store: %v", err)
	}

	listStarted := make(chan struct{})
	listRelease := make(chan struct{})
	modelRelease := make(chan struct{})
	close(modelRelease)
	blockingModel := &resetFenceModel{
		started: make(chan struct{}),
		release: modelRelease,
		response: schema.AssistantMessage(
			`{"memories":[{"key":"late-scan","claim":"late scan candidate"}]}`,
			nil,
		),
	}
	threadID := "thread-late-scan"
	consolidator := &Consolidator{
		Store: consolidatorStore,
		Threads: &resetFenceThreadRepository{
			meta: store.ThreadMeta{
				ID:           threadID,
				UpdatedAt:    fixed.Add(-7 * time.Hour),
				MessageCount: 2,
			},
			messages: []*schema.Message{
				schema.UserMessage("remember this after the scan starts"),
				schema.AssistantMessage("noted", nil),
			},
			listStarted: listStarted,
			listRelease: listRelease,
		},
		Model:      blockingModel,
		IdleAfter:  6 * time.Hour,
		ScanMaxAge: 10 * 24 * time.Hour,
		MaxPerScan: 1,
		Now:        func() time.Time { return fixed },
	}

	type runResult struct {
		written int
		err     error
	}
	result := make(chan runResult, 1)
	go func() {
		written, runErr := consolidator.RunOnce(context.Background())
		result <- runResult{written: written, err: runErr}
	}()
	select {
	case <-listStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("consolidator did not start listing threads")
	}
	if err := resetStore.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	close(listRelease)

	select {
	case got := <-result:
		if got.err != nil || got.written != 0 {
			t.Fatalf("RunOnce = (%d, %v), want (0, nil)", got.written, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consolidator did not return")
	}
	select {
	case <-blockingModel.started:
		t.Fatal("reset generation must prevent the stale scan from reaching the model")
	default:
	}

	active, err := resetStore.ListActive()
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	processed, err := resetStore.IsProcessed(threadID)
	if err != nil {
		t.Fatalf("IsProcessed: %v", err)
	}
	report, err := resetStore.Report()
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(active) != 0 || processed || report.LastConsolidate != nil || report.LastError != "" {
		t.Fatalf("late scan state: active=%+v processed=%v report=%+v", active, processed, report)
	}
}
