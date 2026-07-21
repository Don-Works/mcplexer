// mime_test.go — coverage for the attachpolicy default MIME denylist.
// Table-driven: each row is one (mime, filename) pair plus the expected
// outcome. The two-list design (MIME OR extension) means both lanes
// need positive + negative coverage.
package attachpolicy

import "testing"

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name     string
		mime     string
		filename string
		wantDeny bool
		wantCode string
	}{
		// Allowed shapes — common docs / images / text / archives.
		{name: "plain text passes", mime: "text/plain", filename: "notes.txt"},
		{name: "PDF passes", mime: "application/pdf", filename: "report.pdf"},
		{name: "JSON passes", mime: "application/json", filename: "payload.json"},
		{name: "PNG passes", mime: "image/png", filename: "screenshot.png"},
		{name: "JPEG passes", mime: "image/jpeg", filename: "photo.jpg"},
		{name: "CSV passes", mime: "text/csv", filename: "export.csv"},
		{name: "ZIP passes", mime: "application/zip", filename: "bundle.zip"},
		{name: "octet-stream + ordinary ext passes", mime: "application/octet-stream", filename: "data.bin"},
		{name: "empty mime + empty ext passes", mime: "", filename: ""},

		// Direct MIME match cases.
		{name: "Mach-O denied", mime: "application/x-mach-binary", filename: "tool", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "ELF denied", mime: "application/x-executable", filename: "tool", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "Windows PE denied", mime: "application/vnd.microsoft.portable-executable", filename: "setup", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "POSIX shell denied", mime: "application/x-sh", filename: "deploy", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "shellscript denied", mime: "application/x-shellscript", filename: "deploy", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "APK denied", mime: "application/vnd.android.package-archive", filename: "app", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "JAR denied", mime: "application/java-archive", filename: "tool", wantDeny: true, wantCode: "attachment_mime_denied"},

		// MIME-param normalisation — "; charset=utf-8" must not bypass.
		{name: "shell + charset param denied", mime: "application/x-sh; charset=utf-8", filename: "deploy", wantDeny: true, wantCode: "attachment_mime_denied"},
		// Case-insensitive MIME.
		{name: "uppercase mime denied", mime: "APPLICATION/X-MACH-BINARY", filename: "tool", wantDeny: true, wantCode: "attachment_mime_denied"},

		// Extension fallback — opaque MIME but obviously executable.
		{name: "octet-stream + .exe denied", mime: "application/octet-stream", filename: "setup.exe", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "octet-stream + .sh denied", mime: "application/octet-stream", filename: "deploy.sh", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "octet-stream + .ps1 denied", mime: "application/octet-stream", filename: "evil.ps1", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "no mime + .apk denied", mime: "", filename: "app.apk", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "no mime + .dmg denied", mime: "", filename: "installer.dmg", wantDeny: true, wantCode: "attachment_mime_denied"},
		{name: "no mime + uppercase .EXE denied", mime: "", filename: "PAYLOAD.EXE", wantDeny: true, wantCode: "attachment_mime_denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.mime, tc.filename)
			if got.Denied != tc.wantDeny {
				t.Errorf("Evaluate(%q, %q) Denied=%v, want %v (reason=%q)",
					tc.mime, tc.filename, got.Denied, tc.wantDeny, got.Reason)
			}
			if tc.wantDeny && got.Code != tc.wantCode {
				t.Errorf("Evaluate(%q, %q) Code=%q, want %q",
					tc.mime, tc.filename, got.Code, tc.wantCode)
			}
			if tc.wantDeny && got.Reason == "" {
				t.Errorf("Evaluate(%q, %q): Denied=true but Reason is empty",
					tc.mime, tc.filename)
			}
		})
	}
}

func TestEvaluateReasonExplainsDenial(t *testing.T) {
	// A user-visible reason — the REST handler echoes this back through
	// scopes.Denial.Message, so it should describe the rejection so the
	// uploader knows why.
	got := Evaluate("application/x-sh", "deploy")
	if !got.Denied {
		t.Fatal("expected denial for application/x-sh")
	}
	if got.Reason == "" {
		t.Error("Reason should explain the denial")
	}
}
