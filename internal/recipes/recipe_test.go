package recipes

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestRank(t *testing.T) {
	now := time.Now()
	r := &store.Recipe{
		TotalCount:   100,
		SuccessCount: 95,
		ErrorRate:    0.05,
		SessionCount: 10,
		LastUsedAt:   &now,
	}
	score := Rank(r, now)
	if score <= 0 || score > 1 {
		t.Fatalf("rank score %f out of [0,1]", score)
	}
}

func TestExtractNamespace(t *testing.T) {
	if got := extractNamespace("github__list_issues"); got != "github" {
		t.Errorf("got %q want github", got)
	}
	if got := extractNamespace("no_ns"); got != "other" {
		t.Errorf("got %q want other", got)
	}
}
