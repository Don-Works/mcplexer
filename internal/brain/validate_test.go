package brain

import (
	"errors"
	"testing"
)

func TestValidateTask(t *testing.T) {
	vocab := []string{"open", "doing", "review", "done"}
	base := func() TaskFrontmatter {
		return TaskFrontmatter{
			ID: "01J7XYZ", Schema: SchemaTaskV1, Workspace: "ws",
			Title: "Title", Status: "open",
		}
	}

	cases := []struct {
		name      string
		fm        TaskFrontmatter
		filename  string
		vocab     []string
		wantErr   bool
		wantField string
	}{
		{name: "valid", fm: base(), filename: "01J7XYZ-title.md", vocab: vocab},
		{name: "valid no filename check", fm: base(), filename: "", vocab: vocab},
		{
			name: "missing id", fm: func() TaskFrontmatter { f := base(); f.ID = ""; return f }(),
			filename: "01J7XYZ-title.md", wantErr: true, wantField: "id",
		},
		{
			name: "missing title", fm: func() TaskFrontmatter { f := base(); f.Title = ""; return f }(),
			filename: "01J7XYZ-title.md", wantErr: true, wantField: "title",
		},
		{
			name: "missing status", fm: func() TaskFrontmatter { f := base(); f.Status = ""; return f }(),
			filename: "01J7XYZ-title.md", wantErr: true, wantField: "status",
		},
		{
			name: "id mismatch filename", fm: base(),
			filename: "01OTHER-title.md", wantErr: true, wantField: "id",
		},
		{
			name: "status not in vocab", fm: func() TaskFrontmatter { f := base(); f.Status = "frobnicated"; return f }(),
			filename: "01J7XYZ-title.md", vocab: vocab, wantErr: true, wantField: "status",
		},
		{
			name: "empty vocab skips status check", fm: func() TaskFrontmatter { f := base(); f.Status = "anything"; return f }(),
			filename: "01J7XYZ-title.md", vocab: nil,
		},
		{
			name: "unknown schema", fm: func() TaskFrontmatter { f := base(); f.Schema = "task/v99"; return f }(),
			filename: "01J7XYZ-title.md", wantErr: true, wantField: "schema",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTask(tc.fm, tc.filename, tc.vocab)
			checkValidation(t, err, tc.wantErr, tc.wantField)
		})
	}
}

func TestValidateMemory(t *testing.T) {
	base := func() MemoryFrontmatter {
		return MemoryFrontmatter{ID: "01M", Schema: SchemaMemoryV1, Kind: MemoryKindNote, Name: "deploy"}
	}
	fact := func() MemoryFrontmatter {
		f := base()
		f.Kind = MemoryKindFact
		f.TValidStart = tp("2026-06-03T00:00:00Z")
		return f
	}

	cases := []struct {
		name      string
		fm        MemoryFrontmatter
		filename  string
		wantErr   bool
		wantField string
	}{
		{name: "valid note", fm: base(), filename: "deploy.md"},
		{name: "valid fact", fm: fact(), filename: "deploy.md"},
		{name: "missing id", fm: func() MemoryFrontmatter { f := base(); f.ID = ""; return f }(), filename: "deploy.md", wantErr: true, wantField: "id"},
		{name: "missing name", fm: func() MemoryFrontmatter { f := base(); f.Name = ""; return f }(), filename: "deploy.md", wantErr: true, wantField: "name"},
		{name: "bad kind", fm: func() MemoryFrontmatter { f := base(); f.Kind = "thought"; return f }(), filename: "deploy.md", wantErr: true, wantField: "kind"},
		{name: "name mismatch filename", fm: base(), filename: "other.md", wantErr: true, wantField: "name"},
		{name: "fact missing t_valid_start", fm: func() MemoryFrontmatter { f := fact(); f.TValidStart = nil; return f }(), filename: "deploy.md", wantErr: true, wantField: "t_valid_start"},
		// Path-traversal hardening: names that would escape (or nest inside)
		// the flat memory dir are rejected on the inbound path.
		{name: "name with slash", fm: func() MemoryFrontmatter { f := base(); f.Name = "a/b"; return f }(), filename: "a-b.md", wantErr: true, wantField: "name"},
		{name: "name with backslash", fm: func() MemoryFrontmatter { f := base(); f.Name = `a\b`; return f }(), filename: "a-b.md", wantErr: true, wantField: "name"},
		{name: "name with dotdot", fm: func() MemoryFrontmatter { f := base(); f.Name = "../escape"; return f }(), filename: "escape.md", wantErr: true, wantField: "name"},
		{name: "slashy live regression name", fm: func() MemoryFrontmatter {
			f := base()
			f.Name = "Brain cross-machine sync: canonical remote = example/memory-repo (private)"
			return f
		}(), filename: "anything.md", wantErr: true, wantField: "name"},
		// Slugified filenames are the canonical outbound form — the stem
		// check accepts the slug (and the id fallback) alongside the raw name.
		{name: "slug stem accepted", fm: func() MemoryFrontmatter { f := base(); f.Name = "Deploy Hygiene"; return f }(), filename: "deploy-hygiene.md"},
		{name: "id stem accepted", fm: base(), filename: "01M.md"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMemory(tc.fm, tc.filename)
			checkValidation(t, err, tc.wantErr, tc.wantField)
		})
	}
}

func TestValidatePerson(t *testing.T) {
	base := func() PersonFrontmatter {
		return PersonFrontmatter{ID: "01P", Schema: SchemaPersonV1, Workspace: "crm", Name: "Ada Lovelace"}
	}

	cases := []struct {
		name      string
		fm        PersonFrontmatter
		filename  string
		wantErr   bool
		wantField string
	}{
		{name: "valid raw stem", fm: base(), filename: "Ada Lovelace.md"},
		{name: "valid slug stem", fm: base(), filename: "ada-lovelace.md"},
		{name: "valid id stem", fm: base(), filename: "01P.md"},
		{name: "no filename skips stem check", fm: base(), filename: ""},
		{name: "missing id", fm: func() PersonFrontmatter { f := base(); f.ID = ""; return f }(), filename: "ada-lovelace.md", wantErr: true, wantField: "id"},
		{name: "missing workspace", fm: func() PersonFrontmatter { f := base(); f.Workspace = ""; return f }(), filename: "ada-lovelace.md", wantErr: true, wantField: "workspace"},
		{name: "missing name", fm: func() PersonFrontmatter { f := base(); f.Name = ""; return f }(), filename: "ada-lovelace.md", wantErr: true, wantField: "name"},
		{name: "stem mismatch", fm: base(), filename: "grace-hopper.md", wantErr: true, wantField: "name"},
		{name: "name with slash", fm: func() PersonFrontmatter { f := base(); f.Name = "Ada/Lovelace"; return f }(), filename: "ada-lovelace.md", wantErr: true, wantField: "name"},
		{name: "name with backslash", fm: func() PersonFrontmatter { f := base(); f.Name = `Ada\Lovelace`; return f }(), filename: "ada-lovelace.md", wantErr: true, wantField: "name"},
		{name: "name with dotdot", fm: func() PersonFrontmatter { f := base(); f.Name = "../escape"; return f }(), filename: "escape.md", wantErr: true, wantField: "name"},
		{name: "unknown schema", fm: func() PersonFrontmatter { f := base(); f.Schema = "person/v99"; return f }(), filename: "ada-lovelace.md", wantErr: true, wantField: "schema"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePerson(tc.fm, tc.filename)
			checkValidation(t, err, tc.wantErr, tc.wantField)
		})
	}
}

func TestValidateWorkspace(t *testing.T) {
	valid := WorkspaceFrontmatter{ID: "ws1", Schema: SchemaWorkspaceV1, Name: "mcplexer"}
	if err := ValidateWorkspace(valid); err != nil {
		t.Fatalf("valid workspace: unexpected error %v", err)
	}
	if err := ValidateWorkspace(WorkspaceFrontmatter{Name: "x"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing id: want ErrValidation, got %v", err)
	}
}

func checkValidation(t *testing.T, err error, wantErr bool, wantField string) {
	t.Helper()
	if wantErr {
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrValidation) {
			t.Fatalf("error %v does not wrap ErrValidation", err)
		}
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("error %v is not a *ValidationError", err)
		}
		if wantField != "" && ve.Field != wantField {
			t.Fatalf("ValidationError.Field = %q, want %q", ve.Field, wantField)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
