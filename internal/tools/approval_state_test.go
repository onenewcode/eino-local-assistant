package tools

import (
	"sync"
	"testing"
)

func TestApprovalStateSwitchesSafely(t *testing.T) {
	state, err := NewApprovalState(ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.InteractiveMode(); got != "ask" {
		t.Fatalf("initial interactive mode = %q, want ask", got)
	}

	const workers = 8
	const iterations = 100
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				mode := "ask"
				if (worker+iteration)%2 == 0 {
					mode = "auto"
				}
				if err := state.SetInteractiveMode(mode); err != nil {
					t.Errorf("SetInteractiveMode(%q): %v", mode, err)
				}
				if got := state.Mode(); got != ApprovalOnRequest && got != ApprovalNever {
					t.Errorf("unexpected mode %q", got)
				}
			}
		}(worker)
	}
	wg.Wait()

	if err := state.SetInteractiveMode("auto"); err != nil {
		t.Fatal(err)
	}
	if state.Mode() != ApprovalNever || state.InteractiveMode() != "auto" {
		t.Fatalf("auto state = mode %q / interactive %q", state.Mode(), state.InteractiveMode())
	}
	if err := state.SetInteractiveMode("ask"); err != nil {
		t.Fatal(err)
	}
	if state.Mode() != ApprovalOnRequest || state.InteractiveMode() != "ask" {
		t.Fatalf("ask state = mode %q / interactive %q", state.Mode(), state.InteractiveMode())
	}
	if err := state.SetInteractiveMode("never"); err == nil {
		t.Fatal("unsupported TUI mode unexpectedly accepted")
	}
}
