package skillregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

func auditRelations(heads []store.SkillRegistryEntry) []AuditIssue {
	issues := auditShadows(heads)
	return append(issues, auditDuplicateInstructions(heads)...)
}

func auditShadows(heads []store.SkillRegistryEntry) []AuditIssue {
	global := make(map[string]store.SkillRegistryEntry)
	for _, entry := range heads {
		if entry.WorkspaceID == nil {
			global[entry.Name] = entry
		}
	}
	issues := make([]AuditIssue, 0)
	for _, entry := range heads {
		base, ok := global[entry.Name]
		if entry.WorkspaceID == nil || !ok {
			continue
		}
		code, message := "SCOPE_SHADOW_DIVERGED", "workspace head overrides different global instructions"
		if entry.ContentHash == base.ContentHash {
			code, message = "SCOPE_SHADOW_IDENTICAL", "workspace head duplicates the global skill exactly"
		}
		issue := newAuditIssue(entry, code, AuditSeverityWarning, message, "workspace_id")
		issue.Related = []AuditSkillRef{auditRef(base)}
		issues = append(issues, issue)
		if entry.PublishedAt.Before(base.PublishedAt) {
			older := newAuditIssue(entry, "SCOPE_SHADOW_OLDER_THAN_GLOBAL", AuditSeverityWarning,
				"workspace override predates the current global head", "published_at")
			older.Related = []AuditSkillRef{auditRef(base)}
			issues = append(issues, older)
		}
	}
	return issues
}

func auditDuplicateInstructions(heads []store.SkillRegistryEntry) []AuditIssue {
	groups := make(map[string][]store.SkillRegistryEntry)
	for _, entry := range heads {
		_, markdown, err := splitFrontmatter(entry.Body)
		if err != nil {
			continue
		}
		normalized := strings.Join(strings.Fields(markdown), " ")
		if len(normalized) < 256 {
			continue
		}
		sum := sha256.Sum256([]byte(normalized))
		key := hex.EncodeToString(sum[:])
		groups[key] = append(groups[key], entry)
	}
	issues := make([]AuditIssue, 0)
	for _, group := range groups {
		if distinctSkillNames(group) < 2 {
			continue
		}
		refs := make([]AuditSkillRef, 0, len(group))
		for _, entry := range group {
			refs = append(refs, auditRef(entry))
		}
		sortAuditRefs(refs)
		anchor := refs[0]
		issues = append(issues, AuditIssue{
			Code:     "DUPLICATE_INSTRUCTIONS",
			Severity: AuditSeverityWarning,
			Skill:    anchor,
			Related:  append([]AuditSkillRef(nil), refs[1:]...),
			Message:  "different skill names contain identical normalized instructions",
			Field:    "body",
		})
	}
	return issues
}

func distinctSkillNames(entries []store.SkillRegistryEntry) int {
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		names[entry.Name] = struct{}{}
	}
	return len(names)
}

func sortAuditRefs(refs []AuditSkillRef) {
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && auditRefKey(refs[j]) < auditRefKey(refs[j-1]); j-- {
			refs[j], refs[j-1] = refs[j-1], refs[j]
		}
	}
}
