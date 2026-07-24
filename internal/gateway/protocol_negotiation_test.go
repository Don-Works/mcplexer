package gateway

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/mcpversion"
)

func TestInitializeProtocolVersionNegotiation(t *testing.T) {
	tests := make(map[string]string)
	for _, version := range mcpversion.Supported() {
		tests[version] = version
	}
	tests[""] = mcpversion.Latest
	tests["2026-01-01"] = mcpversion.Latest
	tests["DRAFT-2026-v1"] = mcpversion.Latest

	for proposed, want := range tests {
		proposed, want := proposed, want
		t.Run("proposed_"+proposed, func(t *testing.T) {
			srv := newInitializationGateServer(t, TransportSocket, &mockStore{})
			params := InitializeParams{
				ProtocolVersion: proposed,
				Capabilities:    ClientCapabilities{},
				ClientInfo: ClientInfo{
					Name:    "protocol-negotiation-test",
					Version: "1.0",
				},
			}
			rawParams, err := json.Marshal(params)
			if err != nil {
				t.Fatalf("marshal initialize params: %v", err)
			}

			resp := dispatchInitializationGateRequest(t, srv, "initialize", rawParams)
			if resp.Error != nil {
				t.Fatalf("initialize failed: %#v", resp.Error)
			}
			var result InitializeResult
			if err := json.Unmarshal(resp.Result, &result); err != nil {
				t.Fatalf("decode initialize result: %v", err)
			}
			if result.ProtocolVersion != want {
				t.Fatalf(
					"protocolVersion = %q, want %q for proposal %q",
					result.ProtocolVersion,
					want,
					proposed,
				)
			}
		})
	}
}

func TestInitializeRetainsOpenClientCapabilities(t *testing.T) {
	srv := newInitializationGateServer(t, TransportSocket, &mockStore{})
	wantCapabilities := ClientCapabilities{
		"roots":         json.RawMessage(`{"listChanged":true}`),
		"elicitation":   json.RawMessage(`{"form":{},"url":{}}`),
		"tasks":         json.RawMessage(`{"list":{},"requests":{"elicitation":{"create":{}}}}`),
		"futureFeature": json.RawMessage(`{"nested":{"enabled":true},"values":[1,2,3]}`),
	}
	params := InitializeParams{
		ProtocolVersion: mcpversion.Latest,
		Capabilities:    wantCapabilities,
		ClientInfo: ClientInfo{
			Name:    "capability-test",
			Version: "1.0",
		},
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal initialize params: %v", err)
	}
	resp := dispatchInitializationGateRequest(t, srv, "initialize", rawParams)
	if resp.Error != nil {
		t.Fatalf("initialize failed: %#v", resp.Error)
	}

	gotVersion, gotCapabilities := srv.handler.sessions.clientNegotiation()
	if gotVersion != mcpversion.Latest {
		t.Fatalf("stored protocol version = %q, want %q", gotVersion, mcpversion.Latest)
	}
	assertCapabilityJSONEqual(t, gotCapabilities, wantCapabilities)

	// Accessors return deep-enough copies: mutating a returned RawMessage must
	// not change the session, even while other readers take snapshots.
	gotCapabilities["futureFeature"][0] = 'x'
	delete(gotCapabilities, "tasks")

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, snapshot := srv.handler.sessions.clientNegotiation()
			if value := snapshot["roots"]; len(value) > 0 {
				value[0] = 'x'
			}
		}()
	}
	wg.Wait()

	_, afterMutation := srv.handler.sessions.clientNegotiation()
	assertCapabilityJSONEqual(t, afterMutation, wantCapabilities)
}

func assertCapabilityJSONEqual(
	t *testing.T,
	got ClientCapabilities,
	want ClientCapabilities,
) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("capability count = %d, want %d", len(got), len(want))
	}
	for name, wantRaw := range want {
		gotRaw, ok := got[name]
		if !ok {
			t.Fatalf("capability %q missing from stored session", name)
		}
		var gotValue, wantValue any
		if err := json.Unmarshal(gotRaw, &gotValue); err != nil {
			t.Fatalf("decode stored capability %q: %v", name, err)
		}
		if err := json.Unmarshal(wantRaw, &wantValue); err != nil {
			t.Fatalf("decode wanted capability %q: %v", name, err)
		}
		gotJSON, _ := json.Marshal(gotValue)
		wantJSON, _ := json.Marshal(wantValue)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("capability %q = %s, want %s", name, gotJSON, wantJSON)
		}
	}
}
