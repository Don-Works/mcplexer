package admin_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// TestPublishAsTemplateRoundTrip is the end-to-end integration test
// flagged by M3's spec: publish → list templates → install → verify the
// new Worker carries source_template_* + parameter values + tool
// allowlist + secret scope id.
func TestPublishAsTemplateRoundTrip(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	reg := workertemplates.New(db)
	svc.SetTemplatePublisher(reg)

	// 1) Author a worker with a prompt template carrying placeholders.
	in := baseCreate(wsID, scopeID)
	in.Name = "reddit-ads-reviewer"
	in.Description = "Reviews Reddit ads for a brand"
	in.PromptTemplate = "Review the {subreddit} ads for {brand}."
	in.ToolAllowlistJSON = `["reddit__list_ads"]`
	in.OutputChannelsJSON = `[{"type":"mesh","priority":"normal"}]`
	worker, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}

	// 2) Publish as template.
	entry, err := svc.PublishAsTemplate(ctx, admin.PublishAsTemplateInput{
		WorkerID: worker.ID,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if entry.Version != 1 {
		t.Fatalf("version = %d, want 1", entry.Version)
	}

	// 3) Decode the body, sanity-check the placeholders + secret slot.
	tmpl, err := workertemplates.Unmarshal(entry.Body)
	if err != nil {
		t.Fatalf("decode template: %v", err)
	}
	paramNames := map[string]bool{}
	for _, p := range tmpl.ParameterSchema {
		paramNames[p.Name] = true
	}
	for _, want := range []string{"subreddit", "brand"} {
		if !paramNames[want] {
			t.Errorf("parameter_schema missing %q (got %+v)", want, tmpl.ParameterSchema)
		}
	}
	if len(tmpl.SecretSlots) == 0 || tmpl.SecretSlots[0].Name != "model_api_key" {
		t.Errorf("expected model_api_key slot, got %+v", tmpl.SecretSlots)
	}
	if len(tmpl.ToolAllowlist) != 1 || tmpl.ToolAllowlist[0] != "reddit__list_ads" {
		t.Errorf("tool_allowlist mismatch: %+v", tmpl.ToolAllowlist)
	}

	// 4) Install the template into a NEW worker.
	installed, err := svc.InstallFromTemplate(ctx, admin.InstallFromTemplateInput{
		TemplateName:  entry.Name,
		WorkerName:    "reddit-ads-clone",
		WorkspaceID:   wsID,
		SecretScopeID: scopeID,
		Parameters:    map[string]string{"subreddit": "r/SaaS", "brand": "Mcplexer"},
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	// 5) Verify the new Worker tracks the source + carries the params.
	if installed.SourceTemplateName != entry.Name {
		t.Errorf("source_template_name = %q, want %q", installed.SourceTemplateName, entry.Name)
	}
	if installed.SourceTemplateVersion != entry.Version {
		t.Errorf("source_template_version = %d, want %d", installed.SourceTemplateVersion, entry.Version)
	}
	if !strings.Contains(installed.PromptTemplate, "{subreddit}") {
		t.Errorf("prompt_template lost placeholders")
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(installed.ParametersJSON), &params); err != nil {
		t.Fatalf("parameters_json invalid: %v", err)
	}
	if params["subreddit"] != "r/SaaS" || params["brand"] != "Mcplexer" {
		t.Errorf("parameters_json mismatch: %+v", params)
	}
	if installed.ToolAllowlistJSON != `["reddit__list_ads"]` {
		t.Errorf("tool_allowlist_json = %q, want reddit__list_ads", installed.ToolAllowlistJSON)
	}
}

// TestInstallFromTemplateMissingRequiredParam confirms the validator
// rejects an install that omits a required parameter.
func TestInstallFromTemplateMissingRequiredParam(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	reg := workertemplates.New(db)
	svc.SetTemplatePublisher(reg)

	in := baseCreate(wsID, scopeID)
	in.PromptTemplate = "Hello {audience}."
	w, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	entry, err := svc.PublishAsTemplate(ctx, admin.PublishAsTemplateInput{WorkerID: w.ID})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	_, err = svc.InstallFromTemplate(ctx, admin.InstallFromTemplateInput{
		TemplateName:  entry.Name,
		WorkerName:    "needs-audience",
		WorkspaceID:   wsID,
		SecretScopeID: scopeID,
	})
	if err == nil {
		t.Fatal("expected missing-required-parameter error, got nil")
	}
	if !strings.Contains(err.Error(), "audience") {
		t.Errorf("expected error to mention 'audience', got %q", err.Error())
	}
}

// TestPublishAsTemplateDedup confirms re-publishing identical content
// returns the same version (registry-level content-hash dedup).
func TestPublishAsTemplateDedup(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	reg := workertemplates.New(db)
	svc.SetTemplatePublisher(reg)

	in := baseCreate(wsID, scopeID)
	in.PromptTemplate = "Static."
	w, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := svc.PublishAsTemplate(ctx, admin.PublishAsTemplateInput{WorkerID: w.ID})
	if err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	second, err := svc.PublishAsTemplate(ctx, admin.PublishAsTemplateInput{WorkerID: w.ID})
	if err != nil {
		t.Fatalf("publish 2: %v", err)
	}
	if first.Version != second.Version {
		t.Errorf("expected dedup; got versions %d and %d", first.Version, second.Version)
	}
}
