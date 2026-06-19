// handler_tasks_shape.go — response-shape normalisers for task__* MCP
// surface. Goal: every collection field in a task__list / task__get /
// task__update / task__create response marshals to `[]` (or `{}`) when
// empty, never `null`.
//
// Why this matters: for an LLM reading the response, `null` is
// ambiguous — is the field absent? did the query fail? did the
// workspace not exist? `[]` is unambiguous: "the query succeeded; zero
// rows." Each round-trip the model spends disambiguating empty-vs-
// missing burns the caller's budget.
//
// Source: task 01KSGHS25GM0BG8K6T7EEFHSDN (UX: tasks: null vs []).
package gateway

import (
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// nonNilTaskRows guarantees a non-nil []store.Task. Empty input
// returns an empty (length-0) slice so the JSON marshaller emits `[]`.
//
//nolint:unused // retained for task__list response-shape normalization.
func nonNilTaskRows(rows []store.Task) []store.Task {
	if rows == nil {
		return []store.Task{}
	}
	return rows
}

// nonNilTaskNotes guarantees a non-nil []store.TaskNote.
func nonNilTaskNotes(notes []store.TaskNote) []store.TaskNote {
	if notes == nil {
		return []store.TaskNote{}
	}
	return notes
}

// nonNilStrings guarantees a non-nil []string. Used for composed_by,
// composes, and the known_statuses / known_tags / known_meta_keys
// envelope fields.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// nonNilKnownAssignees guarantees a non-nil []tasks.KnownAssignee for
// the discovery envelope's known_assignees field.
func nonNilKnownAssignees(a []tasks.KnownAssignee) []tasks.KnownAssignee {
	if a == nil {
		return []tasks.KnownAssignee{}
	}
	return a
}
