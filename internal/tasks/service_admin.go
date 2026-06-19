// service_admin.go — Phase 5 admin surface on top of Service. These
// methods back the CWD-gated `task__consolidate_statuses /
// __apply_status_consolidation / __rebind_peer` MCP tools. They are
// kept in a separate file so the universal CRUD path in service.go
// stays focused.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// StatusConsolidationProposal is the structured plan returned by
// ConsolidateStatusesDryRun. The MCP layer marshals it back to the
// agent verbatim; ApplyStatusConsolidation accepts the same shape.
type StatusConsolidationProposal struct {
	WorkspaceID string         `json:"workspace_id"`
	Counts      map[string]int `json:"counts"`
	Vocabulary  []VocabSummary `json:"vocabulary"`
	Merges      []StatusMerge  `json:"merges"`
	Note        string         `json:"note,omitempty"`
}

// VocabSummary is the per-status row from task_status_vocabulary
// projected into the proposal.
type VocabSummary struct {
	StatusText string `json:"status_text"`
	IsTerminal bool   `json:"is_terminal"`
	ManagedBy  string `json:"managed_by"`
}

// StatusMerge is one row in the proposal: "rewrite every task whose
// status == From to status == To, and mark To as Terminal in the
// vocabulary".
type StatusMerge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Terminal bool   `json:"terminal"`
}

// ConsolidationApplyResult is what ApplyStatusConsolidation hands back
// after the writes complete.
type ConsolidationApplyResult struct {
	WorkspaceID  string         `json:"workspace_id"`
	TasksUpdated map[string]int `json:"tasks_updated"` // canonical → count
	VocabUpserts int            `json:"vocab_upserts"`
}

// ConsolidateStatusesDryRun computes a fast, deterministic, model-free
// merge proposal — case-folding + obvious-synonym clustering — plus
// the current vocabulary entries so the operator can decide. This is
// the dry-run mode of `task__consolidate_statuses`. The bundled
// `task-status-consolidator` worker template is the richer, model-
// driven alternative an operator can schedule for harder cases.
func (s *Service) ConsolidateStatusesDryRun(
	ctx context.Context, workspaceID string,
) (*StatusConsolidationProposal, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, errors.New("workspace_id is required")
	}
	counts, err := s.store.SelectDistinctTaskStatuses(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("select distinct statuses: %w", err)
	}
	vocab, err := s.store.ListTaskStatusVocab(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list vocab: %w", err)
	}
	prop := &StatusConsolidationProposal{
		WorkspaceID: workspaceID,
		Counts:      counts,
		Vocabulary:  summarizeVocab(vocab),
		Merges:      proposeStatusMerges(counts, vocab),
	}
	if len(prop.Merges) == 0 {
		prop.Note = "no heuristic merges found; consider scheduling the task-status-consolidator worker template for a model-driven clustering pass"
	}
	return prop, nil
}

// ApplyStatusConsolidation rewrites tasks.status from each merge.from
// to merge.to inside the workspace, then upserts the canonical
// status_text into task_status_vocabulary with the requested terminal
// flag. Returns per-canonical row counts so callers can audit.
func (s *Service) ApplyStatusConsolidation(
	ctx context.Context, workspaceID string, merges []StatusMerge, actorSession string,
) (*ConsolidationApplyResult, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, errors.New("workspace_id is required")
	}
	if len(merges) == 0 {
		return nil, errors.New("merges: at least one merge required")
	}
	result := &ConsolidationApplyResult{
		WorkspaceID:  workspaceID,
		TasksUpdated: map[string]int{},
	}
	for _, m := range merges {
		if m.From == "" || m.To == "" {
			return nil, fmt.Errorf("merge from/to must be non-empty (got %+v)", m)
		}
		if err := s.applyOneMerge(ctx, workspaceID, m, actorSession, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// applyOneMerge performs one StatusMerge — rewriting every task that
// carries m.From in the workspace to m.To and upserting m.To into the
// vocabulary. Extracted so ApplyStatusConsolidation stays under the
// 50-line per-function ceiling.
func (s *Service) applyOneMerge(
	ctx context.Context, workspaceID string, m StatusMerge, actorSession string, result *ConsolidationApplyResult,
) error {
	if m.From == m.To {
		// Vocabulary-only upsert — flag terminal without rewriting tasks.
		if err := s.upsertCanonicalVocab(ctx, workspaceID, m.To, m.Terminal); err != nil {
			return err
		}
		result.VocabUpserts++
		return nil
	}
	victims, err := s.store.ListTasks(ctx, store.TaskFilter{
		WorkspaceID: workspaceID,
		Status:      m.From,
	})
	if err != nil {
		return fmt.Errorf("list tasks with status %q: %w", m.From, err)
	}
	newStatus := m.To
	terminal := m.Terminal
	for i := range victims {
		patch := UpdatePatch{
			Status:             &newStatus,
			Terminal:           &terminal,
			UpdatedBySessionID: actorSession,
		}
		if _, err := s.Update(ctx, workspaceID, victims[i].ID, patch); err != nil {
			return fmt.Errorf("rewrite task %s: %w", victims[i].ID, err)
		}
		result.TasksUpdated[m.To]++
	}
	if err := s.upsertCanonicalVocab(ctx, workspaceID, m.To, m.Terminal); err != nil {
		return err
	}
	result.VocabUpserts++
	return nil
}

// upsertCanonicalVocab stamps a (workspace, status) entry into the
// status vocabulary with managed_by="skill" to mark it as
// consolidator-generated.
func (s *Service) upsertCanonicalVocab(
	ctx context.Context, workspaceID, status string, terminal bool,
) error {
	return s.store.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: workspaceID,
		StatusText:  status,
		IsTerminal:  terminal,
		ManagedBy:   "skill",
		UpdatedAt:   time.Now().UTC(),
	})
}

// RebindPeer rewrites every reference to oldPeerID across tasks +
// task_offers + workspace_peer_bindings to newPeerID, atomically.
// Returns the per-table row counts and a JSON-marshalable summary.
func (s *Service) RebindPeer(ctx context.Context, oldPeerID, newPeerID string) (map[string]int, error) {
	return s.store.RebindPeerInTasks(ctx, oldPeerID, newPeerID)
}

// ----------------------------------------------------------------------
// Heuristic clustering
// ----------------------------------------------------------------------

// canonicalAliases maps a normalised alias → canonical status name. The
// rules deliberately stay small and obvious so an operator reading the
// proposal can verify each row at a glance. Anything more ambitious
// belongs in the model-driven worker template.
var canonicalAliases = map[string]string{
	// doing cluster
	"in_progress": "doing", "in-progress": "doing", "inprogress": "doing",
	"wip": "doing", "started": "doing", "active": "doing", "ongoing": "doing",
	// blocked cluster
	"waiting": "blocked", "stuck": "blocked", "paused": "blocked", "on_hold": "blocked", "on-hold": "blocked",
	// review cluster
	"in_review": "review", "in-review": "review", "needs_review": "review", "needs-review": "review",
	// done cluster (terminal)
	"finished":  "done",
	"completed": "done",
	"complete":  "done",
	"closed":    "done",
	"resolved":  "done",
	"shipped":   "done",
	// cancelled cluster (terminal)
	"canceled":  "cancelled",
	"abandoned": "cancelled",
	"wontfix":   "cancelled",
	"wont-fix":  "cancelled",
	"won't_fix": "cancelled",
	"rejected":  "cancelled",
}

// terminalCanonicals is the set of canonical status names the heuristic
// flags as terminal in the resulting vocabulary. Operators can edit.
var terminalCanonicals = map[string]bool{
	"done":      true,
	"cancelled": true,
}

// proposeStatusMerges scans the workspace counts and returns one
// StatusMerge per alias that maps onto a canonical name. Existing
// vocab terminal flags win over the heuristic so an operator's manual
// edit isn't overwritten on the next consolidation pass.
func proposeStatusMerges(counts map[string]int, vocab []store.TaskStatusVocab) []StatusMerge {
	vocabTerminal := map[string]bool{}
	for _, v := range vocab {
		vocabTerminal[v.StatusText] = v.IsTerminal
	}
	var merges []StatusMerge
	for status := range counts {
		key := strings.ToLower(strings.TrimSpace(status))
		canon, ok := canonicalAliases[key]
		if !ok {
			continue
		}
		if status == canon {
			continue
		}
		terminal := terminalCanonicals[canon]
		if t, present := vocabTerminal[canon]; present {
			terminal = t
		}
		merges = append(merges, StatusMerge{From: status, To: canon, Terminal: terminal})
	}
	sort.Slice(merges, func(i, j int) bool {
		if merges[i].To != merges[j].To {
			return merges[i].To < merges[j].To
		}
		return merges[i].From < merges[j].From
	})
	return merges
}

func summarizeVocab(rows []store.TaskStatusVocab) []VocabSummary {
	out := make([]VocabSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, VocabSummary{
			StatusText: r.StatusText,
			IsTerminal: r.IsTerminal,
			ManagedBy:  r.ManagedBy,
		})
	}
	return out
}

// ParseConsolidationPlan unmarshals a JSON-encoded plan into the typed
// shape the apply path expects. The MCP handler accepts both an
// object (`{"merges":[...]}`) and a bare array (`[...]`) so an agent
// pasting partial JSON gets the friendliest result.
func ParseConsolidationPlan(raw json.RawMessage) ([]StatusMerge, error) {
	if len(raw) == 0 {
		return nil, errors.New("plan is required")
	}
	// Object form first.
	var obj struct {
		Merges []StatusMerge `json:"merges"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Merges != nil {
		return obj.Merges, nil
	}
	// Bare array fallback.
	var arr []StatusMerge
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, errors.New("plan must be an object with `merges:[...]` or a bare array of merges")
}
