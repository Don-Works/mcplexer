package skillregistry_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

func TestDeleteGuardCanProtectPinnedVersions(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()
	publish(t, reg, nil, "guarded", "Use when testing pinned composition references.")
	called := false
	reg.AddDeleteGuard(func(_ context.Context, workspaceID *string, name string, version int) error {
		called = true
		if workspaceID != nil || name != "guarded" || version != 1 {
			t.Fatalf("guard args: workspace=%v name=%q version=%d", workspaceID, name, version)
		}
		return errors.New("referenced by composed-skill@v3")
	})

	err := reg.SoftDelete(ctx, nil, "guarded", 1)
	if !called || err == nil || !strings.Contains(err.Error(), "delete blocked") {
		t.Fatalf("guard result: called=%v err=%v", called, err)
	}
	entry, getErr := reg.Get(ctx, skillregistry.GlobalScope(), "guarded", skillregistry.VersionRef{Latest: true})
	if getErr != nil || entry.Version != 1 {
		t.Fatalf("guarded version changed: entry=%+v err=%v", entry, getErr)
	}
}
