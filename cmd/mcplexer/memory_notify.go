// memory_notify.go — bridge between memory.Service events and the
// dashboard's notify.Bus / Signal tray + /memory page live activity.
//
// Pre-fix the memorySvc.Notify hook was never assigned, so every
// memory__save / __invalidate / __link / pin / offer event was a no-op
// from the UI's point of view: the /memory landing page filters the
// Signal stream by source=="memory" || kind startsWith "memory_", but
// no producer ever emitted such an event. The dashboard's "Live
// activity" widget therefore read "nothing learned yet" even after the
// store had accumulated dozens of memories.
//
// Wiring this adapter at boot fixes the realtime contract:
//   - every memory CUD op (write/invalidate/delete/link/unlink/pin/unpin)
//   - every cross-peer memory offer transition (received/accepted/declined)
//
// flows over the existing /api/v1/notifications/stream SSE channel to
// the same Signal store the /memory page subscribes to via useSignal().
// Auto-reconnect with backoff is handled by useSignalStream; no
// memory-specific SSE endpoint is needed.
//
// All events use Source="memory" so the /memory page filter matches.
// Kind is namespaced "memory_<event>" so the same event also lands in
// the global Signal tray with a clear taxonomy.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/notify"
)

// memoryNotifyAdapter forwards memory.Event into a notify.Bus event
// shaped for the dashboard's Signal tray + the /memory landing page's
// live-activity widget.
type memoryNotifyAdapter struct{ bus *notify.Bus }

// publish converts a memory.Event into a notify.Event and fans it out.
// Safe for nil receiver / nil bus — those drop silently so unit tests
// that build a Service without a daemon still work.
func (a *memoryNotifyAdapter) publish(_ context.Context, ev memory.Event) {
	if a == nil || a.bus == nil {
		return
	}
	title, body, priority := memoryEventCopy(ev)
	a.bus.Publish(notify.Event{
		MessageID: uuid.NewString(),
		Source:    "memory",
		AgentName: "mcplexer",
		Role:      "memory",
		Kind:      "memory_" + ev.Kind,
		Priority:  priority,
		Title:     title,
		Body:      body,
		Tags:      ev.Source,
		Link:      memoryEventLink(ev),
		CreatedAt: time.Now().UTC(),
	})
}

// memoryEventCopy renders human-readable copy for the Signal tray row.
// Compact — the /memory page's ActivityRow truncates anything longer.
func memoryEventCopy(ev memory.Event) (title, body, priority string) {
	name := strings.TrimSpace(ev.MemoryName)
	if name == "" {
		name = strings.TrimSpace(ev.MemoryID)
	}
	switch ev.Kind {
	case "write":
		return memoryWriteTitle(name), "", "normal"
	case "invalidate":
		return "Memory invalidated", name, "normal"
	case "delete":
		if name == "" && ev.Source != "" {
			return "Memories forgotten", "source=" + ev.Source, "normal"
		}
		return "Memory deleted", name, "normal"
	case "link_entity":
		return "Memory linked", name + " → " + ev.EntityKind + ":" + ev.EntityID, "normal"
	case "unlink_entity":
		return "Memory unlinked", name + " ↛ " + ev.EntityKind + ":" + ev.EntityID, "normal"
	case "pin":
		return "Memory pinned", name, "normal"
	case "unpin":
		return "Memory unpinned", name, "normal"
	case "offer_received":
		return "Memory offered by peer", offerSubject(ev), "normal"
	case "offer_accepted":
		return "Memory offer accepted", offerSubject(ev), "normal"
	case "offer_declined":
		return "Memory offer declined", offerSubject(ev), "normal"
	case memory.EventKindPossibleContradiction:
		// The candidate scan is LEXICAL near-duplicate (FTS token overlap) on
		// the no-embedder path — the vector arm only adds more when an embedder
		// is wired. So the copy says "possibly-related", not "contradiction".
		return contradictionCopy(name, ev.Candidates)
	default:
		return "Memory event", ev.Kind, "low"
	}
}

// contradictionCopy renders the possible_contradiction Signal row: the saved
// memory name plus the COUNT of surfaced near-duplicate candidates, so the
// user has a reason to click through to review. Empty candidate sets never
// reach here (surfaceContradictions emits nothing when none are found).
func contradictionCopy(name string, candidates []string) (title, body, priority string) {
	n := len(candidates)
	subject := name
	if subject == "" {
		subject = "new memory"
	}
	return "Possibly-related memories found",
		fmt.Sprintf("%s · %d possibly-related — review for duplicates/conflicts", subject, n),
		"normal"
}

func memoryWriteTitle(name string) string {
	if name == "" {
		return "Memory saved"
	}
	return "Memory saved: " + name
}

// offerSubject builds the body for offer_* events. Prefer peer name
// when set, fall back to peer id. Empty when both are unknown.
func offerSubject(ev memory.Event) string {
	who := strings.TrimSpace(ev.PeerName)
	if who == "" {
		who = strings.TrimSpace(ev.PeerID)
	}
	what := strings.TrimSpace(ev.MemoryName)
	if what == "" {
		what = strings.TrimSpace(ev.MemoryID)
	}
	switch {
	case who != "" && what != "":
		return who + " · " + what
	case who != "":
		return who
	case what != "":
		return what
	default:
		return ""
	}
}

// memoryEventLink picks a deep-link target for the Signal-tray row click.
// Write/invalidate/delete/pin/unpin/link/unlink → memory detail page.
// Offer transitions → the offers landing page.
func memoryEventLink(ev memory.Event) string {
	switch ev.Kind {
	case "offer_received", "offer_accepted", "offer_declined":
		return "/memory/shared"
	case memory.EventKindPossibleContradiction:
		// Deep-link to the saved memory with a review affordance so the click
		// lands on the row whose neighbours need adjudicating.
		if id := strings.TrimSpace(ev.MemoryID); id != "" {
			return "/memory/all?id=" + id + "&review=duplicates"
		}
		return "/memory"
	default:
		if id := strings.TrimSpace(ev.MemoryID); id != "" {
			return "/memory/all?id=" + id
		}
		return "/memory"
	}
}
