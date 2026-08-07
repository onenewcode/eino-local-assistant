package agent

import (
	"context"
	"encoding/json"
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

func samplePlan() UpdatePlanInput {
	return UpdatePlanInput{
		Explanation: "ship the feature",
		Plan: []PlanItemInput{
			{Step: "inspect the code", Status: planStatusCompleted},
			{Step: "implement the change", Status: planStatusInProgress},
			{Step: "run tests", Status: planStatusPending},
		},
	}
}

func TestUpdatePlanAcceptsCodexStyleChecklist(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "plan-basic")
	result, err := controller.UpdatePlan(ctx, samplePlan())
	if err != nil || !result.OK || result.Message != "Plan updated" {
		t.Fatalf("UpdatePlan = %#v, %v", result, err)
	}
	status := controller.TaskExecutionStatus(ctx)
	if !status.Available || status.State != planRunActive || status.Tasks != 3 || status.DoneTasks != 1 {
		t.Fatalf("status = %#v", status)
	}
	if got := strings.Join(status.ActiveTasks, ","); got != "step-2" {
		t.Fatalf("active = %q", got)
	}
	if status.Items[0].State != "done" || status.Items[1].State != "working" || status.Items[2].State != "pending" {
		t.Fatalf("item states = %#v", status.Items)
	}
}

func TestUpdatePlanEnforcesAtMostOneInProgress(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "plan-one-progress")
	result, err := controller.UpdatePlan(ctx, UpdatePlanInput{Plan: []PlanItemInput{
		{Step: "a", Status: planStatusInProgress},
		{Step: "b", Status: planStatusInProgress},
	}})
	if err != nil || result.OK {
		t.Fatalf("expected soft rejection, got %#v %v", result, err)
	}
	if !strings.Contains(result.Message, "at most one") {
		t.Fatalf("message = %q", result.Message)
	}
}

func TestUpdatePlanRejectsEmptySteps(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "plan-empty")
	result, err := controller.UpdatePlan(ctx, UpdatePlanInput{})
	if err != nil || result.OK {
		t.Fatalf("expected rejection: %#v %v", result, err)
	}
}

func TestExecutionPacketListsChecklistWithoutDeliveryLock(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "plan-packet")
	if _, err := controller.UpdatePlan(ctx, samplePlan()); err != nil {
		t.Fatal(err)
	}
	packet := controller.ExecutionPacket(ctx)
	for _, want := range []string{"Current plan", "implement the change", "does not gate delivery"} {
		if !strings.Contains(packet, want) {
			t.Fatalf("packet missing %q:\n%s", want, packet)
		}
	}
	if strings.Contains(packet, "task_complete") || strings.Contains(packet, "proof") {
		t.Fatalf("packet must not revive proof/completion language:\n%s", packet)
	}
}

func TestInterruptMarksPlan(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "plan-interrupt")
	if _, err := controller.UpdatePlan(ctx, samplePlan()); err != nil {
		t.Fatal(err)
	}
	receipt := controller.InterruptTask(ctx, "user cancel")
	if !receipt.Applied {
		t.Fatalf("receipt = %#v", receipt)
	}
	status := controller.TaskExecutionStatus(ctx)
	if status.State != planRunInterrupted {
		t.Fatalf("state = %q", status.State)
	}
}

func TestPlanPersistsAndRestoresAcrossController(t *testing.T) {
	threadStore, err := store.OpenThreadStore(t.TempDir(), store.ThreadStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = threadStore.Close() })
	id := "plan-persist"
	if _, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: id}, "system"); err != nil {
		t.Fatal(err)
	}
	ctx := durableTaskTestContext(t, threadStore, id)
	controller := NewTaskController()
	if result, err := controller.UpdatePlan(ctx, samplePlan()); err != nil || !result.OK {
		t.Fatalf("UpdatePlan = %#v %v", result, err)
	}

	recovered := NewTaskController()
	status := recovered.TaskExecutionStatus(ctx)
	if status.Tasks != 3 || status.DoneTasks != 1 || status.Items[1].Goal != "implement the change" {
		t.Fatalf("recovered status = %#v", status)
	}
}

func TestLegacySnapshotVersionsAreIgnored(t *testing.T) {
	threadStore, err := store.OpenThreadStore(t.TempDir(), store.ThreadStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = threadStore.Close() })
	id := "plan-legacy"
	if _, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: id}, "system"); err != nil {
		t.Fatal(err)
	}
	ctx := durableTaskTestContext(t, threadStore, id)
	legacy, _ := json.Marshal(map[string]any{"version": 2, "goal": "old", "state": "active", "tasks": []any{}})
	state, err := threadStore.LoadThread(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.UpdateTaskState(ctx, id, state.Revision, "", store.TaskStateUpdate{Snapshot: legacy}); err != nil {
		t.Fatal(err)
	}
	controller := NewTaskController()
	status := controller.TaskExecutionStatus(ctx)
	if status.Available {
		t.Fatalf("legacy snapshot should not restore active plan: %#v", status)
	}
}
