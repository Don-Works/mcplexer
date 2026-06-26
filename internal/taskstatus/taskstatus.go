// Package taskstatus centralises the small canonical lifecycle vocabulary
// that classifies otherwise-freeform task status text.
package taskstatus

import "strings"

const (
	KindOpen      = "open"
	KindWorking   = "working"
	KindBlocked   = "blocked"
	KindReview    = "review"
	KindDone      = "done"
	KindCancelled = "cancelled"
)

// DefaultKinds maps common freeform status words to lifecycle buckets when a
// workspace has not explicitly classified that status in task_status_vocabulary.
// Explicit vocabulary rows always win over this fallback.
var DefaultKinds = map[string]string{
	"open": KindOpen,
	"todo": KindOpen,

	"doing":       KindWorking,
	"in_progress": KindWorking,
	"in-progress": KindWorking,
	"inprogress":  KindWorking,
	"running":     KindWorking,
	"active":      KindWorking,
	"wip":         KindWorking,
	"started":     KindWorking,
	"ongoing":     KindWorking,

	"blocked": KindBlocked,
	"waiting": KindBlocked,
	"stuck":   KindBlocked,
	"paused":  KindBlocked,
	"on_hold": KindBlocked,
	"on-hold": KindBlocked,

	"review":          KindReview,
	"in_review":       KindReview,
	"in-review":       KindReview,
	"needs_review":    KindReview,
	"needs-review":    KindReview,
	"awaiting_review": KindReview,

	"done":      KindDone,
	"completed": KindDone,
	"complete":  KindDone,
	"finished":  KindDone,
	"closed":    KindDone,
	"resolved":  KindDone,
	"shipped":   KindDone,
	"archived":  KindDone,

	"cancelled": KindCancelled,
	"canceled":  KindCancelled,
	"abandoned": KindCancelled,
	"wontfix":   KindCancelled,
	"wont-fix":  KindCancelled,
	"won't_fix": KindCancelled,
	"rejected":  KindCancelled,
}

func Normalize(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func DefaultKind(status string) (string, bool) {
	kind, ok := DefaultKinds[Normalize(status)]
	return kind, ok
}

func IsValidKind(kind string) bool {
	switch kind {
	case KindOpen, KindWorking, KindBlocked, KindReview, KindDone, KindCancelled:
		return true
	default:
		return false
	}
}

func IsTerminalKind(kind string) bool {
	return kind == KindDone || kind == KindCancelled
}

func TerminalDefaultStatuses() []string {
	out := make([]string, 0, len(DefaultKinds))
	for status, kind := range DefaultKinds {
		if IsTerminalKind(kind) {
			out = append(out, status)
		}
	}
	return out
}

func DefaultKindMap() map[string]string {
	out := make(map[string]string, len(DefaultKinds))
	for status, kind := range DefaultKinds {
		out[status] = kind
	}
	return out
}
