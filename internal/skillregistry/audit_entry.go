package skillregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

var (
	includeDirectiveRE   = regexp.MustCompile(`(?mi)^\s*(?:@include\b|\{\{\s*include\b)`)
	descriptionTriggerRE = regexp.MustCompile(`(?i)\bwhen\b`)
)

func auditEntry(entry store.SkillRegistryEntry) []AuditIssue {
	issues := make([]AuditIssue, 0)
	issues = append(issues, auditIntegrity(entry)...)
	issues = append(issues, auditTags(entry)...)
	issues = append(issues, auditStyle(entry)...)
	issues = append(issues, auditIncludes(entry)...)
	issues = append(issues, auditSource(entry)...)
	if entry.WorkspaceID != nil && strings.TrimSpace(*entry.WorkspaceID) == "global" {
		issues = append(issues, newAuditIssue(entry, "SCOPE_LITERAL_GLOBAL", AuditSeverityError,
			`workspace_id "global" is a routing sentinel stored as a real scope`, "workspace_id"))
	}
	return issues
}

func auditIntegrity(entry store.SkillRegistryEntry) []AuditIssue {
	issues := make([]AuditIssue, 0, 4)
	sum := sha256.Sum256([]byte(entry.Body))
	if hex.EncodeToString(sum[:]) != entry.ContentHash {
		issues = append(issues, newAuditIssue(entry, "REGISTRY_HASH_MISMATCH", AuditSeverityError,
			"stored content hash does not match the SKILL.md body", "content_hash"))
	}
	parsed, err := Parse(entry.Body, "")
	if err != nil {
		return append(issues, newAuditIssue(entry, "REGISTRY_BODY_INVALID", AuditSeverityError,
			"stored SKILL.md is invalid: "+err.Error(), "body"))
	}
	if parsed.Name != entry.Name {
		issues = append(issues, newAuditIssue(entry, "REGISTRY_NAME_MISMATCH", AuditSeverityError,
			fmt.Sprintf("frontmatter name %q differs from registry name %q", parsed.Name, entry.Name), "name"))
	}
	if parsed.Description != entry.Description {
		issues = append(issues, newAuditIssue(entry, "REGISTRY_DESCRIPTION_MISMATCH", AuditSeverityError,
			"frontmatter description differs from the indexed description", "description"))
	}
	return issues
}

func auditTags(entry store.SkillRegistryEntry) []AuditIssue {
	var tags []string
	if err := json.Unmarshal(entry.TagsJSON, &tags); err != nil {
		return []AuditIssue{newAuditIssue(entry, "TAGS_JSON_INVALID", AuditSeverityError,
			"stored tags are not a JSON string array: "+err.Error(), "tags")}
	}
	if len(tags) == 0 {
		return []AuditIssue{newAuditIssue(entry, "TAGS_EMPTY", AuditSeverityInfo,
			"skill has no search tags", "tags")}
	}
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		if _, ok := seen[normalized]; ok {
			return []AuditIssue{newAuditIssue(entry, "TAG_DUPLICATE", AuditSeverityWarning,
				fmt.Sprintf("tag %q appears more than once", tag), "tags")}
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

func auditStyle(entry store.SkillRegistryEntry) []AuditIssue {
	issues := make([]AuditIssue, 0, 3)
	if !hasDescriptionTrigger(entry.Description) {
		issue := newAuditIssue(entry, "DESCRIPTION_TRIGGER_MISSING", AuditSeverityWarning,
			`description should say when the skill should be used (for example, "Use when …")`, "description")
		issue.Suggestion = "Add an explicit trigger phrase to improve skill selection."
		issues = append(issues, issue)
	}
	if lines := strings.Count(entry.Body, "\n") + 1; lines > 500 {
		issues = append(issues, newAuditIssue(entry, "BODY_LINE_LIMIT", AuditSeverityWarning,
			fmt.Sprintf("SKILL.md has %d lines; guidance recommends at most 500", lines), "body"))
	}
	if tokens := models.EstimateContextTokens(len(entry.Body)); tokens > 5000 {
		issues = append(issues, newAuditIssue(entry, "BODY_TOKEN_LIMIT", AuditSeverityWarning,
			fmt.Sprintf("SKILL.md is approximately %d context tokens; guidance recommends at most 5000", tokens), "body"))
	}
	return issues
}

func hasDescriptionTrigger(description string) bool {
	return descriptionTriggerRE.MatchString(description)
}

func auditIncludes(entry store.SkillRegistryEntry) []AuditIssue {
	issues := make([]AuditIssue, 0, 2)
	if loc := includeDirectiveRE.FindStringIndex(entry.Body); loc != nil {
		issue := newAuditIssue(entry, "INCLUDE_UNSUPPORTED_DIRECTIVE", AuditSeverityWarning,
			"include directive is inert; registry retrieval does not expand includes", "body")
		issue.Line = strings.Count(entry.Body[:loc[0]], "\n") + 1
		issues = append(issues, issue)
	}
	var metadata map[string]any
	if json.Unmarshal(entry.MetadataJSON, &metadata) == nil && metadataHasInclude(metadata) {
		issues = append(issues, newAuditIssue(entry, "INCLUDE_METADATA_INERT", AuditSeverityWarning,
			"include metadata is stored but not expanded by registry retrieval", "metadata"))
	}
	return issues
}

func metadataHasInclude(metadata map[string]any) bool {
	for key, value := range metadata {
		if key == ManifestExtraStashKey {
			// Typed ManifestExtra includes are executable composition data, not
			// inert free-form metadata. Audit resolves them through RenderEntry.
			continue
		}
		if key == "include" || key == "includes" {
			return true
		}
		if nested, ok := value.(map[string]any); ok && metadataHasInclude(nested) {
			return true
		}
	}
	return false
}

func auditSource(entry store.SkillRegistryEntry) []AuditIssue {
	switch entry.SourceType {
	case "", "inline", "bundle", "hub", "hub-pull":
		return nil
	case "path", "git":
		return auditFileSource(entry)
	default:
		return []AuditIssue{newAuditIssue(entry, "SOURCE_TYPE_UNKNOWN", AuditSeverityWarning,
			fmt.Sprintf("source type %q has no audit policy", entry.SourceType), "source_type")}
	}
}

func auditFileSource(entry store.SkillRegistryEntry) []AuditIssue {
	if strings.TrimSpace(entry.SourcePath) == "" {
		return []AuditIssue{newAuditIssue(entry, "SOURCE_MISSING", AuditSeverityError,
			"path-backed skill has no source path", "source_path")}
	}
	path, issue := resolveAuditSourcePath(entry)
	if issue != nil {
		return []AuditIssue{*issue}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return []AuditIssue{newAuditIssue(entry, "SOURCE_UNREADABLE", AuditSeverityError,
			"cannot read source SKILL.md: "+err.Error(), "source_path")}
	}
	issues := make([]AuditIssue, 0, 2)
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != entry.ContentHash {
		issues = append(issues, newAuditIssue(entry, "SOURCE_DRIFT", AuditSeverityWarning,
			"source SKILL.md differs from the published registry head", "source_path"))
	}
	if parsed, parseErr := Parse(string(body), ""); parseErr == nil && parsed.Name != entry.Name {
		issues = append(issues, newAuditIssue(entry, "SOURCE_NAME_MISMATCH", AuditSeverityError,
			fmt.Sprintf("source frontmatter name %q differs from registry name %q", parsed.Name, entry.Name), "source_path"))
	}
	return issues
}

func resolveAuditSourcePath(entry store.SkillRegistryEntry) (string, *AuditIssue) {
	path := entry.SourcePath
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		issue := newAuditIssue(entry, "SOURCE_MISSING", AuditSeverityError,
			"source path does not exist", "source_path")
		return "", &issue
	}
	if err != nil {
		issue := newAuditIssue(entry, "SOURCE_UNREADABLE", AuditSeverityError,
			"cannot inspect source path: "+err.Error(), "source_path")
		return "", &issue
	}
	if info.Mode()&os.ModeSymlink != 0 {
		issue := newAuditIssue(entry, "SOURCE_SYMLINK", AuditSeverityError,
			"source path is a symlink and was not followed", "source_path")
		return "", &issue
	}
	if info.IsDir() {
		return inspectAuditSkillFile(entry, filepath.Join(path, "SKILL.md"))
	}
	return validateAuditSourceSize(entry, path, info)
}

func inspectAuditSkillFile(entry store.SkillRegistryEntry, path string) (string, *AuditIssue) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		issue := newAuditIssue(entry, "SOURCE_MISSING", AuditSeverityError,
			"source directory does not contain SKILL.md", "source_path")
		return "", &issue
	}
	if err != nil {
		issue := newAuditIssue(entry, "SOURCE_UNREADABLE", AuditSeverityError,
			"cannot inspect source SKILL.md: "+err.Error(), "source_path")
		return "", &issue
	}
	if info.Mode()&os.ModeSymlink != 0 {
		issue := newAuditIssue(entry, "SOURCE_SYMLINK", AuditSeverityError,
			"source SKILL.md is a symlink and was not followed", "source_path")
		return "", &issue
	}
	return validateAuditSourceSize(entry, path, info)
}

func validateAuditSourceSize(
	entry store.SkillRegistryEntry, path string, info os.FileInfo,
) (string, *AuditIssue) {
	if info.Size() > MaxBodyBytes {
		issue := newAuditIssue(entry, "SOURCE_UNREADABLE", AuditSeverityError,
			fmt.Sprintf("source SKILL.md exceeds the %d-byte audit read limit", MaxBodyBytes), "source_path")
		return "", &issue
	}
	return path, nil
}

func newAuditIssue(
	entry store.SkillRegistryEntry, code, severity, message, field string,
) AuditIssue {
	return AuditIssue{Code: code, Severity: severity, Skill: auditRef(entry), Message: message, Field: field}
}
