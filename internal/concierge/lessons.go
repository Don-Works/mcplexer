// lessons.go — B5: memory pinning for accepted refinements. When a
// refinement proposal wins its A/B + quorum gate, the concierge derives
// a short one-line "lesson" from the refinement text and pins it as a
// memory under either concierge.lessons:global or
// concierge.lessons:<channel>:<user> (when the friction was
// user-specific).
//
// Lessons are short by design — the concierge prompt loads them into a
// `{concierge_lessons}` placeholder each turn, capped at ~10 per scope,
// recency-weighted. Long prose belongs in the prompt template (which
// gets refined via W3); short reusable rules belong here.
package concierge

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
)

// MemoryServiceForLessons is the narrow memory facade the lesson layer
// needs. *memory.Service implements this naturally.
type MemoryServiceForLessons interface {
	Write(ctx context.Context, opts memory.WriteOptions) (string, error)
	Recall(ctx context.Context, f store.MemoryFilter, query string, k int) ([]store.MemoryHit, error)
}

// PinLessonOptions is the arg payload for PinLesson.
type PinLessonOptions struct {
	// LessonText is the one-line rule the concierge has learned. Required.
	LessonText string
	// SourceRefinementID points at the refinement that promoted this
	// lesson (audit + dedup). Optional but recommended.
	SourceRefinementID string
	// Channel + UserIDExternal scope the lesson. Pass both empty for a
	// global concierge lesson.
	Channel        string
	UserIDExternal string
	// WorkspaceID scopes the underlying memory write. Optional — empty =
	// global memory.
	WorkspaceID string
}

// PinLesson writes the lesson as a `note`-kind memory with the
// concierge.lessons:* scope key encoded into the name. Subsequent
// invocations with the same scope+content dedupe via memory's content
// hash (long-term — for now, multiple identical lessons just accumulate
// and are dropped by the consolidator).
//
// Returns the memory id so the caller can plumb it back into the
// refinement's audit row.
func PinLesson(
	ctx context.Context, svc MemoryServiceForLessons, opts PinLessonOptions,
) (string, error) {
	if svc == nil {
		return "", errors.New("PinLesson: nil memory service")
	}
	lesson := strings.TrimSpace(opts.LessonText)
	if lesson == "" {
		return "", errors.New("PinLesson: lesson_text required")
	}
	// Single line. Lessons that exceed ~280 chars are typically
	// refinement-paragraphs masquerading as lessons; cap and let the
	// consolidator clean it up later.
	if len(lesson) > 280 {
		lesson = lesson[:277] + "…"
	}

	scopeKey := MemoryScopeKeyLessons(opts.Channel, opts.UserIDExternal)
	tags := []string{"concierge", "lesson"}
	if opts.Channel != "" {
		tags = append(tags, "channel:"+opts.Channel)
	}
	if opts.UserIDExternal != "" {
		tags = append(tags, "user:"+opts.UserIDExternal)
	} else {
		tags = append(tags, "scope:global")
	}
	meta := map[string]any{
		"scope_key":        scopeKey,
		"pinned_at":        time.Now().UTC().Format(time.RFC3339),
		"source_refine_id": opts.SourceRefinementID,
		"channel":          opts.Channel,
		"user_external_id": opts.UserIDExternal,
	}
	var wsID *string
	if opts.WorkspaceID != "" {
		w := opts.WorkspaceID
		wsID = &w
	}
	id, err := svc.Write(ctx, memory.WriteOptions{
		Name:        scopeKey,
		Kind:        store.MemoryKindNote,
		Content:     lesson,
		Tags:        tags,
		Metadata:    meta,
		WorkspaceID: wsID,
		Pinned:      true, // protect from the consolidator's auto-prune
		SourceKind:  store.MemorySourceAgent,
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// RecentLessonsFor returns up to `limit` recent lessons applicable to a
// (channel, user) pair: global lessons + per-user lessons. The result
// is the raw lesson text (newline-joined) so a worker prompt can drop
// it straight into `{concierge_lessons}`.
//
// Order: per-user lessons FIRST (most specific wins on conflict), then
// global, both newest-first within their bucket. The cap is shared
// across both buckets — a chatty per-user history doesn't crowd out
// the global rules.
func RecentLessonsFor(
	ctx context.Context, svc MemoryServiceForLessons,
	channel, userExternalID string, limit int,
) (string, error) {
	if svc == nil {
		return "", errors.New("RecentLessonsFor: nil memory service")
	}
	if limit <= 0 {
		limit = 10
	}
	tagFilter := []string{"concierge", "lesson"}
	hits, err := svc.Recall(ctx, store.MemoryFilter{
		Kind: store.MemoryKindNote,
		Tags: tagFilter,
	}, "", limit*2)
	if err != nil {
		return "", err
	}
	// Partition into per-user and global. We stored the scope key in the
	// memory name; the filter doesn't scope by name, only by tags, so
	// post-filter here.
	perUserScope := ""
	if channel != "" && userExternalID != "" {
		perUserScope = MemoryScopeKeyLessons(channel, userExternalID)
	}
	const globalScope = "concierge.lessons:global"
	var perUser, global []string
	for _, h := range hits {
		switch h.Entry.Name {
		case globalScope:
			global = append(global, h.Entry.Content)
		case perUserScope:
			if perUserScope != "" {
				perUser = append(perUser, h.Entry.Content)
			}
		}
	}
	combined := append(perUser, global...)
	if len(combined) > limit {
		combined = combined[:limit]
	}
	return strings.Join(combined, "\n"), nil
}
