package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Consolidator scans idle sessions and extracts candidate memories.
type Consolidator struct {
	Store   *Store
	Threads store.ThreadRepository
	Model   model.BaseChatModel
	// ActiveThreadID returns the currently open session (never claimed).
	// When nil, no thread is treated as active.
	ActiveThreadID func() string
	IdleAfter      time.Duration
	ScanMaxAge     time.Duration
	MaxPerScan     int
	Now            func() time.Time
}

// RunOnce claims up to MaxPerScan idle threads and writes candidates.
func (c *Consolidator) RunOnce(ctx context.Context) (int, error) {
	if c == nil || c.Store == nil || c.Threads == nil || c.Model == nil {
		return 0, nil
	}
	if !c.Store.GenerateEnabled() {
		return 0, nil
	}
	nowFn := c.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn().UTC()
	idleAfter := c.IdleAfter
	if idleAfter <= 0 {
		idleAfter = 6 * time.Hour
	}
	maxAge := c.ScanMaxAge
	if maxAge <= 0 {
		maxAge = 10 * 24 * time.Hour
	}
	maxN := c.MaxPerScan
	if maxN <= 0 {
		maxN = 2
	}
	activeID := ""
	if c.ActiveThreadID != nil {
		activeID = c.ActiveThreadID()
	}

	metas, err := c.Threads.ListThreads(ctx)
	if err != nil {
		return 0, err
	}
	extractor := &Extractor{Model: c.Model}
	written := 0
	scanned := 0
	for _, meta := range metas {
		if scanned >= maxN {
			break
		}
		if meta.ID == "" || meta.ID == activeID {
			continue
		}
		if now.Sub(meta.UpdatedAt) < idleAfter {
			continue
		}
		if now.Sub(meta.UpdatedAt) > maxAge {
			continue
		}
		if meta.MessageCount == 0 {
			continue
		}
		done, err := c.Store.IsProcessed(meta.ID)
		if err != nil {
			return written, err
		}
		if done {
			continue
		}
		scanned++
		n, err := c.processThread(ctx, extractor, meta.ID)
		if err != nil {
			_ = c.Store.RecordExtractError(err)
			// Do not mark processed — allow retry on a later scan.
			continue
		}
		if err := c.Store.MarkExtracted(meta.ID); err != nil {
			_ = c.Store.RecordExtractError(err)
			continue
		}
		written += n
	}
	return written, nil
}

func (c *Consolidator) processThread(ctx context.Context, extractor *Extractor, threadID string) (int, error) {
	msgs, err := c.Threads.LoadRecentMessages(ctx, threadID, 40)
	if err != nil {
		return 0, err
	}
	transcript := renderTranscript(msgs)
	if strings.TrimSpace(transcript) == "" {
		return 0, nil
	}
	drafts, err := extractor.ExtractCandidates(ctx, transcript)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range drafts {
		if _, err := c.Store.AddCandidate(d.Key, d.Claim, threadID, nil); err != nil {
			// Skip refused candidates (e.g. user owns key); other errors abort.
			if strings.Contains(err.Error(), "candidate refused") {
				continue
			}
			return n, err
		}
		n++
	}
	return n, nil
}

func renderTranscript(msgs []*schema.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m == nil {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if m.Role == schema.System {
			continue
		}
		if len(content) > 4000 {
			content = content[:4000] + "…"
		}
		fmt.Fprintf(&b, "%s: %s\n\n", m.Role, content)
	}
	return b.String()
}

// StartLoop runs RunOnce on interval until ctx is cancelled.
func (c *Consolidator) StartLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go func() {
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			_, _ = c.RunOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
