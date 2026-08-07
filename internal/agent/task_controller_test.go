package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/store"
)

func taskTestContext(t *testing.T, id string) context.Context {
	t.Helper()
	return chat.WithTaskRunContext(context.Background(), id)
}

func durableTaskTestContext(t *testing.T, threadStore *store.ThreadStore, id string) context.Context {
	t.Helper()
	ctx := store.WithThreadAccess(context.Background(), threadStore, id)
	ctx = chat.WithTaskRunContext(ctx, id)
	return chat.WithTaskStateWriter(ctx, func(ctx context.Context, snapshot []byte) error {
		state, err := threadStore.LoadThread(ctx, id)
		if err != nil {
			return err
		}
		_, err = threadStore.UpdateTaskState(ctx, id, state.Revision, "", store.TaskStateUpdate{Snapshot: snapshot})
		return err
	})
}

func simpleTaskPlan() TaskPlanInput {
	return TaskPlanInput{
		Goal: "add the requested capability",
		Requirements: []TaskRequirementInput{
			{ID: "R1", Description: "the requested capability works"},
		},
		Scenarios: []TaskScenarioInput{
			{ID: "S1", Description: "normal path succeeds", RequirementIDs: []string{"R1"}},
		},
		Tasks: []TaskDefinitionInput{
			{
				ID:          "implement",
				Goal:        "implement the normal path",
				ScenarioIDs: []string{"S1"},
				Proofs: []TaskProofInput{
					{ID: "unit", Command: "go test ./internal/example", Description: "behavior passes"},
				},
			},
		},
	}
}

func TestTaskControllerAcceptsOnlyObservedPassingProofBeforeCompletion(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-proof")

	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.RequestCompletion(ctx); err != nil || result.Complete || result.OK {
		t.Fatalf("early completion = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK || result.TaskState != taskWorking {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	if result, err := controller.RecordProof(ctx, "implement", "unit", ""); err != nil || result.OK {
		t.Fatalf("proof without tool observation = %#v, %v", result, err)
	}

	controller.RecordToolResult(ctx, "shell", "call-unit", `{"command":"go test ./internal/example"}`, `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "call-unit"); err != nil || !result.OK || result.TaskState != taskDone {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}
	if result, err := controller.RequestCompletion(ctx); err != nil || !result.OK || !result.Complete {
		t.Fatalf("completion = %#v, %v", result, err)
	}
	if gate := controller.TaskCompletionGate(ctx); !gate.Complete || gate.Active {
		t.Fatalf("completion gate = %#v", gate)
	}
}

func TestTaskControllerRevokesOnlyUncommittedCompletionOwnedByCurrentTurn(t *testing.T) {
	controller := NewTaskController()
	ctx := chat.WithTaskTurnContext(taskTestContext(t, "task-completion-owner"), "turn-owning-completion")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}
	if result, err := controller.RequestCompletion(ctx); err != nil || !result.OK || !result.Complete {
		t.Fatalf("RequestCompletion = %#v, %v", result, err)
	}

	otherTurn := chat.WithTaskTurnContext(ctx, "turn-after-delivery")
	if receipt := controller.AbortTaskCompletion(otherTurn, "later turn cancelled"); receipt.Applied {
		t.Fatalf("unrelated turn must not revoke accepted completion: %#v", receipt)
	}
	if gate := controller.TaskCompletionGate(ctx); !gate.Complete || gate.Active {
		t.Fatalf("unrelated turn changed completion gate: %#v", gate)
	}

	if receipt := controller.AbortTaskCompletion(ctx, "delivery turn cancelled"); !receipt.Applied {
		t.Fatalf("owning turn must revoke uncommitted completion: %#v", receipt)
	}
	if gate := controller.TaskCompletionGate(ctx); !gate.Active || gate.Complete {
		t.Fatalf("revoked completion gate = %#v", gate)
	}
}

func TestTaskControllerFailedProofNeedsReplanAndPatchInvalidatesEvidence(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-repair")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if _, err := controller.StartTask(ctx, "implement"); err != nil {
		t.Fatal(err)
	}
	controller.RecordToolResult(ctx, "shell", "call-fail", "", `{"command":"go test ./internal/example","exit_code":1}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "call-fail"); err != nil || result.OK || result.TaskState != taskNeedsReplan {
		t.Fatalf("failed proof = %#v, %v", result, err)
	}
	if result, err := controller.ReplanTask(ctx, "implement", "fix assertion"); err != nil || !result.OK || result.TaskState != "" {
		t.Fatalf("ReplanTask = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("restart = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "call-pass", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "call-pass"); err != nil || !result.OK {
		t.Fatalf("passing proof = %#v, %v", result, err)
	}

	// A subsequent workspace mutation invalidates old test evidence rather than
	// allowing a test result from an earlier diff to close the run.
	controller.RecordToolResult(ctx, "apply_patch", "patch-1", "", `{"results":[{"path":"internal/example/example.go"}]}`)
	status := controller.TaskExecutionStatus(ctx)
	if status.DoneTasks != 0 || len(status.Gaps) == 0 {
		t.Fatalf("patch should invalidate accepted evidence: %#v", status)
	}
	if gate := controller.TaskCompletionGate(ctx); !gate.Active || !strings.Contains(gate.Summary, "implement") {
		t.Fatalf("gate after patch = %#v", gate)
	}

	// A shell command that cannot be proven observational outside the declared
	// proof set could have changed the workspace. It must invalidate evidence
	// just as a successful patch does.
	if _, err := controller.StartTask(ctx, "implement"); err != nil {
		t.Fatal(err)
	}
	controller.RecordToolResult(ctx, "shell", "call-pass-again", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "call-pass-again"); err != nil || !result.OK {
		t.Fatalf("proof after patch = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "unknown-shell", "", `{"command":"touch changed.txt","exit_code":0}`)
	if status := controller.TaskExecutionStatus(ctx); status.DoneTasks != 0 {
		t.Fatalf("unplanned shell command should invalidate evidence: %#v", status)
	}
}

func TestTaskControllerInvalidatesEvidenceAfterUncertainPatchResult(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-uncertain-patch")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}

	// apply_patch can have written an earlier operation before a later one
	// fails, so a non-denial result cannot safely reuse older proof evidence.
	controller.RecordToolResult(ctx, "apply_patch", "partial-patch", "", `{"error":"write second file: disk full"}`)
	if status := controller.TaskExecutionStatus(ctx); status.DoneTasks != 0 || len(status.Gaps) == 0 {
		t.Fatalf("uncertain patch result must invalidate evidence: %#v", status)
	}
}

func TestTaskControllerInvalidatesEvidenceAfterFailedUnplannedShell(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-failed-unplanned-shell")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}

	controller.RecordToolResult(ctx, "shell", "failed-mutation", "", `{"command":"sh -c 'touch generated.go; exit 1'","exit_code":1}`)
	if status := controller.TaskExecutionStatus(ctx); status.DoneTasks != 0 || len(status.Gaps) == 0 {
		t.Fatalf("failed unplanned shell must invalidate evidence: %#v", status)
	}
}

func TestTaskControllerInvalidatesUpstreamProofAfterFailedDeclaredShell(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-failed-declared-shell")
	plan := TaskPlanInput{
		Goal: "verify dependent changes",
		Requirements: []TaskRequirementInput{
			{ID: "R1", Description: "foundation remains correct"},
			{ID: "R2", Description: "feature behavior is verified"},
		},
		Scenarios: []TaskScenarioInput{
			{ID: "S1", Description: "foundation works", RequirementIDs: []string{"R1"}},
			{ID: "S2", Description: "feature works", RequirementIDs: []string{"R2"}},
		},
		Tasks: []TaskDefinitionInput{
			{ID: "foundation", Goal: "verify foundation", ScenarioIDs: []string{"S1"}, Proofs: []TaskProofInput{{ID: "foundation-test", Command: "go test ./foundation"}}},
			{ID: "feature", Goal: "verify feature", ScenarioIDs: []string{"S2"}, DependsOn: []string{"foundation"}, Proofs: []TaskProofInput{{ID: "feature-test", Command: "sh -c 'touch generated.go; exit 1'"}}},
		},
	}
	if result, err := controller.SetPlan(ctx, plan); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "foundation"); err != nil || !result.OK {
		t.Fatalf("StartTask foundation = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "foundation-proof", "", `{"command":"go test ./foundation","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "foundation", "foundation-test", "foundation-proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof foundation = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "feature"); err != nil || !result.OK {
		t.Fatalf("StartTask feature = %#v, %v", result, err)
	}

	// A declared proof can still write before it exits unsuccessfully. It must
	// invalidate earlier accepted evidence just like any other uncertain shell.
	controller.RecordToolResult(ctx, "shell", "failed-feature-proof", "", `{"command":"sh -c 'touch generated.go; exit 1'","exit_code":1}`)
	if status := controller.TaskExecutionStatus(ctx); status.DoneTasks != 0 {
		t.Fatalf("failed declared shell must invalidate upstream proof: %#v", status)
	}
}

func TestTaskControllerBoundsLargeShellObservationWithoutLosingProof(t *testing.T) {
	controller := NewTaskController()
	const threadID = "task-bounded-shell-observation"
	ctx := taskTestContext(t, threadID)
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}

	output := `{"command":"go test ./internal/example","exit_code":0,"stdout":"` + strings.Repeat("x", maxTaskObservationOutputRunes*2) + `"}`
	controller.RecordToolResult(ctx, "shell", "large-proof", "", output)
	controller.mu.RLock()
	stored := controller.runs[threadID].observations["large-proof"].Output
	controller.mu.RUnlock()
	if got := len([]rune(stored)); got > maxTaskObservationOutputRunes {
		t.Fatalf("stored shell observation is %d runes, want at most %d", got, maxTaskObservationOutputRunes)
	}
	if result, err := controller.RecordProof(ctx, "implement", "unit", "large-proof"); err != nil || !result.OK {
		t.Fatalf("large shell output must still provide proof fields: %#v, %v", result, err)
	}
}

func TestTaskControllerInterruptPreservesCompletedEvidence(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-control")
	plan := TaskPlanInput{
		Goal: "two steps",
		Requirements: []TaskRequirementInput{
			{ID: "R1", Description: "first behavior"},
			{ID: "R2", Description: "second behavior"},
		},
		Scenarios: []TaskScenarioInput{
			{ID: "S1", Description: "first scenario", RequirementIDs: []string{"R1"}},
			{ID: "S2", Description: "second scenario", RequirementIDs: []string{"R2"}},
		},
		Tasks: []TaskDefinitionInput{
			{ID: "foundation", Goal: "foundation", ScenarioIDs: []string{"S1"}, Proofs: []TaskProofInput{{ID: "p1", Command: "go test ./foundation"}}},
			{ID: "feature", Goal: "feature", ScenarioIDs: []string{"S2"}, DependsOn: []string{"foundation"}, Proofs: []TaskProofInput{{ID: "p2", Command: "go test ./feature"}}},
		},
	}
	if result, err := controller.SetPlan(ctx, plan); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if _, err := controller.StartTask(ctx, "foundation"); err != nil {
		t.Fatal(err)
	}
	controller.RecordToolResult(ctx, "shell", "foundation-proof", "", `{"command":"go test ./foundation","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "foundation", "p1", "foundation-proof"); err != nil || !result.OK {
		t.Fatalf("foundation proof = %#v, %v", result, err)
	}
	if _, err := controller.StartTask(ctx, "feature"); err != nil {
		t.Fatal(err)
	}
	receipt := controller.InterruptTask(ctx, "user interrupted")
	if !receipt.Applied {
		t.Fatalf("interrupt receipt = %#v", receipt)
	}
	status := controller.TaskExecutionStatus(ctx)
	if status.State != taskRunInterrupted || status.DoneTasks != 1 || status.ActiveTask != "" {
		t.Fatalf("interruption should retain completed evidence: %#v", status)
	}
	if packet := controller.ExecutionPacket(ctx); !strings.Contains(packet, "Autonomous task recovery") || !strings.Contains(packet, "foundation/p1") {
		t.Fatalf("interrupted task recovery packet = %q", packet)
	}
	if result, err := controller.SetPlan(ctx, plan); err != nil || !result.OK {
		t.Fatalf("SetPlan after interruption = %#v, %v", result, err)
	}
	status = controller.TaskExecutionStatus(ctx)
	if status.State != taskRunActive || status.DoneTasks != 1 {
		t.Fatalf("unchanged completed evidence was not restored = %#v", status)
	}
	if result, err := controller.StartTask(ctx, "feature"); err != nil || !result.OK {
		t.Fatalf("unfinished task should be restartable = %#v, %v", result, err)
	}
}

func TestTaskControllerInterruptBlocksDeliveryUntilFreshPlan(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-interrupt-delivery-gate")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if receipt := controller.InterruptTask(ctx, "user interrupted"); !receipt.Applied {
		t.Fatalf("InterruptTask = %#v", receipt)
	}

	gate := controller.TaskCompletionGate(ctx)
	if !gate.Active || gate.Complete || !strings.Contains(gate.Gap, "task_plan") {
		t.Fatalf("interrupted task must block delivery until a fresh plan: %#v", gate)
	}
}

func TestTaskControllerNaturalLanguageContinuationRetainsUnchangedEvidence(t *testing.T) {
	controller := NewTaskController()
	const threadID = "task-natural-language-continuation"
	initial := chat.WithTaskRequestContext(taskTestContext(t, threadID), "Add the requested capability.")
	plan := simpleTaskPlan()
	plan.Scenarios[0].RequirementIDs = append(plan.Scenarios[0].RequirementIDs, taskUserRequestID)
	if result, err := controller.SetPlan(initial, plan); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(initial, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(initial, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(initial, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}
	if receipt := controller.InterruptTask(initial, "user interrupted"); !receipt.Applied {
		t.Fatalf("InterruptTask = %#v", receipt)
	}

	continuation := chat.WithTaskRequestContext(taskTestContext(t, threadID), "Continue from the completed work.")
	if result, err := controller.SetPlan(continuation, plan); err != nil || !result.OK {
		t.Fatalf("SetPlan after continuation = %#v, %v", result, err)
	}
	if status := controller.TaskExecutionStatus(continuation); status.State != taskRunActive || status.DoneTasks != 1 {
		t.Fatalf("continuation must retain unchanged evidence: %#v", status)
	}
}

func TestTaskControllerDoesNotReuseProofAfterScopeRequirementChanges(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-requirement-update")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if _, err := controller.StartTask(ctx, "implement"); err != nil {
		t.Fatal(err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}
	if receipt := controller.InterruptTask(ctx, "change scope"); !receipt.Applied {
		t.Fatalf("InterruptTask = %#v", receipt)
	}

	updated := simpleTaskPlan()
	updated.Requirements[0].Description = "the changed requested capability works"
	if result, err := controller.SetPlan(ctx, updated); err != nil || !result.OK {
		t.Fatalf("SetPlan after requirement change = %#v, %v", result, err)
	}
	if status := controller.TaskExecutionStatus(ctx); status.DoneTasks != 0 {
		t.Fatalf("changed requirement must invalidate old proof: %#v", status)
	}
}

func TestTaskControllerRequiresProofAfterTaskStartAndWorkspaceChange(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-proof-freshness")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "before-start", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	if result, err := controller.RecordProof(ctx, "implement", "unit", "before-start"); err != nil || result.OK || !strings.Contains(result.Message, "predates") {
		t.Fatalf("proof before task start = %#v, %v", result, err)
	}

	controller.RecordToolResult(ctx, "shell", "before-patch", "", `{"command":"go test ./internal/example","exit_code":0}`)
	controller.RecordToolResult(ctx, "apply_patch", "patch", "", `{"results":[{"path":"internal/example/example.go"}]}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "before-patch"); err != nil || result.OK || !strings.Contains(result.Message, "predates") {
		t.Fatalf("proof before workspace change = %#v, %v", result, err)
	}

	controller.RecordToolResult(ctx, "shell", "after-patch", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "after-patch"); err != nil || !result.OK || result.TaskState != taskDone {
		t.Fatalf("fresh proof after workspace change = %#v, %v", result, err)
	}
}

func TestTaskControllerRejectsCancelledTransitionAfterInterrupt(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-cancelled-transition")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if receipt := controller.InterruptTask(ctx, "user interrupted"); !receipt.Applied {
		t.Fatalf("InterruptTask = %#v", receipt)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := controller.SetPlan(cancelled, simpleTaskPlan()); !errors.Is(err, context.Canceled) {
		t.Fatalf("SetPlan with cancelled context error = %v, want context.Canceled", err)
	}
	if status := controller.TaskExecutionStatus(ctx); status.State != taskRunInterrupted {
		t.Fatalf("cancelled transition revived interrupted run: %#v", status)
	}
}

func TestTaskControllerDoesNotCompleteInMemoryWhenTaskStatePersistenceFails(t *testing.T) {
	controller := NewTaskController()
	const threadID = "task-completion-persistence-failure"
	writable := chat.WithTaskStateWriter(taskTestContext(t, threadID), func(context.Context, []byte) error {
		return nil
	})
	if result, err := controller.SetPlan(writable, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(writable, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(writable, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(writable, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}

	persistenceErr := errors.New("task state storage is unavailable")
	failing := chat.WithTaskStateWriter(taskTestContext(t, threadID), func(context.Context, []byte) error {
		return persistenceErr
	})
	if _, err := controller.RequestCompletion(failing); !errors.Is(err, persistenceErr) {
		t.Fatalf("RequestCompletion error = %v, want %v", err, persistenceErr)
	}
	if gate := controller.TaskCompletionGate(writable); !gate.Active || gate.Complete {
		t.Fatalf("completion gate after failed persistence = %#v", gate)
	}
	if status := controller.TaskExecutionStatus(writable); status.State != taskRunActive {
		t.Fatalf("task state after failed persistence = %#v", status)
	}
}

func TestTaskControllerRetainsCompletionGateWhenInitialPlanPersistenceFails(t *testing.T) {
	controller := NewTaskController()
	persistenceErr := errors.New("task state storage is unavailable")
	ctx := chat.WithTaskStateWriter(taskTestContext(t, "task-plan-persistence-failure"), func(context.Context, []byte) error {
		return persistenceErr
	})
	if _, err := controller.SetPlan(ctx, simpleTaskPlan()); !errors.Is(err, persistenceErr) {
		t.Fatalf("SetPlan error = %v, want %v", err, persistenceErr)
	}
	if gate := controller.TaskCompletionGate(ctx); !gate.Active || gate.Complete {
		t.Fatalf("completion gate after failed plan persistence = %#v", gate)
	}
}

func TestTaskControllerKeepsRunActiveWhenInterruptPersistenceFails(t *testing.T) {
	controller := NewTaskController()
	const threadID = "task-interrupt-persistence-failure"
	writable := chat.WithTaskStateWriter(taskTestContext(t, threadID), func(context.Context, []byte) error {
		return nil
	})
	if result, err := controller.SetPlan(writable, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}

	persistenceErr := errors.New("task state storage is unavailable")
	failing := chat.WithTaskStateWriter(taskTestContext(t, threadID), func(context.Context, []byte) error {
		return persistenceErr
	})
	receipt := controller.InterruptTask(failing, "user interrupted")
	if receipt.Applied || !strings.Contains(receipt.Summary, persistenceErr.Error()) {
		t.Fatalf("InterruptTask receipt = %#v", receipt)
	}
	if gate := controller.TaskCompletionGate(writable); !gate.Active || gate.Complete {
		t.Fatalf("completion gate after failed interruption persistence = %#v", gate)
	}
	if status := controller.TaskExecutionStatus(writable); status.State != taskRunActive {
		t.Fatalf("task state after failed interruption persistence = %#v", status)
	}
}

func TestTaskPlanUpdateDoesNotReuseProofForChangedScenario(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-scenario-update")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if _, err := controller.StartTask(ctx, "implement"); err != nil {
		t.Fatal(err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}

	updated := simpleTaskPlan()
	updated.Scenarios[0].Description = "normal path succeeds and reports its source"
	if result, err := controller.SetPlan(ctx, updated); err != nil || result.OK {
		t.Fatalf("active scenario rewrite must be rejected = %#v, %v", result, err)
	}
	if receipt := controller.InterruptTask(ctx, "change scope"); !receipt.Applied {
		t.Fatalf("interrupt receipt = %#v", receipt)
	}
	if result, err := controller.SetPlan(ctx, updated); err != nil || !result.OK {
		t.Fatalf("updated plan after interruption = %#v, %v", result, err)
	}
	if status := controller.TaskExecutionStatus(ctx); status.DoneTasks != 0 {
		t.Fatalf("changed scenario must invalidate old proof: %#v", status)
	}
}

func TestTaskControllerRequiresFreshPlanAfterUnplannedPatch(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-unplanned-patch")

	controller.RecordToolResult(ctx, "apply_patch", "patch-before-plan", "", `{"results":[{"path":"internal/example/example.go"}]}`)
	gate := controller.TaskCompletionGate(ctx)
	if !gate.Active || !strings.Contains(gate.Summary, "before a task plan") {
		t.Fatalf("gate after unplanned patch = %#v", gate)
	}
	if result, err := controller.RequestCompletion(ctx); err != nil || result.OK || result.Complete {
		t.Fatalf("completion after unplanned patch = %#v, %v", result, err)
	}
	packet := controller.ExecutionPacket(ctx)
	if !strings.Contains(packet, "Create a fresh task_plan") || !strings.Contains(packet, "patch-before-plan") {
		t.Fatalf("plan-required execution packet = %q", packet)
	}

	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan after unplanned patch = %#v, %v", result, err)
	}
	if gate := controller.TaskCompletionGate(ctx); !gate.Active || strings.Contains(gate.Summary, "before a task plan") {
		t.Fatalf("gate after fresh plan = %#v", gate)
	}
}

func TestTaskControllerRequiresFreshPlanAfterShellBeforePlan(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-shell-before-plan")

	// shell has workspace-write capability. Without an already accepted task
	// graph, even a non-zero result can have changed files before it failed.
	controller.RecordToolResult(ctx, "shell", "shell-before-plan", "", `{"command":"sh -c 'touch generated.go; exit 1'","exit_code":1}`)
	status := controller.TaskExecutionStatus(ctx)
	if status.State != taskRunActive || !status.PlanRequired {
		t.Fatalf("unplanned shell must open a fresh-plan gate: %#v", status)
	}
	if gate := controller.TaskCompletionGate(ctx); !gate.Active || gate.Complete || !strings.Contains(gate.Summary, "before a task plan") {
		t.Fatalf("gate after unplanned shell = %#v", gate)
	}
}

func TestTaskControllerRequiresFreshPlanAfterPatchFollowingInterrupt(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-interrupted-patch")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if _, err := controller.StartTask(ctx, "implement"); err != nil {
		t.Fatal(err)
	}
	if receipt := controller.InterruptTask(ctx, "user interrupted"); !receipt.Applied {
		t.Fatalf("InterruptTask = %#v", receipt)
	}

	controller.RecordToolResult(ctx, "apply_patch", "patch-after-interrupt", "", `{"results":[{"path":"internal/example/example.go"}]}`)
	status := controller.TaskExecutionStatus(ctx)
	if status.State != taskRunActive || !status.PlanRequired {
		t.Fatalf("patch after interrupt must open a fresh-plan gate: %#v", status)
	}
	if gate := controller.TaskCompletionGate(ctx); !gate.Active || !strings.Contains(gate.Summary, "before a task plan") {
		t.Fatalf("gate after interrupted patch = %#v", gate)
	}
}

func TestTaskControllerRequiresFreshPlanAfterLateShellFollowingInterrupt(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-interrupted-shell")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	if receipt := controller.InterruptTask(ctx, "user interrupted"); !receipt.Applied {
		t.Fatalf("InterruptTask = %#v", receipt)
	}

	controller.RecordToolResult(ctx, "shell", "late-shell", "", `{"command":"sh -c 'touch generated.go; exit 1'","exit_code":1}`)
	status := controller.TaskExecutionStatus(ctx)
	if status.State != taskRunActive || !status.PlanRequired {
		t.Fatalf("late shell after interruption must open a fresh-plan gate: %#v", status)
	}
	if gate := controller.TaskCompletionGate(ctx); !gate.Active || !strings.Contains(gate.Summary, "before a task plan") {
		t.Fatalf("gate after late shell = %#v", gate)
	}
}

func TestTaskControllerRequiresFreshPlanAfterLateShellFollowingCompletion(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-completed-shell")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}
	if result, err := controller.RequestCompletion(ctx); err != nil || !result.OK || !result.Complete {
		t.Fatalf("RequestCompletion = %#v, %v", result, err)
	}

	// A shell still in flight when task_complete succeeds can mutate the
	// workspace afterward. Its non-zero exit does not make that mutation safe.
	controller.RecordToolResult(ctx, "shell", "late-shell", "", `{"command":"sh -c 'touch generated.go; exit 1'","exit_code":1}`)
	status := controller.TaskExecutionStatus(ctx)
	if status.State != taskRunActive || !status.PlanRequired {
		t.Fatalf("late shell after completion must open a fresh-plan gate: %#v", status)
	}
	if gate := controller.TaskCompletionGate(ctx); !gate.Active || gate.Complete || !strings.Contains(gate.Summary, "before a task plan") {
		t.Fatalf("gate after late completed shell = %#v", gate)
	}
}

func TestTaskControllerExecutionPacketCarriesRecentObservedResultAndAcceptedEvidence(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-execution-packet")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if _, err := controller.StartTask(ctx, "implement"); err != nil {
		t.Fatal(err)
	}
	controller.RecordToolResult(ctx, "shell", "failed-check", "", `{"command":"go test ./internal/example","exit_code":1,"stderr":"expected boundary behavior"}`)
	packet := controller.ExecutionPacket(ctx)
	if !strings.Contains(packet, "failed-check") || !strings.Contains(packet, "expected boundary behavior") {
		t.Fatalf("packet must retain latest observed shell result: %q", packet)
	}

	controller.RecordToolResult(ctx, "shell", "passing-check", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "passing-check"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}
	packet = controller.ExecutionPacket(ctx)
	if !strings.Contains(packet, "Accepted proof evidence") || !strings.Contains(packet, "implement/unit") || !strings.Contains(packet, "passing-check") {
		t.Fatalf("packet must retain accepted proof evidence: %q", packet)
	}
}

func TestTaskControllerPreservesRawUserRequestAsRootRequirement(t *testing.T) {
	controller := NewTaskController()
	ctx := chat.WithTaskRequestContext(taskTestContext(t, "task-raw-request"), "Add an empty state without changing existing API behavior.")

	plan := simpleTaskPlan()
	if result, err := controller.SetPlan(ctx, plan); err != nil || !result.OK {
		t.Fatalf("plan with an implicit raw request = %#v, %v", result, err)
	}
	status := controller.TaskExecutionStatus(ctx)
	if status.Requirements != 2 || !strings.Contains(controller.ExecutionPacket(ctx), "Add an empty state") {
		t.Fatalf("raw root requirement missing from task state: %#v", status)
	}
	controller.mu.RLock()
	for _, scenario := range controller.runs["task-raw-request"].scenarios {
		if !containsString(scenario.RequirementIDs, taskUserRequestID) {
			controller.mu.RUnlock()
			t.Fatalf("scenario did not inherit %q: %#v", taskUserRequestID, scenario)
		}
	}
	controller.mu.RUnlock()

	plan.Requirements = append(plan.Requirements, TaskRequirementInput{ID: taskUserRequestID, Description: "model-rewritten scope"})
	if _, err := controller.SetPlan(ctx, plan); err == nil || !strings.Contains(err.Error(), "must exactly preserve") {
		t.Fatalf("rewritten raw root requirement error = %v", err)
	}
}

func TestTaskControllerRestoresTaskGraphAndAcceptedProofFromThreadLedger(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "task-resume-proof"
	if _, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: threadID}, "system"); err != nil {
		t.Fatal(err)
	}
	ctx := durableTaskTestContext(t, threadStore, threadID)
	controller := NewTaskController()
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "proof-call", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof-call"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}

	recovered := NewTaskController()
	status := recovered.TaskExecutionStatus(ctx)
	if status.State != taskRunActive || status.DoneTasks != 1 || status.Tasks != 1 {
		t.Fatalf("recovered status = %#v", status)
	}
	packet := recovered.ExecutionPacket(ctx)
	if !strings.Contains(packet, "Accepted proof evidence") || !strings.Contains(packet, "proof-call") {
		t.Fatalf("recovered packet missing proof evidence: %q", packet)
	}
	if result, err := recovered.RequestCompletion(ctx); err != nil || !result.OK || !result.Complete {
		t.Fatalf("RequestCompletion after recovery = %#v, %v", result, err)
	}
	completed := NewTaskController().TaskCompletionGate(ctx)
	if !completed.Complete || completed.Active {
		t.Fatalf("durably completed gate = %#v", completed)
	}
}

func TestTaskControllerMarksInFlightTaskForReplanAfterRecovery(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "task-resume-working"
	if _, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: threadID}, "system"); err != nil {
		t.Fatal(err)
	}
	ctx := durableTaskTestContext(t, threadStore, threadID)
	controller := NewTaskController()
	if _, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartTask(ctx, "implement"); err != nil {
		t.Fatal(err)
	}

	recovered := NewTaskController()
	status := recovered.TaskExecutionStatus(ctx)
	if status.ActiveTask != "" || status.DoneTasks != 0 || !containsTaskGap(status.Gaps, "needs_replan") {
		t.Fatalf("in-flight recovery status = %#v", status)
	}
	if packet := recovered.ExecutionPacket(ctx); !strings.Contains(packet, resumedTaskNeedsReplanFailure) {
		t.Fatalf("recovery packet = %q", packet)
	}
}

func TestTaskControllerRequiresFreshProofsForRecoveredWorkingTask(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "task-resume-fresh-proof"
	if _, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: threadID}, "system"); err != nil {
		t.Fatal(err)
	}
	ctx := durableTaskTestContext(t, threadStore, threadID)
	plan := simpleTaskPlan()
	plan.Tasks[0].Proofs = append(plan.Tasks[0].Proofs, TaskProofInput{
		ID:      "integration",
		Command: "go test ./internal/integration",
	})
	controller := NewTaskController()
	if result, err := controller.SetPlan(ctx, plan); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "unit-before-restart", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "unit-before-restart"); err != nil || !result.OK {
		t.Fatalf("RecordProof before restart = %#v, %v", result, err)
	}

	recovered := NewTaskController()
	if status := recovered.TaskExecutionStatus(ctx); !containsTaskGap(status.Gaps, "needs_replan") {
		t.Fatalf("recovered status = %#v", status)
	}
	if result, err := recovered.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask after recovery = %#v, %v", result, err)
	}
	recovered.RecordToolResult(ctx, "shell", "integration-after-restart", "", `{"command":"go test ./internal/integration","exit_code":0}`)
	result, err := recovered.RecordProof(ctx, "implement", "integration", "integration-after-restart")
	if err != nil || !result.OK {
		t.Fatalf("RecordProof after restart = %#v, %v", result, err)
	}

	status := recovered.TaskExecutionStatus(ctx)
	if result.TaskState != taskWorking || status.DoneTasks != 0 || !containsTaskGap(result.Gaps, "unit") {
		t.Fatalf("recovered task must require a fresh unit proof: result=%#v status=%#v", result, status)
	}
}

func TestTaskControllerKeepsRecoveryGateAfterSnapshotDecodeError(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "task-recovery-error"
	state, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: threadID}, "system")
	if err != nil {
		t.Fatal(err)
	}
	_, err = threadStore.UpdateTaskState(context.Background(), threadID, state.Revision, "", store.TaskStateUpdate{
		Snapshot: json.RawMessage(`{"version":999}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := durableTaskTestContext(t, threadStore, threadID)
	controller := NewTaskController()
	for attempt := 0; attempt < 2; attempt++ {
		gate := controller.TaskCompletionGate(ctx)
		if !gate.Active || !strings.Contains(gate.Gap, "could not be recovered") {
			t.Fatalf("recovery gate attempt %d = %#v", attempt, gate)
		}
	}
	if status := controller.TaskExecutionStatus(ctx); status.State != "recovery_error" {
		t.Fatalf("recovery status = %#v", status)
	}
}

func TestTaskControllerReopensCompletedRunWhenRecoveryFindsLaterShellEvent(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "task-recovery-late-shell"
	if _, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: threadID}, "system"); err != nil {
		t.Fatal(err)
	}
	ctx := durableTaskTestContext(t, threadStore, threadID)
	controller := NewTaskController()
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}
	if result, err := controller.RequestCompletion(ctx); err != nil || !result.OK || !result.Complete {
		t.Fatalf("RequestCompletion = %#v, %v", result, err)
	}

	state, err := threadStore.LoadThread(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, threadID, state.Revision, store.TurnStart{TurnID: "turn-late-shell", Input: "finalize"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.ToolStarted(ctx, threadID, state.Revision, store.ToolStarted{
		TurnID: "turn-late-shell", ToolCallID: "late-shell", ToolName: "shell",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.ToolCompleted(ctx, threadID, state.Revision, store.ToolCompleted{
		TurnID: "turn-late-shell", ToolCallID: "late-shell", ToolName: "shell",
		Output: `{"command":"sh -c 'touch generated.go; exit 1'","exit_code":1}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.FinishTurn(ctx, threadID, store.TurnFinish{TurnID: "turn-late-shell", Cancelled: true, Reason: "simulated crash recovery"}); err != nil {
		t.Fatal(err)
	}

	gate := NewTaskController().TaskCompletionGate(ctx)
	if !gate.Active || gate.Complete || !strings.Contains(gate.Summary, "before a task plan") {
		t.Fatalf("late durable shell must reopen completion gate: %#v", gate)
	}
}

func TestTaskControllerKeepsCompletedRunAfterRecoveryForLaterReadOnlyShell(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "task-recovery-late-read-only-shell"
	if _, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: threadID}, "system"); err != nil {
		t.Fatal(err)
	}
	ctx := durableTaskTestContext(t, threadStore, threadID)
	controller := NewTaskController()
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","impact":"workspace_write","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}
	if result, err := controller.RequestCompletion(ctx); err != nil || !result.OK || !result.Complete {
		t.Fatalf("RequestCompletion = %#v, %v", result, err)
	}

	state, err := threadStore.LoadThread(ctx, threadID)
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, threadID, state.Revision, store.TurnStart{TurnID: "turn-late-read-only", Input: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.ToolStarted(ctx, threadID, state.Revision, store.ToolStarted{
		TurnID: "turn-late-read-only", ToolCallID: "late-read-only", ToolName: "shell",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.ToolCompleted(ctx, threadID, state.Revision, store.ToolCompleted{
		TurnID: "turn-late-read-only", ToolCallID: "late-read-only", ToolName: "shell",
		Impact: "read_only", Output: `{"command":"git status","impact":"read_only","exit_code":0}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.FinishTurn(ctx, threadID, store.TurnFinish{TurnID: "turn-late-read-only", Cancelled: true, Reason: "simulated crash recovery"}); err != nil {
		t.Fatal(err)
	}

	gate := NewTaskController().TaskCompletionGate(ctx)
	if gate.Active || !gate.Complete {
		t.Fatalf("late read-only shell must preserve completed gate: %#v", gate)
	}
}

func TestTaskControllerDowngradesUncommittedCompletionOnRecovery(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const (
		threadID = "task-recovery-uncommitted-completion"
		turnID   = "turn-completing-delivery"
	)
	state, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: threadID}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(context.Background(), threadID, state.Revision, store.TurnStart{TurnID: turnID, Input: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := store.WithThreadAccess(context.Background(), threadStore, threadID)
	ctx = chat.WithTaskRunContext(ctx, threadID)
	ctx = chat.WithTaskTurnContext(ctx, turnID)
	ctx = chat.WithTaskStateWriter(ctx, func(ctx context.Context, snapshot []byte) error {
		current, err := threadStore.LoadThread(ctx, threadID)
		if err != nil {
			return err
		}
		_, err = threadStore.UpdateTaskState(ctx, threadID, current.Revision, turnID, store.TaskStateUpdate{Snapshot: snapshot})
		return err
	})
	controller := NewTaskController()
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}
	if result, err := controller.RequestCompletion(ctx); err != nil || !result.OK || !result.Complete {
		t.Fatalf("RequestCompletion = %#v, %v", result, err)
	}
	if _, err := threadStore.FinishTurn(ctx, threadID, store.TurnFinish{TurnID: turnID, Cancelled: true, Reason: "delivery crashed"}); err != nil {
		t.Fatal(err)
	}

	gate := NewTaskController().TaskCompletionGate(ctx)
	if !gate.Active || gate.Complete || !strings.Contains(gate.Summary, "interrupted") {
		t.Fatalf("uncommitted completion must be interrupted on recovery: %#v", gate)
	}
}

func TestTaskControllerRestoresFreshPlanGateForPrePlanShellAfterRecovery(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "task-recovery-shell-before-plan"
	state, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: threadID}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(context.Background(), threadID, state.Revision, store.TurnStart{TurnID: "turn-shell-before-plan", Input: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.ToolStarted(context.Background(), threadID, state.Revision, store.ToolStarted{
		TurnID: "turn-shell-before-plan", ToolCallID: "shell-before-plan", ToolName: "shell",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.ToolCompleted(context.Background(), threadID, state.Revision, store.ToolCompleted{
		TurnID: "turn-shell-before-plan", ToolCallID: "shell-before-plan", ToolName: "shell",
		Output: `{"command":"sh -c 'touch generated.go; exit 1'","exit_code":1}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.FinishTurn(context.Background(), threadID, store.TurnFinish{TurnID: "turn-shell-before-plan", Cancelled: true, Reason: "simulated crash recovery"}); err != nil {
		t.Fatal(err)
	}
	ctx := store.WithThreadAccess(context.Background(), threadStore, threadID)
	ctx = chat.WithTaskRunContext(ctx, threadID)

	gate := NewTaskController().TaskCompletionGate(ctx)
	if !gate.Active || gate.Complete || !strings.Contains(gate.Summary, "before a task plan") {
		t.Fatalf("recovered pre-plan shell must require a task plan: %#v", gate)
	}
}

func containsTaskGap(gaps []string, fragment string) bool {
	for _, gap := range gaps {
		if strings.Contains(gap, fragment) {
			return true
		}
	}
	return false
}
