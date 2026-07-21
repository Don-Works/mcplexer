package skillregistry_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

func TestAuditIsDeterministicAndFindsRegistryProblems(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	longInstructions := strings.Repeat("Repeatable instruction content for duplicate detection. ", 8)

	publishAuditBody(t, reg, nil, auditBody("duplicate-one", "General helper.", longInstructions), "")
	publishAuditBody(t, reg, nil, auditBody("duplicate-two", "Use when duplicate checks run.", longInstructions), "")
	publishAuditBody(t, reg, nil, auditBody("shadowed", "Use when testing global shadows.", "global"), "")
	publishAuditBody(t, reg, ptr("workspace-one"), auditBody("shadowed", "Use when testing workspace shadows.", "workspace"), "")
	publishAuditBody(t, reg, ptr("global"), auditBody("literal-global", "Use when testing sentinels.", "sentinel"), "")
	publishAuditBody(t, reg, nil, auditBody("missing-source", "Use when checking sources.", "source"), t.TempDir()+"/absent")
	publishAuditBody(t, reg, nil, auditBody("inert-include", "Use when checking includes.", "@include shared.md"), "")

	opts := skillregistry.AuditOptions{Scope: skillregistry.AdminScope(), IncludeInfo: true}
	first, err := reg.Audit(ctx, opts)
	if err != nil {
		t.Fatalf("first audit: %v", err)
	}
	second, err := reg.Audit(ctx, opts)
	if err != nil {
		t.Fatalf("second audit: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("identical registry state produced different audit reports")
	}
	for _, code := range []string{
		"DESCRIPTION_TRIGGER_MISSING", "DUPLICATE_INSTRUCTIONS", "INCLUDE_UNSUPPORTED_DIRECTIVE",
		"SCOPE_LITERAL_GLOBAL", "SCOPE_SHADOW_DIVERGED", "SOURCE_MISSING", "TAGS_EMPTY",
	} {
		if !auditHasCode(first, code) {
			t.Errorf("audit missing code %s: %+v", code, first.Issues)
		}
	}

	limited, err := reg.Audit(ctx, skillregistry.AuditOptions{
		Scope: skillregistry.AdminScope(), IncludeInfo: true, MaxIssues: 1,
	})
	if err != nil {
		t.Fatalf("limited audit: %v", err)
	}
	if len(limited.Issues) != 1 || !limited.Truncated || limited.IssueCount <= 1 {
		t.Fatalf("limited report = %+v", limited)
	}
}

func TestAuditFindsCorruptStoredRows(t *testing.T) {
	reg, db := newTestRegistry(t)
	ctx := context.Background()
	validBody := auditBody("frontmatter-name", "Use when checking integrity.", "valid")
	if _, err := db.PublishSkillRegistryEntry(ctx, &store.SkillRegistryEntry{
		Name: "stored-name", ContentHash: "incorrect", Description: "different",
		Body: validBody, MetadataJSON: json.RawMessage(`{}`), TagsJSON: json.RawMessage(`[]`),
	}); err != nil {
		t.Fatalf("seed mismatched row: %v", err)
	}
	invalidBody := "not a SKILL.md"
	sum := sha256.Sum256([]byte(invalidBody))
	if _, err := db.PublishSkillRegistryEntry(ctx, &store.SkillRegistryEntry{
		Name: "invalid-body", ContentHash: fmt.Sprintf("%x", sum), Description: "invalid",
		Body: invalidBody, MetadataJSON: json.RawMessage(`{}`), TagsJSON: json.RawMessage(`[]`),
	}); err != nil {
		t.Fatalf("seed invalid body row: %v", err)
	}
	report, err := reg.Audit(ctx, skillregistry.AuditOptions{Scope: skillregistry.AdminScope()})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	for _, code := range []string{
		"REGISTRY_BODY_INVALID", "REGISTRY_DESCRIPTION_MISMATCH",
		"REGISTRY_HASH_MISMATCH", "REGISTRY_NAME_MISMATCH",
	} {
		if !auditHasCode(report, code) {
			t.Errorf("audit missing code %s: %+v", code, report.Issues)
		}
	}
}

func TestAuditUnderstandsAndResolvesTypedComposition(t *testing.T) {
	reg, db := newTestRegistry(t)
	ctx := context.Background()
	targetBody := auditBody("audit-fragment", "Use when testing valid composition.", "VALID FRAGMENT")
	target, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "audit-fragment", Body: targetBody})
	if err != nil {
		t.Fatalf("publish target: %v", err)
	}
	validBody := includeBody("audit-composed", "Use when auditing valid composition.",
		includeDeclaration("fragment", "audit-fragment", "global", target.Version, target.ContentHash, ""),
		"<!-- mcpx:include fragment -->\n")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "audit-composed", Body: validBody}); err != nil {
		t.Fatalf("publish valid composition: %v", err)
	}

	report, err := reg.Audit(ctx, skillregistry.AuditOptions{Scope: skillregistry.AdminScope()})
	if err != nil {
		t.Fatalf("audit valid composition: %v", err)
	}
	if auditHasCodeForSkill(report, "INCLUDE_METADATA_INERT", "audit-composed") ||
		auditHasCodeForSkill(report, "COMPOSITION_UNRESOLVED", "audit-composed") {
		t.Fatalf("valid typed composition produced include findings: %+v", report.Issues)
	}

	brokenBody := includeBody("audit-broken", "Use when auditing broken composition.",
		includeDeclaration("missing", "missing-fragment", "global", 1, strings.Repeat("a", 64), ""),
		"<!-- mcpx:include missing -->\n")
	if _, err := db.PublishSkillRegistryEntry(ctx, &store.SkillRegistryEntry{
		Name: "audit-broken", ContentHash: skillregistry.ComputeContentHash(brokenBody),
		Description: "Use when auditing broken composition.", Body: brokenBody,
		MetadataJSON: json.RawMessage(`{}`), TagsJSON: json.RawMessage(`[]`), SourceType: "inline",
	}); err != nil {
		t.Fatalf("seed broken composition: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "audit-broken",
		Body: auditBody("audit-broken", "Use when auditing a newer plain head.", "PLAIN HEAD"),
	}); err != nil {
		t.Fatalf("publish newer plain head: %v", err)
	}
	report, err = reg.Audit(ctx, skillregistry.AuditOptions{Scope: skillregistry.AdminScope()})
	if err != nil {
		t.Fatalf("audit broken composition: %v", err)
	}
	if !auditHasCodeForSkillVersion(report, "COMPOSITION_UNRESOLVED", "audit-broken", 1) {
		t.Fatalf("broken non-head composition missing stable error finding: %+v", report.Issues)
	}
}

func auditBody(name, description, instructions string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n# Instructions\n\n" + instructions + "\n"
}

func publishAuditBody(
	t *testing.T, reg *skillregistry.Registry, workspaceID *string, body, sourcePath string,
) {
	t.Helper()
	if _, err := reg.Publish(context.Background(), skillregistry.PublishOptions{
		Body: body, WorkspaceID: workspaceID, SourcePath: sourcePath,
	}); err != nil {
		t.Fatalf("publish audit fixture: %v", err)
	}
}

func auditHasCode(report *skillregistry.AuditReport, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func auditHasCodeForSkill(report *skillregistry.AuditReport, code, name string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code && issue.Skill.Name == name {
			return true
		}
	}
	return false
}

func auditHasCodeForSkillVersion(report *skillregistry.AuditReport, code, name string, version int) bool {
	for _, issue := range report.Issues {
		if issue.Code == code && issue.Skill.Name == name && issue.Skill.Version == version {
			return true
		}
	}
	return false
}
