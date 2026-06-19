package brain

import (
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// SplitBodyNotes splits a task .md body into its prose description and
// the trailing `## Notes` section (if any). The returned description is
// what lands in store.Task.Description; the notes section is informational
// (the canonical notes live in task_notes rows, re-rendered on serialize).
//
// The split is on the first line that is exactly the notesHeading. Both
// halves are right-trimmed of trailing whitespace/newlines so the
// description round-trips byte-stably (the serializer re-attaches the
// notes section).
func SplitBodyNotes(body string) (description, notes string) {
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		if strings.TrimRight(ln, " \t") == notesHeading {
			description = strings.TrimRight(strings.Join(lines[:i], "\n"), "\n ")
			notes = strings.TrimRight(strings.Join(lines[i:], "\n"), "\n ")
			return description, notes
		}
	}
	return strings.TrimRight(body, "\n "), ""
}

// renderNotes builds the `## Notes` body section from append-only
// task_notes rows (oldest first). Returns "" when there are no notes so
// the serialized body stays clean for note-less tasks. Each note renders
// as a list item with an ISO date + author-kind prefix, matching the
// SPEC §5 example shape.
func renderNotes(notes []store.TaskNote) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(notesHeading)
	b.WriteByte('\n')
	for _, n := range notes {
		date := n.CreatedAt.UTC().Format("2006-01-02")
		author := n.AuthorKind
		if author == "" {
			author = "agent"
		}
		// One trimmed line per note; multi-line bodies are indented under
		// the bullet so the section stays valid Markdown.
		body := strings.TrimRight(n.Body, "\n ")
		body = strings.ReplaceAll(body, "\n", "\n  ")
		fmt.Fprintf(&b, "- %s (%s): %s\n", date, author, body)
	}
	return strings.TrimRight(b.String(), "\n")
}

// composeTaskBody assembles the full serialized body for a task: the
// prose description, then a blank line, then the rendered notes section
// (when any notes exist). Either part may be empty.
func composeTaskBody(description string, notes []store.TaskNote) string {
	desc := strings.TrimRight(description, "\n ")
	notesBlock := renderNotes(notes)
	switch {
	case desc == "" && notesBlock == "":
		return ""
	case notesBlock == "":
		return desc
	case desc == "":
		return notesBlock
	default:
		return desc + "\n\n" + notesBlock
	}
}

// taskFileMtime is a tiny helper to convert a time to unix seconds for
// the index_files row; kept here so the indexer + serializer share one
// definition (avoids a 0-vs-Unix(0) ambiguity).
func taskFileMtime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
