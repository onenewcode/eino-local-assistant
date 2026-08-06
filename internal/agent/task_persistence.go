package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"eino-local-assistant/internal/chat"
)

const (
	taskSnapshotVersion           = 1
	maxPersistedTaskObservations  = 16
	resumedTaskNeedsReplanFailure = "session resumed while this task was active; inspect the workspace and replan before continuing"
)

// taskRunSnapshot is deliberately a compact controller projection. The
// append-only thread ledger remains the source of full tool output; this
// snapshot stores only the graph, accepted evidence references, and a bounded
// set of observations needed to choose the next action after /resume.
type taskRunSnapshot struct {
	Version      int                       `json:"version"`
	Goal         string                    `json:"goal"`
	State        string                    `json:"state"`
	Requirements []TaskRequirementInput    `json:"requirements"`
	Scenarios    []TaskScenarioInput       `json:"scenarios"`
	Tasks        []taskNodeSnapshot        `json:"tasks"`
	Observations []taskObservationSnapshot `json:"observations,omitempty"`
	ActiveTask   string                    `json:"active_task,omitempty"`
	Sequence     uint64                    `json:"sequence,omitempty"`
	PlanRequired bool                      `json:"plan_required,omitempty"`
}

type taskNodeSnapshot struct {
	Definition         TaskDefinitionInput `json:"definition"`
	State              string              `json:"state"`
	Evidence           []taskEvidence      `json:"evidence,omitempty"`
	ProofAfterSequence uint64              `json:"proof_after_sequence,omitempty"`
	Failure            string              `json:"failure,omitempty"`
}

type taskObservationSnapshot struct {
	ID       string                   `json:"id"`
	Tool     string                   `json:"tool"`
	Input    string                   `json:"input,omitempty"`
	Output   string                   `json:"output,omitempty"`
	Shell    *taskShellResultSnapshot `json:"shell,omitempty"`
	Sequence uint64                   `json:"sequence"`
	UsedBy   string                   `json:"used_by,omitempty"`
}

type taskShellResultSnapshot struct {
	Command   string `json:"command"`
	Impact    string `json:"impact,omitempty"`
	ExitCode  *int   `json:"exit_code"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
	Denied    bool   `json:"denied,omitempty"`
}

func (c *TaskController) loadRunLocked(ctx context.Context, threadID string) error {
	if c.loaded[threadID] {
		return nil
	}
	recovery, err := chat.LoadTaskStateRecovery(ctx)
	if err != nil {
		return fmt.Errorf("load durable task state: %w", err)
	}
	raw := recovery.Snapshot
	if len(raw) == 0 {
		if recovery.PotentiallyMutatingToolAfterSnapshot {
			// A pre-plan shell or patch was durably recorded without a matching
			// controller snapshot. Recreate the strict admission gate rather than
			// letting a resumed session describe that workspace as unplanned work.
			c.runs[threadID] = newPlanRequiredTaskRun()
		}
		c.loaded[threadID] = true
		return nil
	}
	run, err := taskRunFromSnapshot(raw)
	if err != nil {
		return fmt.Errorf("decode durable task state: %w", err)
	}
	reconcileRestoredTaskRun(run, recovery)
	c.runs[threadID] = run
	c.loaded[threadID] = true
	return nil
}

// persistTaskRunLocked writes a candidate graph before its caller publishes it
// in the in-memory controller. This keeps a failed journal write from making a
// non-durable transition appear complete to the session completion gate.
func (c *TaskController) persistTaskRunLocked(ctx context.Context, run *taskRun) error {
	if run == nil {
		return nil
	}
	snapshot, err := marshalTaskRunSnapshot(run)
	if err != nil {
		return err
	}
	if err := chat.PersistTaskState(ctx, snapshot); err != nil {
		return fmt.Errorf("persist task state: %w", err)
	}
	return nil
}

func marshalTaskRunSnapshot(run *taskRun) ([]byte, error) {
	if run == nil {
		return nil, fmt.Errorf("task run is required")
	}
	snapshot := taskRunSnapshot{
		Version:      taskSnapshotVersion,
		Goal:         run.goal,
		State:        run.state,
		Requirements: make([]TaskRequirementInput, 0, len(run.requirements)),
		Scenarios:    make([]TaskScenarioInput, 0, len(run.scenarios)),
		Tasks:        make([]taskNodeSnapshot, 0, len(run.tasks)),
		Observations: taskObservationsForSnapshot(run),
		ActiveTask:   run.activeTask,
		Sequence:     run.sequence,
		PlanRequired: run.planRequired,
	}
	for _, id := range sortedRequirementIDs(run) {
		requirement := run.requirements[id]
		snapshot.Requirements = append(snapshot.Requirements, TaskRequirementInput{ID: requirement.ID, Description: requirement.Description})
	}
	for _, id := range sortedScenarioIDs(run) {
		scenario := run.scenarios[id]
		snapshot.Scenarios = append(snapshot.Scenarios, TaskScenarioInput{
			ID:             scenario.ID,
			Description:    scenario.Description,
			RequirementIDs: append([]string(nil), scenario.RequirementIDs...),
		})
	}
	for _, id := range sortedTaskIDs(run) {
		task := run.tasks[id]
		definition := TaskDefinitionInput{
			ID:          task.ID,
			Goal:        task.Goal,
			ScenarioIDs: append([]string(nil), task.ScenarioIDs...),
			DependsOn:   append([]string(nil), task.DependsOn...),
			Assumptions: append([]string(nil), task.Assumptions...),
			Proofs:      make([]TaskProofInput, 0, len(task.Proofs)),
		}
		for _, proofID := range sortedProofIDs(task) {
			proof := task.Proofs[proofID]
			definition.Proofs = append(definition.Proofs, TaskProofInput{ID: proof.ID, Command: proof.Command, Description: proof.Description})
		}
		evidence := make([]taskEvidence, 0, len(task.Evidence))
		for _, proofID := range sortedProofIDs(task) {
			if accepted, ok := task.Evidence[proofID]; ok {
				evidence = append(evidence, accepted)
			}
		}
		snapshot.Tasks = append(snapshot.Tasks, taskNodeSnapshot{
			Definition:         definition,
			State:              task.State,
			Evidence:           evidence,
			ProofAfterSequence: task.ProofAfterSequence,
			Failure:            task.Failure,
		})
	}
	return json.Marshal(snapshot)
}

func taskObservationsForSnapshot(run *taskRun) []taskObservationSnapshot {
	if run == nil || len(run.observations) == 0 {
		return nil
	}
	observations := make([]*taskObservation, 0, len(run.observations))
	for _, observation := range run.observations {
		if observation != nil {
			observations = append(observations, observation)
		}
	}
	sort.Slice(observations, func(i, j int) bool {
		if observations[i].Sequence == observations[j].Sequence {
			return observations[i].ID < observations[j].ID
		}
		return observations[i].Sequence < observations[j].Sequence
	})
	selected := make(map[string]*taskObservation, maxPersistedTaskObservations)
	for index := len(observations) - 1; index >= 0 && len(selected) < maxTaskPacketObservations; index-- {
		selected[observations[index].ID] = observations[index]
	}
	for index := len(observations) - 1; index >= 0 && len(selected) < maxPersistedTaskObservations; index-- {
		observation := observations[index]
		if observation.Tool == "shell" && observation.UsedBy == "" {
			selected[observation.ID] = observation
		}
	}
	result := make([]taskObservationSnapshot, 0, len(selected))
	for _, observation := range observations {
		if _, ok := selected[observation.ID]; !ok {
			continue
		}
		snapshot := taskObservationSnapshot{
			ID:       observation.ID,
			Tool:     observation.Tool,
			Input:    truncateTaskObservationOutput(observation.Input),
			Output:   truncateTaskObservationOutput(observation.Output),
			Sequence: observation.Sequence,
			UsedBy:   observation.UsedBy,
		}
		if observation.Shell != nil {
			snapshot.Shell = taskShellSnapshotFromResult(*observation.Shell)
		} else if shell, err := parseProofShellResult(observation.Output); err == nil {
			snapshot.Shell = taskShellSnapshotFromResult(shell)
		}
		result = append(result, snapshot)
	}
	return result
}

func taskShellSnapshotFromResult(result proofShellResult) *taskShellResultSnapshot {
	if result.ExitCode == nil {
		return nil
	}
	exitCode := *result.ExitCode
	return &taskShellResultSnapshot{
		Command:   result.Command,
		Impact:    result.Impact,
		ExitCode:  &exitCode,
		TimedOut:  result.TimedOut,
		Cancelled: result.Cancelled,
		Denied:    result.Denied,
	}
}

func taskRunFromSnapshot(raw []byte) (*taskRun, error) {
	var snapshot taskRunSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Version != taskSnapshotVersion {
		return nil, fmt.Errorf("unsupported task snapshot version %d", snapshot.Version)
	}
	if snapshot.PlanRequired {
		run := newPlanRequiredTaskRun()
		if goal := strings.TrimSpace(snapshot.Goal); goal != "" {
			run.goal = goal
		}
		if snapshot.State != taskRunActive {
			return nil, fmt.Errorf("plan-required task run must be active")
		}
		applyTaskSnapshotRuntimeFields(run, snapshot)
		return run, nil
	}
	definitions := make([]TaskDefinitionInput, 0, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		definitions = append(definitions, task.Definition)
	}
	run, err := taskRunFromPlan(TaskPlanInput{
		Goal:         snapshot.Goal,
		Requirements: snapshot.Requirements,
		Scenarios:    snapshot.Scenarios,
		Tasks:        definitions,
	}, "")
	if err != nil {
		return nil, err
	}
	if !validTaskRunState(snapshot.State) {
		return nil, fmt.Errorf("invalid task run state %q", snapshot.State)
	}
	run.state = snapshot.State
	for _, persisted := range snapshot.Tasks {
		task := run.tasks[persisted.Definition.ID]
		if task == nil || !validTaskNodeState(persisted.State) {
			return nil, fmt.Errorf("invalid persisted task state for %q", persisted.Definition.ID)
		}
		task.State = persisted.State
		task.Failure = strings.TrimSpace(persisted.Failure)
		task.ProofAfterSequence = persisted.ProofAfterSequence
		task.Evidence = make(map[string]taskEvidence, len(persisted.Evidence))
		for _, evidence := range persisted.Evidence {
			proof, exists := task.Proofs[evidence.ProofID]
			if !exists || evidence.ExitCode != 0 || strings.TrimSpace(evidence.ToolCallID) == "" || evidence.Command != proof.Command {
				return nil, fmt.Errorf("invalid accepted evidence for task %q proof %q", task.ID, evidence.ProofID)
			}
			if _, exists := task.Evidence[evidence.ProofID]; exists {
				return nil, fmt.Errorf("duplicate accepted evidence for task %q proof %q", task.ID, evidence.ProofID)
			}
			task.Evidence[evidence.ProofID] = evidence
		}
		if task.State == taskDone && !allProofsPassed(task) {
			return nil, fmt.Errorf("done task %q is missing proof evidence", task.ID)
		}
	}
	applyTaskSnapshotRuntimeFields(run, snapshot)
	if run.activeTask != "" {
		active := run.tasks[run.activeTask]
		if active == nil || active.State != taskWorking || run.state != taskRunActive {
			return nil, fmt.Errorf("invalid active task %q", run.activeTask)
		}
	}
	return run, nil
}

func applyTaskSnapshotRuntimeFields(run *taskRun, snapshot taskRunSnapshot) {
	run.activeTask = strings.TrimSpace(snapshot.ActiveTask)
	run.sequence = snapshot.Sequence
	run.planRequired = snapshot.PlanRequired
	run.observations = make(map[string]*taskObservation, len(snapshot.Observations))
	for _, persisted := range snapshot.Observations {
		id := strings.TrimSpace(persisted.ID)
		toolName := strings.TrimSpace(persisted.Tool)
		if id == "" || toolName == "" {
			continue
		}
		if _, exists := run.observations[id]; exists {
			continue
		}
		output := persisted.Output
		var shell *proofShellResult
		if persisted.Shell != nil && persisted.Shell.ExitCode != nil {
			restoredShell := proofShellResult{
				Command:   persisted.Shell.Command,
				Impact:    persisted.Shell.Impact,
				ExitCode:  persisted.Shell.ExitCode,
				TimedOut:  persisted.Shell.TimedOut,
				Cancelled: persisted.Shell.Cancelled,
				Denied:    persisted.Shell.Denied,
			}
			shell = cloneProofShellResult(&restoredShell)
			normalized, err := json.Marshal(restoredShell)
			if err == nil {
				output = string(normalized)
			}
		}
		run.observations[id] = &taskObservation{
			ID:       id,
			Tool:     toolName,
			Input:    persisted.Input,
			Output:   output,
			Shell:    shell,
			Sequence: persisted.Sequence,
			UsedBy:   strings.TrimSpace(persisted.UsedBy),
		}
		if persisted.Sequence > run.sequence {
			run.sequence = persisted.Sequence
		}
	}
	pruneTaskObservations(run)
}

func reconcileRestoredTaskRun(run *taskRun, recovery chat.TaskStateRecovery) {
	if run == nil {
		return
	}
	if run.state == taskRunActive {
		for _, task := range run.tasks {
			if task.State != taskWorking {
				continue
			}
			task.State = taskNeedsReplan
			task.ProofAfterSequence = run.sequence
			task.Failure = resumedTaskNeedsReplanFailure
		}
		run.activeTask = ""
	}
	if run.state == taskRunComplete && !recovery.SnapshotTurnCommitted {
		// task_complete persists before Session commits the final delivery. A
		// restart cannot treat that provisional approval as user-visible work.
		run.state = taskRunInterrupted
		run.activeTask = ""
		run.completionTurnID = ""
	}
	if recovery.PotentiallyMutatingToolAfterSnapshot {
		// The ledger contains a shell or patch lifecycle event that the task
		// snapshot never reconciled. The tool may have changed the workspace
		// before a crash, so the old graph and proof set are no longer safe.
		run.state = taskRunActive
		run.activeTask = ""
		run.completionTurnID = ""
		run.planRequired = true
		for _, task := range run.tasks {
			task.Evidence = make(map[string]taskEvidence)
			task.ProofAfterSequence = run.sequence
			switch task.State {
			case taskDone:
				task.State = taskPlanned
			case taskWorking, taskInterrupted:
				task.State = taskNeedsReplan
			}
			task.Failure = "workspace may have changed after the last durable task snapshot"
		}
	}
}

func validTaskRunState(state string) bool {
	switch state {
	case taskRunActive, taskRunComplete, taskRunInterrupted:
		return true
	default:
		return false
	}
}

func validTaskNodeState(state string) bool {
	switch state {
	case taskPlanned, taskWorking, taskNeedsReplan, taskDone, taskInterrupted:
		return true
	default:
		return false
	}
}
