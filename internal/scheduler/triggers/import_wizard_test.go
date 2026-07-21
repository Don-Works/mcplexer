package triggers

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newTestWizard builds a wizard pointed at tmpdir-rooted launchd and
// systemd config directories with `crontab -l` stubbed.
func newTestWizard(t *testing.T, crontabOut string, crontabErr error) *ImportWizard {
	t.Helper()
	home := t.TempDir()
	w := &ImportWizard{
		home:            home,
		launchAgentsDir: filepath.Join(home, "LaunchAgents"),
		systemdUserDir:  filepath.Join(home, "systemd-user"),
		run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(crontabOut), crontabErr
		},
	}
	return w
}

func TestFromUserCrontabSkipsBlanksAndComments(t *testing.T) {
	out := `# comment

0 3 * * * /usr/local/bin/foo --flag
*/5 * * * * /opt/bin/bar
malformed
`
	w := newTestWizard(t, out, nil)
	cands, err := w.FromUserCrontab(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d (%+v)", len(cands), cands)
	}
	if cands[0].Job.Spec != "0 3 * * *" || cands[0].Job.Command != "/usr/local/bin/foo" {
		t.Errorf("cand0 = %+v", cands[0])
	}
	if cands[0].Source.Line != 3 {
		t.Errorf("cand0 line = %d, want 3", cands[0].Source.Line)
	}
	if cands[0].Job.ArgsJSON != `["--flag"]` {
		t.Errorf("cand0 args = %q", cands[0].Job.ArgsJSON)
	}
	if cands[1].Job.Spec != "*/5 * * * *" {
		t.Errorf("cand1 spec = %q", cands[1].Job.Spec)
	}
}

// TestFromUserCrontabNoCrontabReturnsSentinel verifies that the
// "crontab not installed" code path maps to ErrNoCrontab. We use
// exec.ErrNotFound directly so the wizard's errors.Is check fires.
func TestFromUserCrontabNoCrontabReturnsSentinel(t *testing.T) {
	w := newTestWizard(t, "", nil)
	w.run = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, exec.ErrNotFound
	}
	_, err := w.FromUserCrontab(context.Background())
	if !errors.Is(err, ErrNoCrontab) {
		t.Errorf("err = %v, want ErrNoCrontab", err)
	}
}

func TestFromLaunchdUserParsesPlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("FromLaunchdUser is macOS-only")
	}
	w := newTestWizard(t, "", nil)
	if err := os.MkdirAll(w.launchAgentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0"?>
<plist version="1.0">
<dict>
  <key>Label</key><string>com.test.foo</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/foo</string>
    <string>--once</string>
  </array>
  <key>StartInterval</key><integer>300</integer>
</dict>
</plist>`
	if err := os.WriteFile(filepath.Join(w.launchAgentsDir, "foo.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	cands, err := w.FromLaunchdUser(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 cand, got %d", len(cands))
	}
	c := cands[0]
	if c.Job.Command != "/usr/local/bin/foo" {
		t.Errorf("cmd = %q", c.Job.Command)
	}
	if c.Job.Spec != "5m0s" {
		t.Errorf("spec = %q, want 5m0s", c.Job.Spec)
	}
	if c.Source.Path != "com.test.foo" {
		t.Errorf("label = %q", c.Source.Path)
	}
}

func TestFromLaunchdUserCalendarInterval(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("FromLaunchdUser is macOS-only")
	}
	w := newTestWizard(t, "", nil)
	_ = os.MkdirAll(w.launchAgentsDir, 0o755)
	plist := `<?xml version="1.0"?>
<plist version="1.0">
<dict>
  <key>Label</key><string>com.test.cal</string>
  <key>ProgramArguments</key>
  <array><string>/bin/echo</string></array>
  <key>StartCalendarInterval</key>
  <dict>
    <key>Hour</key><integer>3</integer>
    <key>Minute</key><integer>0</integer>
  </dict>
</dict>
</plist>`
	_ = os.WriteFile(filepath.Join(w.launchAgentsDir, "cal.plist"), []byte(plist), 0o644)
	cands, err := w.FromLaunchdUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("got %d cands", len(cands))
	}
	if cands[0].Job.Spec != "0 3 * * *" {
		t.Errorf("spec = %q, want '0 3 * * *'", cands[0].Job.Spec)
	}
}

func TestFromLaunchdUserUnsupportedOS(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this test verifies non-darwin behaviour")
	}
	w := newTestWizard(t, "", nil)
	_, err := w.FromLaunchdUser(context.Background())
	if !errors.Is(err, ErrUnsupportedOS) {
		t.Errorf("err = %v, want ErrUnsupportedOS", err)
	}
}

func TestFromSystemdUserParsesTimerAndService(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("FromSystemdUser is Linux-only")
	}
	w := newTestWizard(t, "", nil)
	if err := os.MkdirAll(w.systemdUserDir, 0o755); err != nil {
		t.Fatal(err)
	}
	timer := `[Unit]
Description=Test timer
[Timer]
OnUnitActiveSec=1h
Unit=mytask.service
[Install]
WantedBy=timers.target
`
	service := `[Unit]
Description=Test svc
[Service]
ExecStart=/usr/bin/mytask --quiet
`
	_ = os.WriteFile(filepath.Join(w.systemdUserDir, "mytask.timer"), []byte(timer), 0o644)
	_ = os.WriteFile(filepath.Join(w.systemdUserDir, "mytask.service"), []byte(service), 0o644)
	cands, err := w.FromSystemdUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("got %d cands", len(cands))
	}
	c := cands[0]
	if c.Job.Spec != "1h" {
		t.Errorf("spec = %q", c.Job.Spec)
	}
	if c.Job.Command != "/usr/bin/mytask" {
		t.Errorf("cmd = %q", c.Job.Command)
	}
	if c.Job.ArgsJSON != `["--quiet"]` {
		t.Errorf("args = %q", c.Job.ArgsJSON)
	}
}

func TestFromSystemdUserOnCalendarWarning(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("FromSystemdUser is Linux-only")
	}
	w := newTestWizard(t, "", nil)
	_ = os.MkdirAll(w.systemdUserDir, 0o755)
	timer := `[Timer]
OnCalendar=Mon..Fri 09:00
`
	_ = os.WriteFile(filepath.Join(w.systemdUserDir, "weekday.timer"), []byte(timer), 0o644)
	cands, err := w.FromSystemdUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Warning == "" {
		t.Fatalf("expected one warning candidate, got %+v", cands)
	}
	if !strings.Contains(cands[0].Warning, "Mon..Fri 09:00") {
		t.Errorf("warning = %q", cands[0].Warning)
	}
}

func TestFromSystemdUserUnsupportedOS(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("verifies non-linux behaviour")
	}
	w := newTestWizard(t, "", nil)
	_, err := w.FromSystemdUser(context.Background())
	if !errors.Is(err, ErrUnsupportedOS) {
		t.Errorf("err = %v, want ErrUnsupportedOS", err)
	}
}

func TestNormaliseSystemdDuration(t *testing.T) {
	cases := map[string]string{
		"1h":    "1h",
		"30min": "30m",
		"5 sec": "5s",
		"2hr":   "2h",
	}
	for in, want := range cases {
		if got := normaliseSystemdDuration(in); got != want {
			t.Errorf("normaliseSystemdDuration(%q)=%q want %q", in, got, want)
		}
	}
}

func TestMarshalArgvEscapes(t *testing.T) {
	got := marshalArgv([]string{`a"b\c`, "d"})
	want := `["a\"b\\c","d"]`
	if got != want {
		t.Errorf("marshalArgv mismatch:\n got: %s\nwant: %s", got, want)
	}
	if marshalArgv(nil) != "[]" {
		t.Errorf("empty argv should be []")
	}
}
