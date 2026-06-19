// memory_notify_test.go — round-trip coverage for the memory.Event →
// notify.Event adapter that backs the /memory page's live activity
// widget. The adapter must:
//
//   - emit Source="memory" so MemoryLandingPage.isMemoryEvent matches
//   - prefix Kind with "memory_" so the Signal-tray taxonomy stays clean
//   - render a human-readable title + body for every Kind in the taxonomy
//   - link write/invalidate/delete/pin/link → memory detail
//   - link offer_* → /memory/shared
//
// Without these, signals reach the SSE channel but render as "unknown
// event" rows — exactly the symptom the pre-fix dashboard exhibited.
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/notify"
)

func newRealBus() *notify.Bus { return notify.NewBus() }

func TestMemoryNotifyAdapterPublishesAllKinds(t *testing.T) {
	bus := newRealBus()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)
	a := &memoryNotifyAdapter{bus: bus}

	cases := []struct {
		name      string
		ev        memory.Event
		wantKind  string
		wantTitle string
		linkHas   string
	}{
		{
			name:      "write",
			ev:        memory.Event{Kind: "write", MemoryID: "m1", MemoryName: "preferred-editor"},
			wantKind:  "memory_write",
			wantTitle: "Memory saved: preferred-editor",
			linkHas:   "/memory/all?id=m1",
		},
		{
			name:      "invalidate",
			ev:        memory.Event{Kind: "invalidate", MemoryID: "m1", MemoryName: "preferred-editor"},
			wantKind:  "memory_invalidate",
			wantTitle: "Memory invalidated",
			linkHas:   "/memory/all?id=m1",
		},
		{
			name:      "delete",
			ev:        memory.Event{Kind: "delete", MemoryID: "m1"},
			wantKind:  "memory_delete",
			wantTitle: "Memory deleted",
			linkHas:   "/memory/all?id=m1",
		},
		{
			name: "link_entity",
			ev: memory.Event{
				Kind: "link_entity", MemoryID: "m1", MemoryName: "n",
				EntityKind: "task", EntityID: "T-1",
			},
			wantKind:  "memory_link_entity",
			wantTitle: "Memory linked",
			linkHas:   "/memory/all?id=m1",
		},
		{
			name: "unlink_entity",
			ev: memory.Event{
				Kind: "unlink_entity", MemoryID: "m1", MemoryName: "n",
				EntityKind: "task", EntityID: "T-1",
			},
			wantKind:  "memory_unlink_entity",
			wantTitle: "Memory unlinked",
			linkHas:   "/memory/all?id=m1",
		},
		{
			name:      "pin",
			ev:        memory.Event{Kind: "pin", MemoryID: "m1", MemoryName: "n"},
			wantKind:  "memory_pin",
			wantTitle: "Memory pinned",
			linkHas:   "/memory/all?id=m1",
		},
		{
			name:      "unpin",
			ev:        memory.Event{Kind: "unpin", MemoryID: "m1", MemoryName: "n"},
			wantKind:  "memory_unpin",
			wantTitle: "Memory unpinned",
			linkHas:   "/memory/all?id=m1",
		},
		{
			name: "offer_received",
			ev: memory.Event{
				Kind: "offer_received", OfferID: "off-1",
				PeerID: "peer-1", PeerName: "Alice", MemoryName: "secret-recipe",
			},
			wantKind:  "memory_offer_received",
			wantTitle: "Memory offered by peer",
			linkHas:   "/memory/shared",
		},
		{
			name: "offer_accepted",
			ev: memory.Event{
				Kind: "offer_accepted", OfferID: "off-1",
				PeerID: "peer-1", MemoryID: "m-new",
			},
			wantKind:  "memory_offer_accepted",
			wantTitle: "Memory offer accepted",
			linkHas:   "/memory/shared",
		},
		{
			name:      "offer_declined",
			ev:        memory.Event{Kind: "offer_declined", OfferID: "off-2", PeerID: "peer-2"},
			wantKind:  "memory_offer_declined",
			wantTitle: "Memory offer declined",
			linkHas:   "/memory/shared",
		},
		{
			name: "possible_contradiction",
			ev: memory.Event{
				Kind: memory.EventKindPossibleContradiction, MemoryID: "m9",
				MemoryName: "deploy-policy", Candidates: []string{"m1", "m2"},
			},
			wantKind:  "memory_possible_contradiction",
			wantTitle: "Possibly-related memories found",
			linkHas:   "/memory/all?id=m9&review=duplicates",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a.publish(context.Background(), tc.ev)
			got := <-ch
			if got.Source != "memory" {
				t.Errorf("Source: want memory, got %q", got.Source)
			}
			if got.Kind != tc.wantKind {
				t.Errorf("Kind: want %q, got %q", tc.wantKind, got.Kind)
			}
			if got.Title != tc.wantTitle {
				t.Errorf("Title: want %q, got %q", tc.wantTitle, got.Title)
			}
			if !strings.Contains(got.Link, tc.linkHas) {
				t.Errorf("Link: want contains %q, got %q", tc.linkHas, got.Link)
			}
			if got.MessageID == "" {
				t.Errorf("MessageID empty — the dashboard requires this to dedupe rows")
			}
		})
	}
}

// TestMemoryNotifyContradictionBodyCarriesCount proves the
// possible_contradiction case renders the CANDIDATE COUNT in the body (not the
// generic "Memory event" fallback) so the /memory page has a reason to surface
// the row — the half-wired symptom this fixes was the event producing nothing
// the UI could read. Copy is "possibly-related", not "contradiction", because
// the no-embedder scan is lexical-only.
func TestMemoryNotifyContradictionBodyCarriesCount(t *testing.T) {
	bus := newRealBus()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)
	a := &memoryNotifyAdapter{bus: bus}

	a.publish(context.Background(), memory.Event{
		Kind: memory.EventKindPossibleContradiction, MemoryID: "m9",
		MemoryName: "deploy-policy", Candidates: []string{"m1", "m2", "m3"},
	})
	got := <-ch
	if !strings.Contains(got.Body, "3") {
		t.Errorf("body should carry the candidate count 3, got %q", got.Body)
	}
	if !strings.Contains(got.Body, "deploy-policy") {
		t.Errorf("body should name the saved memory, got %q", got.Body)
	}
	if !strings.Contains(got.Body, "possibly-related") {
		t.Errorf("body should say possibly-related (lexical scan, not contradiction), got %q", got.Body)
	}
	if strings.Contains(strings.ToLower(got.Body), "contradiction") {
		t.Errorf("body must not assert a contradiction (scan is lexical), got %q", got.Body)
	}
}

// TestMemoryNotifyAdapterIsNilSafe confirms the adapter doesn't panic
// when bus or receiver is nil. Pre-fix the daemon would have crashed on
// every memory write if the notify bus wasn't constructed yet (e.g.
// admin-CLI path); nil-safe means the same Service plug-in surface
// works without a daemon (unit tests, MCP-only embed).
func TestMemoryNotifyAdapterIsNilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil bus panicked: %v", r)
		}
	}()
	var nilA *memoryNotifyAdapter
	nilA.publish(context.Background(), memory.Event{Kind: "write"})
	(&memoryNotifyAdapter{bus: nil}).publish(context.Background(), memory.Event{Kind: "write"})
}
