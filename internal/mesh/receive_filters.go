package mesh

import "strings"

// KindTaskEvent is the machine-plumbing kind emitted by the task service
// on every lifecycle transition. It is hidden from mesh__receive and the
// pending-count nag by default: task_event rows are audit echoes, not
// conversation. Pass kinds including "task_event" to opt in.
const KindTaskEvent = "task_event"

// ValidKinds is the enforced mesh__send kind vocabulary. The gateway's
// mesh__send hint and the Send validator both derive from this slice so
// the advertised set cannot drift from the enforced set again.
var ValidKinds = []string{
	"finding", "task", KindTaskEvent, "alert",
	"question", "result", "event", "reply",
}

// ValidKindsHint renders ValidKinds for error messages and tool hints.
func ValidKindsHint() string {
	return strings.Join(ValidKinds, "|")
}

func validKind(k string) bool {
	return containsString(ValidKinds, k)
}

// resolveKindFilters translates a ReceiveRequest's CSV kind filters into
// store filter lists, applying the default task_event exclusion. Unless
// the caller's kinds whitelist explicitly includes task_event, those rows
// are hidden either implicitly by the whitelist or by appending task_event
// to the exclusion list. The boolean says whether the task_event exclusion
// is in effect so the receive envelope can surface the opt-in path.
func resolveKindFilters(req ReceiveRequest) (kinds, excludeKinds []string, taskEventsExcluded bool) {
	kinds = splitCSVList(req.Kinds)
	excludeKinds = splitCSVList(req.ExcludeKinds)
	if containsString(kinds, KindTaskEvent) {
		return kinds, excludeKinds, false
	}
	if len(kinds) > 0 {
		return kinds, excludeKinds, true
	}
	if !containsString(excludeKinds, KindTaskEvent) {
		excludeKinds = append(excludeKinds, KindTaskEvent)
	}
	return kinds, excludeKinds, true
}

// receiveIsNarrowed reports whether a filter=new request carries any
// caller-supplied narrowing filter — kind, actor-kind, tag, or
// repo/branch/path scoping. Such a read is a NON-CONSUMING PEEK: it must not
// advance the cursor, because QueryMeshMessages filters the window at the
// store (Kinds/ExcludeKinds/ActorKinds/ExcludeActorKinds/Tags/Repo/Branch/
// WorkspacePath), so advancing to the delivered batch's max id would push the
// cursor PAST every non-matching message with a lower id and strand the
// broader backlog forever (B1: a kinds:"task_event" poll silently buries the
// entire normal-message backlog below the cursor).
//
// The default task_event exclusion is auto-applied when the caller leaves
// req.Kinds/req.ExcludeKinds empty (see resolveKindFilters), so it is
// deliberately NOT counted here — the canonical unfiltered poll still consumes
// and advances the cursor.
func receiveIsNarrowed(req ReceiveRequest) bool {
	return req.Tags != "" ||
		req.Kinds != "" ||
		req.ExcludeKinds != "" ||
		req.ActorKinds != "" ||
		req.ExcludeActorKinds != "" ||
		req.Repo != "" ||
		req.Branch != "" ||
		req.WorkspacePath != ""
}

// splitCSVList splits a comma-separated list into trimmed non-empty items.
func splitCSVList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
