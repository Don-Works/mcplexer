//go:build darwin

package scheduler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestLaunchdDriverInstallUninstall exercises the full install → print
// → uninstall cycle against a real launchctl. Skips when launchctl is
// missing (CI sandbox) or when HOME isn't writable.
func TestLaunchdDriverInstallUninstall(t *testing.T) {
	if _, err := os.Stat("/bin/launchctl"); err != nil {
		t.Skip("launchctl not present")
	}
	tmp := t.TempDir()
	d := &launchdDriver{
		plistDir:     tmp,
		binaryPath:   "/usr/bin/true", // safe no-op
		launchctlBin: "/bin/launchctl",
	}
	if !d.Available() {
		t.Skip("launchd driver reports unavailable")
	}
	job := store.ScheduledJob{
		ID:      "test-job-mcplexer-unit",
		Name:    "test-job",
		Kind:    KindInterval,
		Spec:    "300s",
		Command: "/usr/bin/true",
		Enabled: true,
	}
	ctx := context.Background()
	label, err := d.Install(ctx, job)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.HasPrefix(label, launchdLabelPrefix) {
		t.Errorf("label %q missing prefix", label)
	}
	defer func() {
		if uerr := d.Uninstall(ctx, label); uerr != nil {
			t.Errorf("cleanup uninstall: %v", uerr)
		}
	}()

	plistPath := filepath.Join(tmp, label+".plist")
	if _, err := os.Stat(plistPath); err != nil {
		t.Errorf("plist not on disk: %v", err)
	}

	// Idempotent — second install should succeed and return the same
	// label.
	label2, err := d.Install(ctx, job)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if label2 != label {
		t.Errorf("label changed on re-install: %q -> %q", label, label2)
	}

	// launchctl print should show the agent (use list which is more
	// portable across macOS versions).
	out, err := exec.CommandContext(ctx, "/bin/launchctl", "list").Output()
	if err != nil {
		t.Skipf("launchctl list failed (sandboxed?): %v", err)
	}
	if !strings.Contains(string(out), label) {
		t.Errorf("launchctl list missing %q\noutput:\n%s", label, out)
	}

	if err := d.Uninstall(ctx, label); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Errorf("plist still on disk after uninstall: %v", err)
	}
	out, _ = exec.CommandContext(ctx, "/bin/launchctl", "list").Output()
	if strings.Contains(string(out), label) {
		t.Errorf("launchctl list still shows %q after uninstall", label)
	}
}

// TestLaunchdRenderPlistShape spot-checks the XML we emit.
func TestLaunchdRenderPlistShape(t *testing.T) {
	d := &launchdDriver{binaryPath: "/opt/mcplexer/bin/mcplexer"}
	body, err := d.renderPlist("com.mcplexer.scheduled.demo", store.ScheduledJob{
		ID: "demo", Kind: KindInterval, Spec: "60s",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"<key>Label</key>",
		"com.mcplexer.scheduled.demo",
		"<string>run-job</string>",
		"<string>demo</string>",
		"<key>StartInterval</key>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plist missing %q\n%s", want, body)
		}
	}
}

// TestLaunchdRenderPlistCron exercises the cron→StartCalendarInterval
// translation that prior to this commit silently fell through to a
// 60-second interval (any promoted cron job firing at the wrong time).
// Three representative specs cover: simple literal hour, every-5-min
// step, and pure stars-only (every minute).
func TestLaunchdRenderPlistCron(t *testing.T) {
	d := &launchdDriver{binaryPath: "/opt/mcplexer/bin/mcplexer"}
	cases := []struct {
		name     string
		spec     string
		mustHave []string
		mustNot  []string
	}{
		{
			name: "every day at 09:00",
			spec: "0 9 * * *",
			mustHave: []string{
				"<key>StartCalendarInterval</key>",
				"<key>Minute</key><integer>0</integer>",
				"<key>Hour</key><integer>9</integer>",
			},
			mustNot: []string{"<key>StartInterval</key>"},
		},
		{
			name: "every 5 minutes",
			spec: "*/5 * * * *",
			mustHave: []string{
				"<key>StartCalendarInterval</key>",
				"<key>Minute</key><integer>0</integer>",
				"<key>Minute</key><integer>5</integer>",
				"<key>Minute</key><integer>55</integer>",
			},
			mustNot: []string{"<key>StartInterval</key>", "<key>Hour</key>"},
		},
		{
			name: "every minute",
			spec: "* * * * *",
			mustHave: []string{
				"<key>StartCalendarInterval</key>",
			},
			mustNot: []string{"<key>StartInterval</key>", "<key>Minute</key>", "<key>Hour</key>"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := d.renderPlist("com.mcplexer.scheduled.cron", store.ScheduledJob{
				ID: "cron", Kind: KindCron, Spec: tc.spec,
			})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			for _, want := range tc.mustHave {
				if !strings.Contains(body, want) {
					t.Errorf("plist missing %q\n%s", want, body)
				}
			}
			for _, nope := range tc.mustNot {
				if strings.Contains(body, nope) {
					t.Errorf("plist must not contain %q\n%s", nope, body)
				}
			}
		})
	}
}
