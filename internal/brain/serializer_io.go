package brain

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// atomicWrite writes data to a temp file in the same dir then renames it
// over the target, so no watcher/editor ever sees a half-written file
// (SPEC §6 outbound step 3).
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".brain-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// oldestFirst returns task notes in created-at ascending order (the
// service's ListTaskNotes returns DESC). A copy is returned so the
// caller's slice is untouched.
func oldestFirst(notes []store.TaskNote) []store.TaskNote {
	out := make([]store.TaskNote, len(notes))
	for i, n := range notes {
		out[len(notes)-1-i] = n
	}
	return out
}

// recordStem derives the on-disk filename stem for a name-keyed record
// (memory/person): the slugified name, falling back to the record id when
// the name slugs to nothing (e.g. an all-symbol or non-Latin name). The
// slug is load-bearing security, not just cosmetics: names are free-form
// text, and a raw join would let a name like "a/b" or "../escape" create
// subdirectories or traverse out of the flat record dir.
func recordStem(name, id string) string {
	if s := slugify(name); s != "" {
		return s
	}
	return id
}

// slugify turns a title into a lowercase kebab-case slug bounded by
// slugMaxLen. Non-alphanumeric runs collapse to a single dash.
func slugify(title string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
		if b.Len() >= slugMaxLen {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}
