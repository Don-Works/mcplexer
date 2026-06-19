package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// handleSkillRunStart implements skill__run_start. Inserts a skill_runs
// row. When `phases` is provided AND `task_epic_id` is empty AND a
// TasksService is wired, also auto-creates a task epic + per-phase
// children so the run is visible + resumable in the task dashboard.
//
// Returns {run_id, task_epic_id?, child_task_ids?} as a JSON tool result.
func (h *handler) handleSkillRunStart(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Skill      string          `json:"skill"`
		Version    int             `json:"version"`
		Phases     []string        `json:"phases"`
		TaskEpicID string          `json:"task_epic_id"`
		Metadata   json.RawMessage `json:"metadata"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if strings.TrimSpace(args.Skill) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "skill is required"}
	}

	wsID := h.currentWorkspaceID(ctx)
	if wsID == "" {
		// Skill telemetry needs a workspace anchor — without one the
		// dashboard's "what's been running here" view has nothing to
		// scope by. Fail loudly so the agent surfaces the misconfig.
		return marshalErrorResult(
			"skill__run_start: session is not rooted in a workspace; cannot record telemetry. " +
				"Open a CWD inside a registered workspace and try again."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}

	metadata := args.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}

	run := &store.SkillRun{
		SkillName:      args.Skill,
		SkillVersion:   args.Version,
		WorkspaceID:    wsID,
		AgentSessionID: h.sessions.sessionID(),
		MetadataJSON:   metadata,
	}
	if err := h.store.RecordSkillRun(ctx, run); err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("record skill run: %v", err)}
	}

	resp := map[string]any{
		"run_id":     run.ID,
		"started_at": run.StartedAt.Format(time.RFC3339Nano),
	}

	// Auto-create task epic + per-phase children when requested.
	if args.TaskEpicID == "" && len(args.Phases) > 0 && h.tasksSvc != nil {
		epicID, childIDs, err := h.autoCreateRunEpic(ctx, wsID, run, args.Phases)
		if err == nil && epicID != "" {
			run.TaskEpicID = epicID
			epicCopy := epicID
			_ = h.store.UpdateSkillRun(ctx, run.ID, store.SkillRunPatch{
				TaskEpicID: &epicCopy,
			})
			resp["task_epic_id"] = epicID
			if len(childIDs) > 0 {
				resp["child_task_ids"] = childIDs
			}
		}
		// Epic creation failure is non-fatal — the skill_runs row is
		// still the source of truth, task tree is purely a UI aid.
	} else if args.TaskEpicID != "" {
		taskID := args.TaskEpicID
		_ = h.store.UpdateSkillRun(ctx, run.ID, store.SkillRunPatch{TaskEpicID: &taskID})
		resp["task_epic_id"] = taskID
	}

	out, _ := json.Marshal(resp)
	return marshalToolResult(string(out)), nil
}

// autoCreateRunEpic creates a task epic for the run plus one child per
// phase. Children are composed into the epic via task service's
// ComposeInto so the bidirectional link is recorded in meta. Returns
// (epicID, childIDs, err). Best-effort: an error from any single
// child create is logged-shaped via return but doesn't unwind the
// whole tree — partial trees are still useful.
func (h *handler) autoCreateRunEpic(ctx context.Context, wsID string, run *store.SkillRun, phases []string) (string, []string, error) {
	title := fmt.Sprintf("skill: %s@%d", run.SkillName, run.SkillVersion)
	desc := fmt.Sprintf("Auto-created by skill__run_start for run %s.\nPhases: %s", run.ID, strings.Join(phases, ", "))
	epic, err := h.tasksSvc.Create(ctx, tasks.CreateOptions{
		WorkspaceID:        wsID,
		Title:              title,
		Description:        desc,
		Status:             "doing",
		Priority:           "normal",
		Tags:               []string{"skill-run", run.SkillName},
		Meta:               fmt.Sprintf("{\"skill_run_id\":\"%s\"}", run.ID),
		SourceKind:         store.TaskSourceAgent,
		SourceSessionID:    h.sessions.sessionID(),
		CreatedBySessionID: h.sessions.sessionID(),
	})
	if err != nil {
		return "", nil, fmt.Errorf("create epic: %w", err)
	}
	childIDs := make([]string, 0, len(phases))
	for _, p := range phases {
		child, cerr := h.tasksSvc.Create(ctx, tasks.CreateOptions{
			WorkspaceID:        wsID,
			Title:              p,
			Status:             "open",
			Priority:           "normal",
			Tags:               []string{"skill-phase"},
			ComposeInto:        epic.ID,
			SourceKind:         store.TaskSourceAgent,
			SourceSessionID:    h.sessions.sessionID(),
			CreatedBySessionID: h.sessions.sessionID(),
		})
		if cerr != nil {
			continue
		}
		childIDs = append(childIDs, child.ID)
	}
	return epic.ID, childIDs, nil
}

// handleSkillPhase implements skill__phase. Appends an event to
// phases_json and (when an epic is attached) mirrors phase state to
// the matching child task.
func (h *handler) handleSkillPhase(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		RunID string `json:"run_id"`
		Phase string `json:"phase"`
		Event string `json:"event"`
		Note  string `json:"note"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.RunID) == "" || strings.TrimSpace(args.Phase) == "" || strings.TrimSpace(args.Event) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "run_id, phase, and event are all required"}
	}
	switch args.Event {
	case "started", "completed", "failed":
	default:
		return nil, &RPCError{Code: CodeInvalidParams, Message: "event must be one of: started, completed, failed"}
	}

	run, err := h.store.GetSkillRun(ctx, args.RunID)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult(fmt.Sprintf("skill run %q not found", args.RunID)), nil
	}
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	if rpc := h.requireWorkspaceWrite(ctx, run.WorkspaceID); rpc != nil {
		return nil, rpc
	}

	// Append to phases_json.
	var events []store.SkillRunPhaseEvent
	if len(run.PhasesJSON) > 0 {
		_ = json.Unmarshal(run.PhasesJSON, &events)
	}
	events = append(events, store.SkillRunPhaseEvent{
		Phase: args.Phase,
		Event: args.Event,
		At:    time.Now().UTC(),
		Note:  args.Note,
	})
	encoded, _ := json.Marshal(events)
	if err := h.store.UpdateSkillRun(ctx, args.RunID, store.SkillRunPatch{
		PhasesJSON: encoded,
	}); err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}

	// Mirror to task child when epic is attached + service wired.
	if run.TaskEpicID != "" && h.tasksSvc != nil {
		_ = h.mirrorPhaseToTask(ctx, run, args.Phase, args.Event, args.Note)
	}

	return marshalToolResult(fmt.Sprintf(
		"recorded %s/%s on run %s",
		args.Phase, args.Event, args.RunID,
	)), nil
}

// mirrorPhaseToTask finds the child of the run's epic whose title
// matches the phase name and flips its status to reflect the event.
// Silent failures — the skill_runs row remains authoritative.
func (h *handler) mirrorPhaseToTask(ctx context.Context, run *store.SkillRun, phase, event, note string) error {
	children, err := h.tasksSvc.List(ctx, store.TaskFilter{
		WorkspaceID: run.WorkspaceID,
		Limit:       200,
	})
	if err != nil {
		return err
	}
	var target *store.Task
	for i := range children {
		t := children[i]
		if t.Title != phase {
			continue
		}
		if !strings.Contains(string(t.Meta), run.TaskEpicID) {
			continue
		}
		target = &t
		break
	}
	if target == nil {
		return nil
	}
	var newStatus string
	var terminal *bool
	switch event {
	case "started":
		newStatus = "doing"
	case "completed":
		newStatus = "done"
		t := true
		terminal = &t
	case "failed":
		newStatus = "blocked"
	}
	patch := tasks.UpdatePatch{
		Status:             &newStatus,
		Terminal:           terminal,
		UpdatedBySessionID: h.sessions.sessionID(),
	}
	if note != "" {
		_, _ = h.tasksSvc.AppendNote(ctx, run.WorkspaceID, target.ID, note, h.sessions.sessionID(), store.TaskSourceAgent)
	}
	_, err = h.tasksSvc.Update(ctx, run.WorkspaceID, target.ID, patch)
	return err
}

// handleSkillRunComplete implements skill__run_complete.
func (h *handler) handleSkillRunComplete(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		RunID   string `json:"run_id"`
		Outcome string `json:"outcome"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.RunID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "run_id is required"}
	}
	switch args.Outcome {
	case store.SkillRunOutcomeSuccess, store.SkillRunOutcomeFailure, store.SkillRunOutcomeCancelled:
	default:
		return nil, &RPCError{Code: CodeInvalidParams, Message: "outcome must be one of: success, failure, cancelled"}
	}

	run, err := h.store.GetSkillRun(ctx, args.RunID)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult(fmt.Sprintf("skill run %q not found", args.RunID)), nil
	}
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	if rpc := h.requireWorkspaceWrite(ctx, run.WorkspaceID); rpc != nil {
		return nil, rpc
	}

	if err := h.store.UpdateSkillRun(ctx, args.RunID, store.SkillRunPatch{
		Outcome: &args.Outcome,
	}); err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}

	// When a task epic was attached, close it out so the dashboard
	// reflects the run's outcome without another tool call.
	if run.TaskEpicID != "" && h.tasksSvc != nil {
		closeEpic(ctx, h, run, args.Outcome, args.Summary)
	}

	return marshalToolResult(fmt.Sprintf(
		"completed run %s with outcome=%s",
		args.RunID, args.Outcome,
	)), nil
}

// closeEpic flips the run's task epic to a terminal status mirroring
// the outcome and appends the summary as a final note.
func closeEpic(ctx context.Context, h *handler, run *store.SkillRun, outcome, summary string) {
	var newStatus string
	switch outcome {
	case store.SkillRunOutcomeSuccess:
		newStatus = "done"
	case store.SkillRunOutcomeFailure:
		newStatus = "blocked"
	case store.SkillRunOutcomeCancelled:
		newStatus = "cancelled"
	default:
		return
	}
	terminal := true
	if summary != "" {
		_, _ = h.tasksSvc.AppendNote(ctx, run.WorkspaceID, run.TaskEpicID, summary, h.sessions.sessionID(), store.TaskSourceAgent)
	}
	_, _ = h.tasksSvc.Update(ctx, run.WorkspaceID, run.TaskEpicID, tasks.UpdatePatch{
		Status:             &newStatus,
		Terminal:           &terminal,
		UpdatedBySessionID: h.sessions.sessionID(),
	})
}
