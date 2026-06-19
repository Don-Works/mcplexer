package writeclass

import "testing"

// TestIsWriteClass exercises the full taxonomy: snake_case prefixes,
// camelCase prefixes, dangerous substrings, and the read-class names
// that must stay read. Each case is documented inline so the SECURITY
// contract is visible.
func TestIsWriteClass(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// --- snake_case prefix (the legacy path) ---
		{"github__create_issue is write", "github__create_issue", true},
		{"github__update_issue is write", "github__update_issue", true},
		{"github__delete_issue is write", "github__delete_issue", true},
		{"linear__update_status is write", "linear__update_status", true},
		{"mesh__send is write (bare verb)", "mesh__send", true},
		{"data__harvest_harness_context is write", "data__harvest_harness_context", true},
		{"github__list_issues is read", "github__list_issues", false},
		{"mesh__receive is read", "mesh__receive", false},
		{"notion__databases.query is read", "notion__databases.query", false},

		// --- camelCase prefix (the gap this fix closes) ---
		{"github__createIssue is write", "github__createIssue", true},
		{"linear__updateUser is write", "linear__updateUser", true},
		{"slack__sendMessage is write", "slack__sendMessage", true},
		{"github__deleteRepo is write", "github__deleteRepo", true},
		{"linear__upsertTicket is write", "linear__upsertTicket", true},
		{"github__patchFile is write", "github__patchFile", true},
		{"db__truncateTable is write", "db__truncateTable", true},
		{"github__publishRelease is write", "github__publishRelease", true},
		{"github__overwriteFile is write", "github__overwriteFile", true},
		{"slack__editMessage is write", "slack__editMessage", true},

		// --- camelCase false positives we must NOT trigger ---
		// "setup" starts with "set" but the next char is lowercase 'u',
		// not uppercase — must NOT classify as write.
		{"github__setupRepo is read (no capital after verb)", "github__setupRepo", false},
		// "remover" / "creator" likewise — the next char must be uppercase
		// to look like a camelCase boundary.
		{"github__remover is read", "github__remover", false},
		// "setup_thing" stays read (legacy assertion).
		{"setup_thing is read", "setup_thing", false},
		{"set bare is write", "set", true},
		// "post" bare-word is write (verb match).
		{"post is write (bare verb)", "post", true},

		// --- danger substrings (substring match, not prefix) ---
		{"info_purge_logs is write (substring)", "info_purge_logs", true},
		{"prefix_drop_table_users is write", "prefix_drop_table_users", true},
		{"audit_delete_all is write", "audit_delete_all", true},
		{"git__force_push is write", "git__force_push", true},
		{"db__truncate_table is write", "db__truncate_table", true},

		// --- substring guards must not over-trigger ---
		// "purchase_order" must NOT trip on "purge" — guarded by the
		// "purge_" / "_purge" qualifier in dangerSubstrings.
		{"purchase_order is read (no purge match)", "purchase_order", false},
		// "drop_off" starts with the write verb `drop` — fails closed
		// to write-class. The operator pays a one-time approval prompt
		// rather than risking a false negative on a real drop.
		{"drop_off is write (drop_ verb prefix)", "drop_off", true},

		// --- namespaced read tools stay read ---
		{"info_send_logs is write (send_ prefix)", "info_send_logs", true},
		{"github__get_issue is read", "github__get_issue", false},
		{"linear__search_issues is read", "linear__search_issues", false},

		// --- empty / pathological ---
		{"empty string is read", "", false},
		{"only namespace separator is read", "github__", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsWriteClass(tc.in)
			if got != tc.want {
				t.Fatalf("IsWriteClass(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsWriteClass_CaseInsensitiveDanger verifies that the substring
// guards fire even when the operator's tool name uses unusual casing
// (real-world MCP servers have mixed conventions).
func TestIsWriteClass_CaseInsensitiveDanger(t *testing.T) {
	if !IsWriteClass("db__DROP_TABLE_users") {
		t.Fatal("uppercase DROP_TABLE must classify as write")
	}
	if !IsWriteClass("Git__Force_Push") {
		t.Fatal("mixed-case Force_Push must classify as write")
	}
}
