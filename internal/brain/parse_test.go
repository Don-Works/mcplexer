package brain

import "testing"

func TestParseTask_MalformedYAML(t *testing.T) {
	data := []byte("---\nid: [unclosed\nstatus: open\n---\nbody\n")
	if _, _, err := ParseTask(data); err == nil {
		t.Fatal("ParseTask(malformed): expected error, got nil")
	}
}

func TestParseTask_MissingFrontmatter(t *testing.T) {
	data := []byte("just a body, no frontmatter fence\n")
	if _, _, err := ParseTask(data); err == nil {
		t.Fatal("ParseTask(no frontmatter): expected error, got nil")
	}
}

func TestParseTask_EmptyInput(t *testing.T) {
	if _, _, err := ParseTask(nil); err == nil {
		t.Fatal("ParseTask(nil): expected error, got nil")
	}
}

func TestParseMemory_BodyTrimmed(t *testing.T) {
	data := []byte("---\nid: 01M\nschema: memory/v1\nkind: note\nname: x\ncreated_at: 2026-06-03T10:00:00Z\nupdated_at: 2026-06-03T10:00:00Z\npinned: false\n---\n\n\nThe body.\n")
	fm, body, err := ParseMemory(data)
	if err != nil {
		t.Fatalf("ParseMemory: %v", err)
	}
	if fm.Name != "x" || fm.Kind != "note" {
		t.Fatalf("frontmatter mismatch: %+v", fm)
	}
	if body != "The body.\n" {
		t.Fatalf("body leading newlines not trimmed: %q", body)
	}
}
