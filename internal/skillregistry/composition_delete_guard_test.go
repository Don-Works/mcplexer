package skillregistry_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

func TestCompositionDeleteGuardScansNonHeadVersionsAndExactTarget(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	targetV1, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "guard-target", Body: sampleBody("guard-target", "Guard target v1."),
	})
	if err != nil {
		t.Fatalf("publish target v1: %v", err)
	}
	targetV2, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "guard-target", Body: sampleBody("guard-target", "Guard target v2."),
	})
	if err != nil {
		t.Fatalf("publish target v2: %v", err)
	}
	rootV1 := includeBody("guard-root", "Guard root v1.",
		includeDeclaration("target", "guard-target", "global", targetV1.Version, targetV1.ContentHash, ""),
		"<!-- mcpx:include target -->\n")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "guard-root", Body: rootV1}); err != nil {
		t.Fatalf("publish dependent v1: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "guard-root", Body: sampleBody("guard-root", "Guard root v2 without includes."),
	}); err != nil {
		t.Fatalf("publish dependent v2: %v", err)
	}

	// The pin is to v1, so deleting the unrelated v2 remains valid.
	if err := reg.SoftDelete(ctx, nil, "guard-target", targetV2.Version); err != nil {
		t.Fatalf("delete unrelated target version: %v", err)
	}
	err = reg.SoftDelete(ctx, nil, "guard-target", targetV1.Version)
	if !errors.Is(err, skillregistry.ErrCompositionReferenced) {
		t.Fatalf("delete pinned target error = %v, want ErrCompositionReferenced", err)
	}
	if !strings.Contains(err.Error(), "global/guard-root@v1") || !strings.Contains(err.Error(), `include "target"`) {
		t.Fatalf("delete error does not name dependent: %v", err)
	}
	if _, err := reg.Get(ctx, skillregistry.GlobalScope(), "guard-target", skillregistry.VersionRef{Version: 1}); err != nil {
		t.Fatalf("blocked target was mutated: %v", err)
	}

	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "unrelated-delete", Body: sampleBody("unrelated-delete", "Unrelated delete fixture."),
	}); err != nil {
		t.Fatalf("publish unrelated: %v", err)
	}
	if err := reg.SoftDelete(ctx, nil, "unrelated-delete", 1); err != nil {
		t.Fatalf("unrelated delete was blocked: %v", err)
	}
}

func TestCompositionDeleteGuardResolvesGlobalAndSameScopes(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	workspace := "guard-workspace"

	globalSameName, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "scoped-target", Body: sampleBody("scoped-target", "Global scoped target."),
	})
	if err != nil {
		t.Fatalf("publish global target: %v", err)
	}
	workspaceSameName, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "scoped-target", Body: sampleBody("scoped-target", "Workspace scoped target."), WorkspaceID: &workspace,
	})
	if err != nil {
		t.Fatalf("publish workspace target: %v", err)
	}
	sameRoot := includeBody("same-scope-root", "Same-scope guard root.",
		includeDeclaration("target", "scoped-target", "same", workspaceSameName.Version, workspaceSameName.ContentHash, ""),
		"<!-- mcpx:include target -->\n")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "same-scope-root", Body: sameRoot, WorkspaceID: &workspace,
	}); err != nil {
		t.Fatalf("publish same-scope root: %v", err)
	}

	// A workspace `same` pin does not point at the global row of the same
	// name/version, so the global delete is unrelated.
	if err := reg.SoftDelete(ctx, nil, "scoped-target", globalSameName.Version); err != nil {
		t.Fatalf("global target incorrectly blocked by workspace same pin: %v", err)
	}
	err = reg.SoftDelete(ctx, &workspace, "scoped-target", workspaceSameName.Version)
	if !errors.Is(err, skillregistry.ErrCompositionReferenced) || !strings.Contains(err.Error(), "same-scope-root") {
		t.Fatalf("workspace same pin did not block exact target: %v", err)
	}

	globalPinTarget, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "global-pin-target", Body: sampleBody("global-pin-target", "Global pin target."),
	})
	if err != nil {
		t.Fatalf("publish global pin target: %v", err)
	}
	globalRoot := includeBody("global-scope-root", "Global-scope guard root.",
		includeDeclaration("target", "global-pin-target", "global", globalPinTarget.Version, globalPinTarget.ContentHash, ""),
		"<!-- mcpx:include target -->\n")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "global-scope-root", Body: globalRoot, WorkspaceID: &workspace,
	}); err != nil {
		t.Fatalf("publish global-scope root: %v", err)
	}
	err = reg.SoftDelete(ctx, nil, "global-pin-target", globalPinTarget.Version)
	if !errors.Is(err, skillregistry.ErrCompositionReferenced) || !strings.Contains(err.Error(), "global-scope-root") {
		t.Fatalf("workspace referrer's global pin did not block global target: %v", err)
	}
}

func TestCompositionDeleteGuardAllowsBulkSelfDelete(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	v1, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "self-chain", Body: sampleBody("self-chain", "Self-chain v1."),
	})
	if err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	v2Body := includeBody("self-chain", "Self-chain v2.",
		includeDeclaration("previous", "self-chain", "same", v1.Version, v1.ContentHash, ""),
		"<!-- mcpx:include previous -->\n")
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{Name: "self-chain", Body: v2Body}); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	if err := reg.SoftDelete(ctx, nil, "self-chain", v1.Version); !errors.Is(err, skillregistry.ErrCompositionReferenced) {
		t.Fatalf("exact v1 delete should be blocked by surviving v2: %v", err)
	}
	if err := reg.SoftDelete(ctx, nil, "self-chain", 0); err != nil {
		t.Fatalf("bulk self-delete should ignore dependents in same deletion set: %v", err)
	}
	if _, err := reg.Get(ctx, skillregistry.GlobalScope(), "self-chain", skillregistry.VersionRef{Latest: true}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bulk delete left an active version: %v", err)
	}
}

func TestCompositionPublishAndDeleteAreSerialized(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	target, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: "serialized-target", Body: sampleBody("serialized-target", "Serialized target."),
	})
	if err != nil {
		t.Fatalf("publish target: %v", err)
	}
	guardEntered := make(chan struct{})
	releaseDelete := make(chan struct{})
	reg.AddDeleteGuard(func(_ context.Context, _ *string, name string, _ int) error {
		if name == "serialized-target" {
			close(guardEntered)
			<-releaseDelete
		}
		return nil
	})

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- reg.SoftDelete(ctx, nil, "serialized-target", target.Version)
	}()
	select {
	case <-guardEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("delete did not reach blocking guard")
	}

	rootBody := includeBody("serialized-root", "Serialized dependent.",
		includeDeclaration("target", "serialized-target", "global", target.Version, target.ContentHash, ""),
		"<!-- mcpx:include target -->\n")
	publishStarted := make(chan struct{})
	publishDone := make(chan error, 1)
	go func() {
		close(publishStarted)
		_, publishErr := reg.Publish(ctx, skillregistry.PublishOptions{Name: "serialized-root", Body: rootBody})
		publishDone <- publishErr
	}()
	<-publishStarted
	select {
	case publishErr := <-publishDone:
		t.Fatalf("publish completed while delete mutation was paused: %v", publishErr)
	case <-time.After(150 * time.Millisecond):
		// Expected: Publish is waiting on Registry.mutationMu.
	}

	close(releaseDelete)
	select {
	case deleteErr := <-deleteDone:
		if deleteErr != nil {
			t.Fatalf("delete: %v", deleteErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delete did not finish after guard release")
	}
	select {
	case publishErr := <-publishDone:
		if publishErr == nil || !strings.Contains(publishErr.Error(), "composition") {
			t.Fatalf("publish after dependency delete error = %v, want composition failure", publishErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publish did not resume after delete")
	}
	if _, err := reg.Get(ctx, skillregistry.GlobalScope(), "serialized-root", skillregistry.VersionRef{Latest: true}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("racing dependent was inserted: %v", err)
	}
}
