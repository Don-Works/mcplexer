package brain

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrValidation is the sentinel wrapping every frontmatter validation
// failure. Callers test with errors.Is(err, ErrValidation); the concrete
// *ValidationError carries the field + reason for surfacing in
// brain_errors / the dashboard.
var ErrValidation = errors.New("brain: frontmatter validation failed")

// ValidationError describes a single failed validation check. It wraps
// ErrValidation so errors.Is(err, ErrValidation) holds.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: field %q: %s", ErrValidation.Error(), e.Field, e.Reason)
}

// Unwrap lets errors.Is(err, ErrValidation) succeed.
func (e *ValidationError) Unwrap() error { return ErrValidation }

// newValidationError is the single constructor so every validator
// produces identically-shaped, sentinel-wrapped errors.
func newValidationError(field, reason string) *ValidationError {
	return &ValidationError{Field: field, Reason: reason}
}

// ValidateTask enforces the Astro/Zod-style discipline on a task
// frontmatter: required fields present, id matches the filename ULID
// prefix, and status is in the workspace vocabulary (when a vocab is
// supplied — an empty vocab skips the status check so the validator is
// usable before the vocab is loaded).
//
// filename is the base name of the file (e.g.
// "01J7XYZ...-fix-scheduler.md"); only its prefix-before-the-first-dash
// is compared to fm.ID.
func ValidateTask(fm TaskFrontmatter, filename string, vocab []string) error {
	if strings.TrimSpace(fm.ID) == "" {
		return newValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(fm.Title) == "" {
		return newValidationError("title", "must not be empty")
	}
	if strings.TrimSpace(fm.Status) == "" {
		return newValidationError("status", "must not be empty")
	}
	if err := checkIDMatchesFilename(fm.ID, filename); err != nil {
		return err
	}
	if len(vocab) > 0 && !contains(vocab, fm.Status) {
		return newValidationError("status",
			fmt.Sprintf("%q is not in the workspace status vocabulary", fm.Status))
	}
	if fm.Schema != "" && fm.Schema != SchemaTaskV1 {
		return newValidationError("schema",
			fmt.Sprintf("unknown task schema %q (want %q)", fm.Schema, SchemaTaskV1))
	}
	return nil
}

// ValidateMemory enforces the memory frontmatter invariants: required
// fields, id-matches-filename (memory files are named <name>.md, so the
// name is what we match), and a recognised kind.
func ValidateMemory(fm MemoryFrontmatter, filename string) error {
	if strings.TrimSpace(fm.ID) == "" {
		return newValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(fm.Name) == "" {
		return newValidationError("name", "must not be empty")
	}
	if reason := unsafeNameReason(fm.Name); reason != "" {
		return newValidationError("name", reason)
	}
	switch fm.Kind {
	case MemoryKindNote, MemoryKindFact:
	default:
		return newValidationError("kind",
			fmt.Sprintf("%q is not a valid memory kind (want note|fact)", fm.Kind))
	}
	// Memory files are named after the memory name (the unique key for
	// facts), not the id — verify the filename stem identifies the name
	// (raw legacy stem, canonical slug, or the slug-empty id fallback).
	if stem := filenameStem(filename); stem != "" && !stemMatchesName(stem, fm.Name, fm.ID) {
		return newValidationError("name",
			fmt.Sprintf("name %q does not match filename stem %q", fm.Name, stem))
	}
	if fm.Schema != "" && fm.Schema != SchemaMemoryV1 {
		return newValidationError("schema",
			fmt.Sprintf("unknown memory schema %q (want %q)", fm.Schema, SchemaMemoryV1))
	}
	// A fact must carry a valid-start timestamp (bi-temporal invariant).
	if fm.Kind == MemoryKindFact && fm.TValidStart == nil {
		return newValidationError("t_valid_start", "facts require a t_valid_start")
	}
	return nil
}

// ValidatePerson enforces the CRM person frontmatter invariants: id +
// workspace + name present, name matches the filename stem (person files are
// named <name>.md, the per-workspace unique key), and a recognised schema.
func ValidatePerson(fm PersonFrontmatter, filename string) error {
	if strings.TrimSpace(fm.ID) == "" {
		return newValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(fm.Workspace) == "" {
		return newValidationError("workspace", "must not be empty")
	}
	if strings.TrimSpace(fm.Name) == "" {
		return newValidationError("name", "must not be empty")
	}
	if reason := unsafeNameReason(fm.Name); reason != "" {
		return newValidationError("name", reason)
	}
	if stem := filenameStem(filename); stem != "" && !stemMatchesName(stem, fm.Name, fm.ID) {
		return newValidationError("name",
			fmt.Sprintf("name %q does not match filename stem %q", fm.Name, stem))
	}
	if fm.Schema != "" && fm.Schema != SchemaPersonV1 {
		return newValidationError("schema",
			fmt.Sprintf("unknown person schema %q (want %q)", fm.Schema, SchemaPersonV1))
	}
	return nil
}

// ValidateWorkspace enforces the minimal workspace frontmatter
// invariants: id + name present, id matches the folder/file context, and
// a recognised schema.
func ValidateWorkspace(fm WorkspaceFrontmatter) error {
	if strings.TrimSpace(fm.ID) == "" {
		return newValidationError("id", "must not be empty")
	}
	if strings.TrimSpace(fm.Name) == "" {
		return newValidationError("name", "must not be empty")
	}
	if fm.Schema != "" && fm.Schema != SchemaWorkspaceV1 {
		return newValidationError("schema",
			fmt.Sprintf("unknown workspace schema %q (want %q)", fm.Schema, SchemaWorkspaceV1))
	}
	return nil
}

// checkIDMatchesFilename verifies the task id equals the filename's ULID
// prefix (everything before the first '-' in the base name, with the .md
// extension stripped). Empty filename skips the check (caller validating
// a struct not yet bound to a path).
func checkIDMatchesFilename(id, filename string) error {
	if filename == "" {
		return nil
	}
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	prefix := base
	if i := strings.IndexByte(base, '-'); i >= 0 {
		prefix = base[:i]
	}
	if prefix != id {
		return newValidationError("id",
			fmt.Sprintf("id %q does not match filename prefix %q", id, prefix))
	}
	return nil
}

// unsafeNameReason reports why a record name is unsafe to materialise on
// disk, or "" when it is safe. Path separators and parent-dir traversal
// sequences are rejected: the slugified outbound path defends writes, and
// this rejects the inbound (file → store) direction so a hostile
// frontmatter name can never round-trip into a path component. A live
// memory named "...remote = example/memory-repo (private)" once
// created an unindexable subdirectory this way.
func unsafeNameReason(name string) string {
	switch {
	case strings.ContainsAny(name, `/\`):
		return `must not contain path separators ('/' or '\')`
	case strings.Contains(name, ".."):
		return "must not contain '..'"
	}
	return ""
}

// stemMatchesName reports whether a filename stem identifies a name-keyed
// record (memory/person): the raw name (legacy files written before
// slugification), its slug (the canonical form), or the record id (the
// slug-empty fallback for all-symbol / non-Latin names).
func stemMatchesName(stem, name, id string) bool {
	return stem == name || stem == slugify(name) || (id != "" && stem == id)
}

// filenameStem returns the base name with its extension stripped.
func filenameStem(filename string) string {
	if filename == "" {
		return ""
	}
	base := filepath.Base(filename)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// contains reports whether s is in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
