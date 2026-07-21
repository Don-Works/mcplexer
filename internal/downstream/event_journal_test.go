package downstream

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestJournalRegistrySinceAndBatch(t *testing.T) {
	r := newJournalRegistry()
	key1 := InstanceKey{ServerID: "s1", AuthScopeID: "scope-a"}
	key2 := InstanceKey{ServerID: "s2"}

	r.append(key1, "notifications/progress", json.RawMessage(`{"progress":25}`))
	r.append(key1, "notifications/progress", json.RawMessage(`{"progress":50}`))
	r.append(key2, "notifications/tools/list_changed", nil)

	st := r.since(key1, 0, 10, nil)
	if st.ServerID != "s1" || st.AuthScopeID != "scope-a" {
		t.Fatalf("stream identity = %s/%s, want s1/scope-a", st.ServerID, st.AuthScopeID)
	}
	if st.HeadSeq != 2 {
		t.Fatalf("HeadSeq = %d, want 2", st.HeadSeq)
	}
	if len(st.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(st.Events))
	}
	if st.Events[0].Seq != 1 || st.Events[1].Seq != 2 {
		t.Fatalf("seqs = %d,%d want 1,2", st.Events[0].Seq, st.Events[1].Seq)
	}
	if string(st.Events[1].Params) != `{"progress":50}` {
		t.Fatalf("params = %s, want progress 50", string(st.Events[1].Params))
	}

	filtered := r.since(key1, 1, 10, normalizeMethodFilter([]string{"NOTIFICATIONS/PROGRESS"}))
	if len(filtered.Events) != 1 {
		t.Fatalf("filtered events = %d, want 1", len(filtered.Events))
	}
	if filtered.Events[0].Seq != 2 {
		t.Fatalf("filtered seq = %d, want 2", filtered.Events[0].Seq)
	}

	streams := r.batch([]EventBatchRequest{
		{ServerID: "s1", AuthScopeID: "scope-a", SinceSeq: 0},
		{ServerID: "s2", SinceSeq: 0},
	}, 10, nil)
	if len(streams) != 2 {
		t.Fatalf("batch streams = %d, want 2", len(streams))
	}
	if len(streams[0].Events) != 2 || len(streams[1].Events) != 1 {
		t.Fatalf("batch event counts = %d,%d want 2,1", len(streams[0].Events), len(streams[1].Events))
	}
}

func TestJournalRegistryRingTruncates(t *testing.T) {
	r := newJournalRegistry()
	key := InstanceKey{ServerID: "s1"}
	j := r.journalFor(key)
	j.mu.Lock()
	j.cap = 3
	j.mu.Unlock()

	r.append(key, "n1", nil)
	r.append(key, "n2", nil)
	r.append(key, "n3", nil)
	r.append(key, "n4", nil)

	st := r.since(key, 0, 10, nil)
	if !st.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if len(st.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(st.Events))
	}
	if st.Events[0].Method != "n2" || st.Events[2].Method != "n4" {
		t.Fatalf("methods = %q..%q, want n2..n4", st.Events[0].Method, st.Events[2].Method)
	}

	notTruncated := r.since(key, 1, 10, nil)
	if notTruncated.Truncated {
		t.Fatal("Truncated = true for since_seq at retained boundary")
	}
}

func TestJournalRegistryLimitNormalization(t *testing.T) {
	r := newJournalRegistry()
	key := InstanceKey{ServerID: "s1"}
	for i := 0; i < 5; i++ {
		r.append(key, "notifications/progress", nil)
	}

	st := r.since(key, 0, 2, nil)
	if len(st.Events) != 2 {
		t.Fatalf("events = %d, want limit 2", len(st.Events))
	}

	defaulted := r.since(key, 0, 0, nil)
	if len(defaulted.Events) != 5 {
		t.Fatalf("defaulted events = %d, want 5", len(defaulted.Events))
	}
}

func TestJournalRegistryWaitReturnsMatchingEvent(t *testing.T) {
	r := newJournalRegistry()
	key := InstanceKey{ServerID: "s1"}

	go func() {
		time.Sleep(10 * time.Millisecond)
		r.append(key, "notifications/progress", json.RawMessage(`{"progress":1}`))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	st, timedOut := r.wait(ctx, key, 0, time.Second, 10, nil)
	if timedOut {
		t.Fatal("timedOut = true, want false")
	}
	if len(st.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(st.Events))
	}
	if st.Events[0].Method != "notifications/progress" {
		t.Fatalf("method = %q, want notifications/progress", st.Events[0].Method)
	}
}

func TestJournalRegistryWaitHonorsMethodFilter(t *testing.T) {
	r := newJournalRegistry()
	key := InstanceKey{ServerID: "s1"}

	go func() {
		time.Sleep(10 * time.Millisecond)
		r.append(key, "notifications/tools/list_changed", nil)
		time.Sleep(10 * time.Millisecond)
		r.append(key, "notifications/progress", json.RawMessage(`{"progress":1}`))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	st, timedOut := r.wait(
		ctx, key, 0, time.Second, 10,
		normalizeMethodFilter([]string{"notifications/progress"}),
	)
	if timedOut {
		t.Fatal("timedOut = true, want false")
	}
	if len(st.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(st.Events))
	}
	if st.Events[0].Method != "notifications/progress" {
		t.Fatalf("method = %q, want notifications/progress", st.Events[0].Method)
	}
}

func TestJournalRegistryWaitTimeout(t *testing.T) {
	r := newJournalRegistry()
	key := InstanceKey{ServerID: "s1"}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	st, timedOut := r.wait(ctx, key, 0, 10*time.Millisecond, 10, nil)
	if !timedOut {
		t.Fatal("timedOut = false, want true")
	}
	if len(st.Events) != 0 {
		t.Fatalf("events = %d, want 0", len(st.Events))
	}
}

func TestBoundedEventParamsTruncatesLargePayload(t *testing.T) {
	r := newJournalRegistry()
	key := InstanceKey{ServerID: "s1"}
	large := json.RawMessage(`{"payload":"` + strings.Repeat("x", maxEventParamBytes+100) + `"}`)

	ev := r.append(key, "notifications/progress", large)
	if !ev.ParamsTruncated {
		t.Fatal("ParamsTruncated = false, want true")
	}
	if ev.ParamsBytes != len(large) {
		t.Fatalf("ParamsBytes = %d, want %d", ev.ParamsBytes, len(large))
	}

	var payload map[string]any
	if err := json.Unmarshal(ev.Params, &payload); err != nil {
		t.Fatalf("truncated params are not valid JSON: %v", err)
	}
	if payload["truncated"] != true {
		t.Fatalf("truncated marker = %v, want true", payload["truncated"])
	}
	if int(payload["bytes"].(float64)) != len(large) {
		t.Fatalf("bytes marker = %v, want %d", payload["bytes"], len(large))
	}
}
