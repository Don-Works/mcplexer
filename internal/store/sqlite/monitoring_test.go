package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// seedRemoteHost creates a valid host for source/channel tests.
func seedRemoteHost(t *testing.T, db interface {
	CreateRemoteHost(ctx context.Context, h *store.RemoteHost) error
}, ctx context.Context, wsID, scopeID string) *store.RemoteHost {
	t.Helper()
	h := &store.RemoteHost{
		WorkspaceID: wsID, Name: "ip-prod-1", SSHUser: "logwatch",
		SSHHost: "100.64.0.3", AuthScopeID: scopeID, Enabled: true,
	}
	if err := db.CreateRemoteHost(ctx, h); err != nil {
		t.Fatalf("seed remote host: %v", err)
	}
	return h
}

// TestRemoteHostCRUD walks one host row through every operation the
// admin surface depends on: defaults, get, list, update, pin, delete.
func TestRemoteHostCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	h := seedRemoteHost(t, db, ctx, wsID, scopeID)
	if h.ID == "" {
		t.Fatal("expected generated ID")
	}
	if h.SSHPort != 22 {
		t.Fatalf("expected default port 22, got %d", h.SSHPort)
	}

	got, err := db.GetRemoteHost(ctx, h.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "ip-prod-1" || got.SSHHost != "100.64.0.3" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if err := db.SetRemoteHostPin(ctx, h.ID, "SHA256:abcdef"); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	got, _ = db.GetRemoteHost(ctx, h.ID)
	if got.HostKeyPin != "SHA256:abcdef" {
		t.Fatalf("pin not persisted: %q", got.HostKeyPin)
	}

	got.SSHPort = 2222
	if err := db.UpdateRemoteHost(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}

	hosts, err := db.ListRemoteHosts(ctx, wsID)
	if err != nil || len(hosts) != 1 {
		t.Fatalf("list: %v len=%d", err, len(hosts))
	}
	if hosts[0].SSHPort != 2222 {
		t.Fatalf("update not persisted: %d", hosts[0].SSHPort)
	}

	if err := db.DeleteRemoteHost(ctx, h.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetRemoteHost(ctx, h.ID); !errors.Is(err, store.ErrRemoteHostNotFound) {
		t.Fatalf("expected ErrRemoteHostNotFound, got %v", err)
	}
}

// TestRemoteHostValidation rejects hostile SSH coordinates.
func TestRemoteHostValidation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	cases := []struct {
		name string
		host store.RemoteHost
	}{
		{"shell metachars in host", store.RemoteHost{WorkspaceID: wsID, Name: "x", SSHUser: "u", SSHHost: "host;rm -rf /", AuthScopeID: scopeID}},
		{"space in user", store.RemoteHost{WorkspaceID: wsID, Name: "x", SSHUser: "u v", SSHHost: "h", AuthScopeID: scopeID}},
		{"missing auth scope", store.RemoteHost{WorkspaceID: wsID, Name: "x", SSHUser: "u", SSHHost: "h"}},
		{"port out of range", store.RemoteHost{WorkspaceID: wsID, Name: "x", SSHUser: "u", SSHHost: "h", AuthScopeID: scopeID, SSHPort: 70000}},
	}
	for _, tc := range cases {
		h := tc.host
		if err := db.CreateRemoteHost(ctx, &h); err == nil {
			t.Errorf("%s: expected validation error", tc.name)
		}
	}
}

// TestLogSourceCRUD covers defaults, cursor advance + failure counters,
// and the enabled-only collector view.
func TestLogSourceCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	h := seedRemoteHost(t, db, ctx, wsID, scopeID)

	s := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: h.ID,
		Name: "api", Selector: "intervals-api", Enabled: true,
	}
	if err := db.CreateLogSource(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}
	if s.Kind != store.LogSourceKindDocker || s.ScheduleSpec != "2m" ||
		s.MaxPullBytes != 4*1024*1024 || s.RetentionMB != 50 || s.RetentionDays != 7 {
		t.Fatalf("defaults not applied: %+v", s)
	}

	cursor := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	if err := db.UpdateLogSourceCursor(ctx, s.ID, cursor, "tailhash"); err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if err := db.SetLogSourceFailures(ctx, s.ID, 3); err != nil {
		t.Fatalf("failures: %v", err)
	}
	got, err := db.GetLogSource(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CursorTS == nil || !got.CursorTS.Equal(cursor) || got.CursorHash != "tailhash" {
		t.Fatalf("cursor round-trip: %+v %v", got.CursorTS, got.CursorHash)
	}
	if got.ConsecutiveFailures != 3 {
		t.Fatalf("failures: %d", got.ConsecutiveFailures)
	}

	// A successful cursor advance resets the failure counter.
	if err := db.UpdateLogSourceCursor(ctx, s.ID, cursor.Add(time.Minute), "h2"); err != nil {
		t.Fatalf("cursor 2: %v", err)
	}
	got, _ = db.GetLogSource(ctx, s.ID)
	if got.ConsecutiveFailures != 0 {
		t.Fatalf("cursor advance should reset failures, got %d", got.ConsecutiveFailures)
	}

	// Disabled sources drop out of the collector view.
	got.Enabled = false
	if err := db.UpdateLogSource(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	enabled, err := db.ListEnabledLogSources(ctx)
	if err != nil || len(enabled) != 0 {
		t.Fatalf("enabled view: %v len=%d", err, len(enabled))
	}

	// Host delete cascades to sources.
	if err := db.DeleteRemoteHost(ctx, h.ID); err != nil {
		t.Fatalf("delete host: %v", err)
	}
	if _, err := db.GetLogSource(ctx, s.ID); !errors.Is(err, store.ErrLogSourceNotFound) {
		t.Fatalf("expected cascade delete, got %v", err)
	}
}

// TestLogSourceSelectorValidation is the ADR 0007 charset gate.
func TestLogSourceSelectorValidation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	h := seedRemoteHost(t, db, ctx, wsID, scopeID)

	for _, sel := range []string{"", "api; rm -rf /", "a b", "$(whoami)", "x|y", "a&&b", "`id`"} {
		s := &store.LogSource{WorkspaceID: wsID, RemoteHostID: h.ID, Name: "bad", Selector: sel}
		if err := db.CreateLogSource(ctx, s); err == nil {
			t.Errorf("selector %q: expected rejection", sel)
		}
	}
}

// TestMonitoringChannelCRUD covers the secrets rule and severity floor.
func TestMonitoringChannelCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)

	c := &store.MonitoringChannel{
		WorkspaceID: wsID, Name: "incidents", Kind: store.ChannelKindGChatWebhook,
		ConfigJSON: `{"webhook_ref":"secret://GCHAT_WEBHOOK_INCIDENTS"}`,
		Enabled:    true,
	}
	if err := db.CreateMonitoringChannel(ctx, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.MinSeverity != store.SeverityError {
		t.Fatalf("expected default min_severity=error, got %q", c.MinSeverity)
	}

	c.MinSeverity = store.SeverityCritical
	if err := db.UpdateMonitoringChannel(ctx, c); err != nil {
		t.Fatalf("update: %v", err)
	}
	list, err := db.ListMonitoringChannels(ctx, wsID)
	if err != nil || len(list) != 1 || list[0].MinSeverity != store.SeverityCritical {
		t.Fatalf("list: %v %+v", err, list)
	}

	if err := db.DeleteMonitoringChannel(ctx, c.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetMonitoringChannel(ctx, c.ID); !errors.Is(err, store.ErrMonitoringChannelNotFound) {
		t.Fatalf("expected ErrMonitoringChannelNotFound, got %v", err)
	}
}

// TestMonitoringChannelRejectsPlaintext is the never-plaintext gate:
// webhook URLs and credentials must be secret:// refs.
func TestMonitoringChannelRejectsPlaintext(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)

	cases := []struct {
		name string
		ch   store.MonitoringChannel
	}{
		{"plaintext webhook URL", store.MonitoringChannel{
			WorkspaceID: wsID, Name: "bad1", Kind: store.ChannelKindGChatWebhook,
			ConfigJSON: `{"webhook_ref":"https://chat.googleapis.com/v1/spaces/x/messages?key=SECRET"}`}},
		{"missing whatsapp ref", store.MonitoringChannel{
			WorkspaceID: wsID, Name: "bad2", Kind: store.ChannelKindWhatsApp,
			ConfigJSON: `{"chat_id":"447700900000@c.us"}`}},
		{"url smuggled in extra key", store.MonitoringChannel{
			WorkspaceID: wsID, Name: "bad3", Kind: store.ChannelKindMesh,
			ConfigJSON: `{"note":"https://hooks.example.com/t/abc"}`}},
		{"unknown kind", store.MonitoringChannel{
			WorkspaceID: wsID, Name: "bad4", Kind: "email"}},
		{"bad severity", store.MonitoringChannel{
			WorkspaceID: wsID, Name: "bad5", Kind: store.ChannelKindMesh, MinSeverity: "high"}},
	}
	for _, tc := range cases {
		ch := tc.ch
		if err := db.CreateMonitoringChannel(ctx, &ch); err == nil {
			t.Errorf("%s: expected rejection", tc.name)
		}
	}
}

// TestInsertLogLines_ChunksLargeBatch is the regression for the
// "too many SQL variables" failure: a high-volume pull (thousands of
// lines) must insert without blowing SQLite's variable ceiling, and
// every row must land + be window-countable.
func TestInsertLogLines_ChunksLargeBatch(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	h := seedRemoteHost(t, db, ctx, wsID, scopeID)
	s := &store.LogSource{WorkspaceID: wsID, RemoteHostID: h.ID, Name: "api", Selector: "api", Enabled: true}
	if err := db.CreateLogSource(ctx, s); err != nil {
		t.Fatalf("create source: %v", err)
	}
	tpl := &store.LogTemplate{
		ID: "t1", SourceID: s.ID, Masked: "GET / <n>", Severity: store.SeverityInfo,
		FirstSeen: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}
	if _, err := db.UpsertLogTemplate(ctx, tpl, 1); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	const n = 4200 // > 8 chunks, and > the old single-statement variable limit
	base := time.Now().UTC().Add(-time.Minute)
	lines := make([]store.LogLine, n)
	for i := range lines {
		lines[i] = store.LogLine{SourceID: s.ID, TemplateID: "t1", TS: base.Add(time.Duration(i) * time.Millisecond), Line: "GET / 200"}
	}
	if err := db.InsertLogLines(ctx, lines); err != nil {
		t.Fatalf("insert %d lines: %v", n, err)
	}
	counts, err := db.CountLinesByTemplate(ctx, []string{s.ID}, base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts["t1"] != n {
		t.Fatalf("expected %d lines in window, got %d", n, counts["t1"])
	}
}
