package admin

import (
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	deliverableSuccessWithOutput = "success_with_output"
	deliverableSpendNoCommit     = "spend_no_commit"
	deliverableFailedNoOutput    = "failed_no_output"
	deliverablePartial           = "partial"
	deliverableUnknown           = "unknown"
)

var (
	workerStatusLineRe = regexp.MustCompile(`(?mi)^STATUS:\s*(success|blocked|partial)\b`)
	commitSHARe        = regexp.MustCompile(`(?i)(?:commit(?:\s+sha)?[:\s]+|sha[:\s]+)([0-9a-f]{7,40})\b`)
	branchLineRe       = regexp.MustCompile(`(?i)(?:^|\n)\s*(?:branch|BRANCH)[:\s=]+([^\s\n,;]+)`)
	noDeliverableRe    = regexp.MustCompile(`(?i)(?:no-code|superseded|no\s+commit|returned\s+no-code)`)
	changedLineRe      = regexp.MustCompile(`(?mi)^CHANGED:\s*(.+)$`)
)

// annotateDeliverable derives deliverable-truth fields from a terminal run's
// output_text. Call at read time after the run row is loaded.
func annotateDeliverable(run *store.WorkerRun) {
	if run == nil {
		return
	}
	text := strings.TrimSpace(run.OutputText)
	reported := parseWorkerReportedStatus(text)
	run.WorkerReportedStatus = reported

	trustedSnapshot := strings.TrimSpace(run.ResultCommit) != "" || strings.TrimSpace(run.ResultBranch) != ""
	commit, branch := "", ""
	if trustedSnapshot {
		// A clean snapshot points at the base HEAD but did not produce an
		// artifact. Keep the raw Result* fields for audit while withholding
		// them from the derived deliverable contract.
		if run.ResultChanged {
			commit = strings.TrimSpace(run.ResultCommit)
			branch = strings.TrimSpace(run.ResultBranch)
		}
	} else {
		// Legacy/non-isolated runs have no trusted snapshot columns; retain the
		// worker-reported parser only as a compatibility fallback.
		commit = parseDeliverableCommit(text)
		branch = parseDeliverableBranch(text)
	}
	run.DeliverableCommit = commit
	run.DeliverableBranch = branch
	run.HasDeliverableCommit = commit != ""
	run.HasDeliverableBranch = branch != ""

	// Committed output requires an explicit branch or commit in the
	// worker's return contract — a prose CHANGED line alone is not
	// enough to clear spend_no_commit for coding delegations.
	hasArtifact := run.HasDeliverableCommit || run.HasDeliverableBranch
	hasSpend := runHasSpend(run)
	explicitNoDeliverable := noDeliverableRe.MatchString(text)

	switch {
	case terminalNonSuccessDeliverableStatus(run.Status):
		if hasArtifact {
			run.DeliverableStatus = deliverablePartial
		} else {
			run.DeliverableStatus = deliverableFailedNoOutput
		}
	case isFailedNoOutput(run, text, reported):
		run.DeliverableStatus = deliverableFailedNoOutput
	case reported == "partial" || run.Status == "partial":
		run.DeliverableStatus = deliverablePartial
	case hasArtifact && !explicitNoDeliverable:
		run.DeliverableStatus = deliverableSuccessWithOutput
	case hasSpend && !hasArtifact && !explicitNoDeliverable &&
		(reported == "success" || run.Status == "success"):
		run.DeliverableStatus = deliverableSpendNoCommit
	case reported == "blocked":
		run.DeliverableStatus = deliverableFailedNoOutput
	default:
		run.DeliverableStatus = deliverableUnknown
	}
}

func terminalNonSuccessDeliverableStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failure", "cap_exceeded", "rejected", "blocked", "cancelled", "interrupted", "awaiting_approval":
		return true
	default:
		return false
	}
}

func parseWorkerReportedStatus(text string) string {
	m := workerStatusLineRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(m[1]))
}

func parseDeliverableCommit(text string) string {
	m := commitSHARe.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.ToLower(m[1])
}

func parseDeliverableBranch(text string) string {
	m := branchLineRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(m[1]), `"'`)
}

func hasSubstantiveChangedLine(text string) bool {
	m := changedLineRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return false
	}
	changed := strings.TrimSpace(m[1])
	if changed == "" {
		return false
	}
	lower := strings.ToLower(changed)
	if strings.Contains(lower, "none") || strings.Contains(lower, "no changes") ||
		strings.Contains(lower, "no-code") || strings.Contains(lower, "superseded") {
		return false
	}
	return true
}

func runHasSpend(run *store.WorkerRun) bool {
	if run == nil {
		return false
	}
	if run.ToolCallsCount > 0 || run.InputTokens > 0 || run.OutputTokens > 0 ||
		run.CostUSD > 0 || run.RealCostUSD > 0 {
		return true
	}
	// CLI runs may omit usage telemetry while still doing real work.
	return run.Status == "success" && run.AccountingMissing
}

func isFailedNoOutput(run *store.WorkerRun, text, reported string) bool {
	if run == nil {
		return false
	}
	if run.Status == "failure" || run.Status == "cap_exceeded" || run.Status == "rejected" {
		if strings.TrimSpace(text) == "" && strings.TrimSpace(run.Error) != "" {
			return true
		}
		if strings.HasPrefix(strings.TrimSpace(run.Error), "adapter send:") &&
			run.InputTokens == 0 && run.OutputTokens == 0 {
			return true
		}
	}
	if reported == "blocked" && !hasSubstantiveChangedLine(text) &&
		parseDeliverableCommit(text) == "" && parseDeliverableBranch(text) == "" {
		if strings.TrimSpace(text) == "" || strings.Contains(strings.ToLower(text), "blocked") {
			return true
		}
	}
	return false
}
