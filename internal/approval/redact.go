package approval

import (
	"encoding/json"

	"github.com/don-works/mcplexer/internal/audit"
)

// redactApprovalArguments removes credential-shaped values before approval
// arguments leave the local process boundary or enter long-lived storage.
func redactApprovalArguments(args string) string {
	if args == "" {
		return ""
	}
	out := string(audit.Redact(json.RawMessage(args), nil))
	return audit.RedactString(out, nil)
}
