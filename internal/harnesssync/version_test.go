package harnesssync

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestUsingMcplexerRegistryVersion_HeadAndFallback(t *testing.T) {
	ctx := context.Background()
	if got := UsingMcplexerRegistryVersion(ctx, nil); got != DefaultUsingMcplexerVersion {
		t.Errorf("nil registry = %d, want default %d", got, DefaultUsingMcplexerVersion)
	}

	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "ver.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	reg := skillregistry.New(db)
	if got := UsingMcplexerRegistryVersion(ctx, reg); got != DefaultUsingMcplexerVersion {
		t.Errorf("empty registry = %d, want default", got)
	}

	seed, err := skillregistry.SeedBody(usingMcplexerSkillName)
	if err != nil {
		t.Fatalf("SeedBody: %v", err)
	}
	if _, err := reg.Publish(ctx, skillregistry.PublishOptions{
		Name: usingMcplexerSkillName, Body: seed, Author: "system",
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := UsingMcplexerRegistryVersion(ctx, reg); got != 1 {
		t.Errorf("head version = %d, want 1", got)
	}
}
