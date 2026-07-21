package skillregistry

import (
	"context"
	"fmt"
	"sort"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	AuditSeverityError   = "error"
	AuditSeverityWarning = "warning"
	AuditSeverityInfo    = "info"
	defaultAuditLimit    = 500
	maxAuditLimit        = 2000
)

// AuditOptions controls a read-only registry consistency audit.
type AuditOptions struct {
	Scope       store.SkillScope
	IncludeInfo bool
	MaxIssues   int
}

// AuditSkillRef identifies one immutable registry version without exposing its body.
type AuditSkillRef struct {
	Name        string  `json:"name"`
	Version     int     `json:"version"`
	WorkspaceID *string `json:"workspace_id,omitempty"`
}

// AuditIssue is one deterministic audit finding. Code is stable for automation.
type AuditIssue struct {
	Code       string          `json:"code"`
	Severity   string          `json:"severity"`
	Skill      AuditSkillRef   `json:"skill"`
	Related    []AuditSkillRef `json:"related,omitempty"`
	Message    string          `json:"message"`
	Field      string          `json:"field,omitempty"`
	Line       int             `json:"line,omitempty"`
	Suggestion string          `json:"suggestion,omitempty"`
}

// AuditSummary counts all findings before the response limit is applied.
type AuditSummary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Info     int `json:"info"`
}

// AuditReport deliberately has no generated-at timestamp so identical registry
// state produces byte-for-byte stable JSON.
type AuditReport struct {
	ScopeHeadCount int          `json:"scope_head_count"`
	DistinctNames  int          `json:"distinct_names"`
	IssueCount     int          `json:"issue_count"`
	Summary        AuditSummary `json:"summary"`
	Issues         []AuditIssue `json:"issues"`
	Truncated      bool         `json:"truncated"`
}

// Audit inspects scope heads for integrity, source drift, style, shadowing, and
// duplicated instructions. Composition resolution additionally covers every
// active version because an explicitly pinned historical version remains a
// runtime dependency even when it is no longer a head. Audit never mutates
// registry or source state.
func (r *Registry) Audit(ctx context.Context, opts AuditOptions) (*AuditReport, error) {
	heads, err := r.ListScopeHeads(ctx, opts.Scope, 0)
	if err != nil {
		return nil, err
	}
	issues := make([]AuditIssue, 0)
	for i := range heads {
		issues = append(issues, auditEntry(heads[i])...)
	}
	compositionIssues, err := r.auditCompositionVersions(ctx, opts.Scope, heads)
	if err != nil {
		return nil, err
	}
	issues = append(issues, compositionIssues...)
	issues = append(issues, auditRelations(heads)...)
	if !opts.IncludeInfo {
		issues = filterAuditInfo(issues)
	}
	sortAuditIssues(issues)
	return buildAuditReport(heads, issues, opts.MaxIssues), nil
}

func (r *Registry) auditCompositionVersions(
	ctx context.Context, scope store.SkillScope, heads []store.SkillRegistryEntry,
) ([]AuditIssue, error) {
	nameSet := make(map[string]struct{}, len(heads))
	for _, head := range heads {
		nameSet[head.Name] = struct{}{}
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)

	var issues []AuditIssue
	for _, name := range names {
		versions, err := r.store.ListSkillRegistryVersions(ctx, scope, name, false)
		if err != nil {
			return nil, fmt.Errorf("audit composition versions for %q: %w", name, err)
		}
		for i := range versions {
			parsed, parseErr := Parse(versions[i].Body, versions[i].Name)
			if parseErr != nil || len(parsed.Extra.Includes) == 0 {
				continue
			}
			if _, renderErr := r.RenderEntry(ctx, &versions[i]); renderErr != nil {
				issues = append(issues, newAuditIssue(versions[i], "COMPOSITION_UNRESOLVED", AuditSeverityError,
					"composition cannot be resolved: "+renderErr.Error(), "includes"))
			}
		}
	}
	return issues, nil
}

func buildAuditReport(
	heads []store.SkillRegistryEntry, issues []AuditIssue, requestedLimit int,
) *AuditReport {
	report := &AuditReport{ScopeHeadCount: len(heads), IssueCount: len(issues)}
	names := make(map[string]struct{}, len(heads))
	for _, entry := range heads {
		names[entry.Name] = struct{}{}
	}
	report.DistinctNames = len(names)
	for _, issue := range issues {
		switch issue.Severity {
		case AuditSeverityError:
			report.Summary.Errors++
		case AuditSeverityWarning:
			report.Summary.Warnings++
		case AuditSeverityInfo:
			report.Summary.Info++
		}
	}
	limit := requestedLimit
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	if limit > maxAuditLimit {
		limit = maxAuditLimit
	}
	report.Truncated = len(issues) > limit
	if report.Truncated {
		issues = issues[:limit]
	}
	report.Issues = issues
	return report
}

func filterAuditInfo(issues []AuditIssue) []AuditIssue {
	out := issues[:0]
	for _, issue := range issues {
		if issue.Severity != AuditSeverityInfo {
			out = append(out, issue)
		}
	}
	return out
}

func sortAuditIssues(issues []AuditIssue) {
	for i := range issues {
		sort.Slice(issues[i].Related, func(a, b int) bool {
			return auditRefKey(issues[i].Related[a]) < auditRefKey(issues[i].Related[b])
		})
	}
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		ak := severityRank(a.Severity) + "\x00" + a.Code + "\x00" + auditRefKey(a.Skill)
		bk := severityRank(b.Severity) + "\x00" + b.Code + "\x00" + auditRefKey(b.Skill)
		return ak < bk
	})
}

func severityRank(severity string) string {
	switch severity {
	case AuditSeverityError:
		return "0"
	case AuditSeverityWarning:
		return "1"
	default:
		return "2"
	}
}

func auditRef(entry store.SkillRegistryEntry) AuditSkillRef {
	return AuditSkillRef{Name: entry.Name, Version: entry.Version, WorkspaceID: entry.WorkspaceID}
}

func auditRefKey(ref AuditSkillRef) string {
	workspace := ""
	if ref.WorkspaceID != nil {
		workspace = *ref.WorkspaceID
	}
	return fmt.Sprintf("%s\x00%s\x00%010d", ref.Name, workspace, ref.Version)
}
