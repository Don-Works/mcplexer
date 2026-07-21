package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

type adminSkillPublishParams struct {
	Name          string         `json:"name"`
	Body          string         `json:"body"`
	BodyBase64    string         `json:"body_b64"`
	BundleBase64  string         `json:"bundle_b64"`
	ParentVersion *int           `json:"parent_version"`
	Author        string         `json:"author"`
	WorkspaceID   *string        `json:"workspace_id"`
	SourcePath    string         `json:"source_path"`
	SourceType    string         `json:"source_type"`
	Metadata      map[string]any `json:"metadata_extras"`
}

func handlePublishSkillRegistry(reg *skillregistry.Registry) handlerFunc {
	return func(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
		var p adminSkillPublishParams
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if err := validateAdminSkillMetadataExtras(p.Metadata); err != nil {
			return nil, err
		}
		body, err := decodeAdminSkillBody(p.Body, p.BodyBase64)
		if err != nil {
			return nil, err
		}
		workspaceID, err := validatePublishWorkspace(ctx, s, p.WorkspaceID)
		if err != nil {
			return nil, err
		}
		author := strings.TrimSpace(p.Author)
		if author == "" {
			author = "admin"
		}
		bundle, err := decodeAdminSkillBundle(p.BundleBase64)
		if err != nil {
			return nil, err
		}
		sourceType := strings.TrimSpace(p.SourceType)
		sourcePath := strings.TrimSpace(p.SourcePath)
		if err := validateAdminSkillProvenance(sourceType, sourcePath, bundle); err != nil {
			return nil, err
		}
		result, err := reg.Publish(ctx, skillregistry.PublishOptions{
			Name: p.Name, Body: body, ParentVersion: p.ParentVersion,
			Author: author, WorkspaceID: workspaceID, Bundle: bundle,
			SourcePath: sourcePath, SourceTypeOverride: sourceType,
			MetadataExtras: p.Metadata,
		})
		if err != nil {
			return nil, err
		}
		return jsonResult(result)
	}
}

func validateAdminSkillMetadataExtras(metadata map[string]any) error {
	if _, reserved := metadata[skillregistry.ManifestExtraStashKey]; reserved {
		return fmt.Errorf(
			"metadata_extras.%s is reserved; typed skill extras must come from parsed and validated SKILL.md frontmatter",
			skillregistry.ManifestExtraStashKey,
		)
	}
	return nil
}

func decodeAdminSkillBundle(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(skillregistry.MaxBundleBytes) {
		return nil, fmt.Errorf("bundle_b64 exceeds the %d-byte decoded limit", skillregistry.MaxBundleBytes)
	}
	bundle, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode bundle_b64: %w", err)
	}
	if len(bundle) > skillregistry.MaxBundleBytes {
		return nil, fmt.Errorf("bundle exceeds %d bytes", skillregistry.MaxBundleBytes)
	}
	return bundle, nil
}

func validateAdminSkillProvenance(sourceType, sourcePath string, bundle []byte) error {
	switch sourceType {
	case "bundle":
		if len(bundle) == 0 {
			return fmt.Errorf("source_type=\"bundle\" requires bundle_b64")
		}
	case "path", "git":
		if sourcePath == "" {
			return fmt.Errorf("source_type=%q requires source_path", sourceType)
		}
	}
	return nil
}

func decodeAdminSkillBody(body, encoded string) (string, error) {
	if body != "" && encoded != "" {
		return "", fmt.Errorf("provide exactly one of body or body_b64")
	}
	if body == "" && encoded == "" {
		return "", fmt.Errorf("body or body_b64 is required")
	}
	if encoded == "" {
		return body, nil
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(skillregistry.MaxBodyBytes) {
		return "", fmt.Errorf("body_b64 exceeds the %d-byte decoded limit", skillregistry.MaxBodyBytes)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode body_b64: %w", err)
	}
	if len(decoded) > skillregistry.MaxBodyBytes {
		return "", fmt.Errorf("body exceeds %d bytes", skillregistry.MaxBodyBytes)
	}
	return string(decoded), nil
}

func validatePublishWorkspace(
	ctx context.Context, s store.Store, requested *string,
) (*string, error) {
	workspaceID, err := cleanOptionalWorkspaceID(requested)
	if err != nil || workspaceID == nil {
		return workspaceID, err
	}
	if *workspaceID == "global" {
		return nil, fmt.Errorf(`workspace_id "global" is reserved; omit workspace_id to publish globally`)
	}
	if _, err := s.GetWorkspace(ctx, *workspaceID); err != nil {
		return nil, fmt.Errorf("verify workspace %q: %w", *workspaceID, err)
	}
	return workspaceID, nil
}

func handleAuditSkillRegistry(reg *skillregistry.Registry) handlerFunc {
	return func(ctx context.Context, _ store.Store, args json.RawMessage) (json.RawMessage, error) {
		var p struct {
			IncludeInfo bool `json:"include_info"`
			MaxIssues   int  `json:"max_issues"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		report, err := reg.Audit(ctx, skillregistry.AuditOptions{
			Scope: skillregistry.AdminScope(), IncludeInfo: p.IncludeInfo, MaxIssues: p.MaxIssues,
		})
		if err != nil {
			return nil, err
		}
		return jsonResult(report)
	}
}
