package harnesssync_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/don-works/mcplexer/internal/harnesssync"
)

func TestDetectAccretion(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, home string)
		want  harnesssync.AccretionReport
	}{
		{
			name:  "clean machine reads empty",
			setup: func(t *testing.T, home string) {},
			want:  harnesssync.AccretionReport{},
		},
		{
			name: "using-mcplexer alone is clean",
			setup: func(t *testing.T, home string) {
				writeSkillDir(t, home, "using-mcplexer")
			},
			want: harnesssync.AccretionReport{},
		},
		{
			name: "extra skill dirs and commands are reported sorted",
			setup: func(t *testing.T, home string) {
				writeSkillDir(t, home, "using-mcplexer")
				writeSkillDir(t, home, "zeta-skill")
				writeSkillDir(t, home, "alpha-skill")
				writeCommandFile(t, home, "todo.md")
				writeCommandFile(t, home, "agent-mesh.md")
			},
			want: harnesssync.AccretionReport{
				ExtraSkills:   []string{"alpha-skill", "zeta-skill"},
				ExtraCommands: []string{"agent-mesh.md", "todo.md"},
			},
		},
		{
			name: "hidden entries, bare dirs and non-md files are ignored",
			setup: func(t *testing.T, home string) {
				skills := filepath.Join(home, ".claude", "skills")
				if err := os.MkdirAll(filepath.Join(skills, ".migrated"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(skills, "no-skill-md"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeCommandFile(t, home, ".hidden.md")
				cmds := filepath.Join(home, ".claude", "commands")
				if err := os.WriteFile(filepath.Join(cmds, "notes.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: harnesssync.AccretionReport{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			tc.setup(t, home)
			got := harnesssync.DetectAccretion(home)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
			if got.Empty() != tc.want.Empty() {
				t.Errorf("Empty() = %v, want %v", got.Empty(), tc.want.Empty())
			}
		})
	}
}

func TestDetectHarnessAccretion_OpenCodeAndCodex(t *testing.T) {
	home := t.TempDir()
	writeSkillDirUnder(t, filepath.Join(home, ".config", "opencode", "skills"), "opencode-local")
	writeSkillDirUnder(t, filepath.Join(home, ".config", "opencode", "skills"), "using-mcplexer")
	writeSkillDirUnder(t, filepath.Join(home, ".claude", "skills"), "using-mcplexer")
	writeSkillDirUnder(t, filepath.Join(home, ".claude", "skills"), "claude-shared")
	writeSkillDirUnder(t, filepath.Join(home, ".agents", "skills"), "agents-shared")
	writeCommandFileUnder(t, filepath.Join(home, ".config", "opencode", "commands"), "slash.md")
	writeCommandFileUnder(t, filepath.Join(home, ".config", "opencode", "command"), "singular.md")
	writeSkillDirUnder(t, filepath.Join(home, ".codex", "skills", ".system"), "system-skill")
	writeSkillDirUnder(t, filepath.Join(home, ".codex", "skills"), "codex-local")

	openCode := harnesssync.DetectHarnessAccretion(home, harnesssync.OpenCode)
	wantOpenCode := harnesssync.AccretionReport{
		ExtraSkills: []string{
			"agents-compatible/agents-shared",
			"claude-compatible/claude-shared",
			"opencode/opencode-local",
		},
		ExtraCommands: []string{
			"opencode/command/singular.md",
			"opencode/commands/slash.md",
		},
	}
	if !reflect.DeepEqual(openCode, wantOpenCode) {
		t.Errorf("opencode accretion = %+v, want %+v", openCode, wantOpenCode)
	}

	codex := harnesssync.DetectHarnessAccretion(home, harnesssync.Codex)
	wantCodex := harnesssync.AccretionReport{ExtraSkills: []string{"codex/codex-local"}}
	if !reflect.DeepEqual(codex, wantCodex) {
		t.Errorf("codex accretion = %+v, want %+v", codex, wantCodex)
	}
}

func TestRecheckPopulatesHarnessAccretion(t *testing.T) {
	home := t.TempDir()
	writeSkillDir(t, home, "rogue-skill")

	st, err := harnesssync.Recheck(home, harnesssync.Claude, 1)
	if err != nil {
		t.Fatalf("recheck claude: %v", err)
	}
	if st.Accretion == nil || len(st.Accretion.ExtraSkills) != 1 || st.Accretion.ExtraSkills[0] != "rogue-skill" {
		t.Errorf("claude accretion = %+v, want rogue-skill", st.Accretion)
	}

	st, err = harnesssync.Recheck(home, harnesssync.Codex, 1)
	if err != nil {
		t.Fatalf("recheck codex: %v", err)
	}
	if st.Accretion != nil {
		t.Errorf("codex accretion = %+v, want nil", st.Accretion)
	}

	writeSkillDirUnder(t, filepath.Join(home, ".codex", "skills"), "codex-rogue")
	st, err = harnesssync.Recheck(home, harnesssync.Codex, 1)
	if err != nil {
		t.Fatalf("recheck codex with accretion: %v", err)
	}
	if st.Accretion == nil || len(st.Accretion.ExtraSkills) != 1 || st.Accretion.ExtraSkills[0] != "codex/codex-rogue" {
		t.Errorf("codex accretion = %+v, want codex/codex-rogue", st.Accretion)
	}
}

func writeSkillDir(t *testing.T, home, name string) {
	t.Helper()
	writeSkillDirUnder(t, filepath.Join(home, ".claude", "skills"), name)
}

func writeSkillDirUnder(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: test skill\n---\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeCommandFile(t *testing.T, home, name string) {
	t.Helper()
	writeCommandFileUnder(t, filepath.Join(home, ".claude", "commands"), name)
}

func writeCommandFileUnder(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("---\ndescription: test\n---\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
