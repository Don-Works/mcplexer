package gateway

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

// --- Test doubles ---

// mockToolLister implements ToolLister for testing.
type mockToolLister struct {
	tools  map[string]json.RawMessage
	err    error
	listed chan []string

	listRequests [][]string

	callCount int
	lastCall  struct {
		serverID    string
		authScopeID string
		toolName    string
		args        json.RawMessage
	}
}

func (m *mockToolLister) ListAllTools(_ context.Context) (map[string]json.RawMessage, error) {
	return m.tools, m.err
}

func (m *mockToolLister) ListToolsForServers(_ context.Context, serverIDs []string) (map[string]json.RawMessage, error) {
	req := append([]string(nil), serverIDs...)
	m.listRequests = append(m.listRequests, req)
	if m.listed != nil {
		select {
		case m.listed <- req:
		default:
		}
	}
	result := make(map[string]json.RawMessage)
	for _, id := range serverIDs {
		if tools, ok := m.tools[id]; ok {
			result[id] = tools
		}
	}
	return result, m.err
}

func (m *mockToolLister) Call(_ context.Context, serverID, authScopeID, toolName string, args json.RawMessage) (json.RawMessage, error) {
	m.callCount++
	m.lastCall.serverID = serverID
	m.lastCall.authScopeID = authScopeID
	m.lastCall.toolName = toolName
	m.lastCall.args = args
	return nil, nil
}

type prefetchToolLister struct {
	mockToolLister
	prefetched chan string
}

func (m *prefetchToolLister) EnsureRunning(_ context.Context, serverID, _ string) {
	if m.prefetched == nil {
		return
	}
	m.prefetched <- serverID
}

type reloadTrackingToolLister struct {
	mockToolLister
	events []string
}

func (m *reloadTrackingToolLister) ReloadServerInstances(serverID string) int {
	m.events = append(m.events, "reload:"+serverID)
	return 1
}

func (m *reloadTrackingToolLister) ListToolsForServers(ctx context.Context, serverIDs []string) (map[string]json.RawMessage, error) {
	m.events = append(m.events, "list:"+strings.Join(serverIDs, ","))
	return m.mockToolLister.ListToolsForServers(ctx, serverIDs)
}

// mockStore implements store.Store with minimal stubs for handler tests.
type mockStore struct {
	servers          []store.DownstreamServer
	capUpdates       map[string]json.RawMessage
	settings         json.RawMessage
	workspaces       []mockWorkspace
	routeRules       map[string][]store.RouteRule // keyed by workspace ID
	skillInvocations []store.SkillInvocation
}

// mockWorkspace is a lightweight workspace definition for tests.
type mockWorkspace struct {
	id       string
	rootPath string
	parentID string
	// tags is the raw JSON tags blob propagated into store.Workspace.Tags.
	// Left nil for workspaces without tags; set to e.g.
	// json.RawMessage(`["admin-trusted"]`) to exercise the admin-trusted gate.
	tags json.RawMessage
}

func (m *mockStore) GetDownstreamServersByIDs(_ context.Context, _ []string) ([]store.DownstreamServer, error) {
	return nil, nil
}
func (m *mockStore) ListDownstreamServers(_ context.Context) ([]store.DownstreamServer, error) {
	return m.servers, nil
}

func (m *mockStore) UpdateCapabilitiesCache(_ context.Context, id string, cache json.RawMessage) error {
	if m.capUpdates != nil {
		m.capUpdates[id] = cache
	}
	return nil
}

// Stubs — HarnessInitStore.
func (m *mockStore) RecordHarnessInitialize(_ context.Context, _, _ string) error { return nil }
func (m *mockStore) GetHarnessInitialization(_ context.Context, _ string) (*store.HarnessInitialization, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListHarnessInitializations(_ context.Context) ([]store.HarnessInitialization, error) {
	return nil, nil
}
func (m *mockStore) UpsertHarnessBootstrap(_ context.Context, _ *store.HarnessInitialization) error {
	return nil
}

// Stubs — WorkspaceStore.
func (m *mockStore) CreateWorkspace(_ context.Context, _ *store.Workspace) error { return nil }
func (m *mockStore) GetWorkspace(_ context.Context, _ string) (*store.Workspace, error) {
	return nil, nil
}
func (m *mockStore) GetWorkspaceByName(_ context.Context, _ string) (*store.Workspace, error) {
	return nil, nil
}
func (m *mockStore) ListWorkspaces(_ context.Context) ([]store.Workspace, error) {
	out := make([]store.Workspace, len(m.workspaces))
	for i, w := range m.workspaces {
		out[i] = store.Workspace{ID: w.id, RootPath: w.rootPath, ParentID: w.parentID, Tags: w.tags}
	}
	return out, nil
}
func (m *mockStore) UpdateWorkspace(_ context.Context, _ *store.Workspace) error { return nil }
func (m *mockStore) DeleteWorkspace(_ context.Context, _ string) error           { return nil }

// Stubs — AuthScopeStore.
func (m *mockStore) CreateAuthScope(_ context.Context, _ *store.AuthScope) error { return nil }
func (m *mockStore) GetAuthScope(_ context.Context, _ string) (*store.AuthScope, error) {
	return nil, nil
}
func (m *mockStore) GetAuthScopeByName(_ context.Context, _ string) (*store.AuthScope, error) {
	return nil, nil
}
func (m *mockStore) ListAuthScopes(_ context.Context) ([]store.AuthScope, error)          { return nil, nil }
func (m *mockStore) UpdateAuthScope(_ context.Context, _ *store.AuthScope) error          { return nil }
func (m *mockStore) DeleteAuthScope(_ context.Context, _ string) error                    { return nil }
func (m *mockStore) UpdateAuthScopeTokenData(_ context.Context, _ string, _ []byte) error { return nil }
func (m *mockStore) UpdateAuthScopeEncryptedData(_ context.Context, _ string, _ []byte) error {
	return nil
}

// Stubs — ModelProfileStore.
func (m *mockStore) ListModelProfiles(_ context.Context) ([]store.ModelProfile, error) {
	return nil, nil
}
func (m *mockStore) GetModelProfile(_ context.Context, _ string) (store.ModelProfile, error) {
	return store.ModelProfile{}, nil
}
func (m *mockStore) GetModelProfileByName(_ context.Context, _ string) (store.ModelProfile, error) {
	return store.ModelProfile{}, nil
}
func (m *mockStore) CreateModelProfile(_ context.Context, _ *store.ModelProfile) error { return nil }
func (m *mockStore) UpdateModelProfile(_ context.Context, _ *store.ModelProfile) error { return nil }
func (m *mockStore) DeleteModelProfile(_ context.Context, _ string) error              { return nil }

// Stubs — OAuthProviderStore.
func (m *mockStore) CreateOAuthProvider(_ context.Context, _ *store.OAuthProvider) error { return nil }
func (m *mockStore) GetOAuthProvider(_ context.Context, _ string) (*store.OAuthProvider, error) {
	return nil, nil
}
func (m *mockStore) GetOAuthProviderByName(_ context.Context, _ string) (*store.OAuthProvider, error) {
	return nil, nil
}
func (m *mockStore) ListOAuthProviders(_ context.Context) ([]store.OAuthProvider, error) {
	return nil, nil
}
func (m *mockStore) UpdateOAuthProvider(_ context.Context, _ *store.OAuthProvider) error { return nil }
func (m *mockStore) DeleteOAuthProvider(_ context.Context, _ string) error               { return nil }

// Stubs — DownstreamServerStore (remaining).
func (m *mockStore) CreateDownstreamServer(_ context.Context, _ *store.DownstreamServer) error {
	return nil
}
func (m *mockStore) GetDownstreamServer(_ context.Context, id string) (*store.DownstreamServer, error) {
	for i := range m.servers {
		if m.servers[i].ID == id {
			return &m.servers[i], nil
		}
	}
	return nil, nil
}
func (m *mockStore) GetDownstreamServerByName(_ context.Context, _ string) (*store.DownstreamServer, error) {
	return nil, nil
}
func (m *mockStore) UpdateDownstreamServer(_ context.Context, _ *store.DownstreamServer) error {
	return nil
}
func (m *mockStore) DeleteDownstreamServer(_ context.Context, _ string) error { return nil }

// Stubs — RouteRuleStore.
func (m *mockStore) CreateRouteRule(_ context.Context, _ *store.RouteRule) error { return nil }
func (m *mockStore) GetRouteRule(_ context.Context, _ string) (*store.RouteRule, error) {
	return nil, nil
}
func (m *mockStore) ListRouteRules(_ context.Context, wsID string) ([]store.RouteRule, error) {
	if m.routeRules != nil {
		return m.routeRules[wsID], nil
	}
	return nil, nil
}
func (m *mockStore) UpdateRouteRule(_ context.Context, _ *store.RouteRule) error { return nil }
func (m *mockStore) DeleteRouteRule(_ context.Context, _ string) error           { return nil }

// Stubs — SessionStore.
func (m *mockStore) CreateSession(_ context.Context, _ *store.Session) error          { return nil }
func (m *mockStore) GetSession(_ context.Context, _ string) (*store.Session, error)   { return nil, nil }
func (m *mockStore) DisconnectSession(_ context.Context, _ string) error              { return nil }
func (m *mockStore) DisconnectAllSessions(_ context.Context) (int, error)             { return 0, nil }
func (m *mockStore) ListActiveSessions(_ context.Context) ([]store.Session, error)    { return nil, nil }
func (m *mockStore) CleanupStaleSessions(_ context.Context, _ time.Time) (int, error) { return 0, nil }

// Stubs — AuditStore.
func (m *mockStore) InsertAuditRecord(_ context.Context, _ *store.AuditRecord) error { return nil }
func (m *mockStore) QueryAuditRecords(_ context.Context, _ store.AuditFilter) ([]store.AuditRecord, int, error) {
	return nil, 0, nil
}
func (m *mockStore) GetAuditStats(_ context.Context, _ string, _, _ time.Time) (*store.AuditStats, error) {
	return nil, nil
}
func (m *mockStore) GetDashboardTimeSeries(_ context.Context, _, _ time.Time) ([]store.TimeSeriesPoint, error) {
	return nil, nil
}
func (m *mockStore) GetDashboardTimeSeriesBucketed(_ context.Context, _, _ time.Time, _ int) ([]store.TimeSeriesPoint, error) {
	return nil, nil
}
func (m *mockStore) GetToolLeaderboard(_ context.Context, _, _ time.Time, _ int) ([]store.ToolLeaderboardEntry, error) {
	return nil, nil
}
func (m *mockStore) GetServerHealth(_ context.Context, _, _ time.Time) ([]store.ServerHealthEntry, error) {
	return nil, nil
}
func (m *mockStore) GetErrorBreakdown(_ context.Context, _, _ time.Time, _ int) ([]store.ErrorBreakdownEntry, error) {
	return nil, nil
}
func (m *mockStore) GetRouteHitMap(_ context.Context, _, _ time.Time) ([]store.RouteHitEntry, error) {
	return nil, nil
}
func (m *mockStore) GetAuditCacheStats(_ context.Context, _, _ time.Time) (*store.AuditCacheStats, error) {
	return nil, nil
}

// Stubs — ToolApprovalStore.
func (m *mockStore) CreateToolApproval(_ context.Context, _ *store.ToolApproval) error { return nil }
func (m *mockStore) GetToolApproval(_ context.Context, _ string) (*store.ToolApproval, error) {
	return nil, nil
}
func (m *mockStore) ListPendingApprovals(_ context.Context) ([]store.ToolApproval, error) {
	return nil, nil
}
func (m *mockStore) ResolveToolApproval(_ context.Context, _, _, _, _, _ string) error { return nil }
func (m *mockStore) ExpirePendingApprovals(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}
func (m *mockStore) GetApprovalMetrics(_ context.Context, _, _ time.Time) (*store.ApprovalMetrics, error) {
	return nil, nil
}

// Stubs — SettingsStore.
func (m *mockStore) GetSettings(_ context.Context) (json.RawMessage, error) {
	if len(m.settings) > 0 {
		return m.settings, nil
	}
	return json.RawMessage("{}"), nil
}
func (m *mockStore) UpdateSettings(_ context.Context, data json.RawMessage) error {
	m.settings = data
	return nil
}

// Stubs — MeshStore.
func (m *mockStore) InsertMeshMessage(context.Context, *store.MeshMessage) error { return nil }
func (m *mockStore) QueryMeshMessages(context.Context, store.MeshMessageFilter) ([]store.MeshMessage, error) {
	return nil, nil
}
func (m *mockStore) GetMeshMessage(context.Context, string) (*store.MeshMessage, error) {
	return nil, nil
}
func (m *mockStore) IncrementReplyCount(context.Context, string) error              { return nil }
func (m *mockStore) ExtendMessageExpiry(context.Context, string, time.Time) error   { return nil }
func (m *mockStore) ArchiveExpiredMessages(context.Context, time.Time) (int, error) { return 0, nil }
func (m *mockStore) ArchiveMessagesBySenderAndKinds(context.Context, []string, []string) (int, error) {
	return 0, nil
}
func (m *mockStore) ArchiveOldWorkerFindings(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (m *mockStore) DeleteArchivedMessages(context.Context, time.Time) (int, error) { return 0, nil }
func (m *mockStore) CountLiveMessages(context.Context, string) (int, error)         { return 0, nil }
func (m *mockStore) ArchiveLowestPriority(context.Context, string, int) (int, error) {
	return 0, nil
}
func (m *mockStore) UpsertMeshAgent(context.Context, *store.MeshAgent) error { return nil }
func (m *mockStore) SetMeshAgentStatus(context.Context, string, string, time.Time) error {
	return nil
}
func (m *mockStore) SetMeshAgentTerminalLocator(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (m *mockStore) FindRecentLocalAgentByClient(context.Context, string, string, string) (*store.MeshAgent, error) {
	return nil, nil
}
func (m *mockStore) GetMeshAgent(context.Context, string) (*store.MeshAgent, error) {
	return nil, nil
}
func (m *mockStore) ListActiveMeshAgents(context.Context, string, time.Time) ([]store.MeshAgent, error) {
	return nil, nil
}
func (m *mockStore) UpdateAgentCursor(context.Context, string, string) error { return nil }
func (m *mockStore) TouchMeshAgent(context.Context, string) error            { return nil }
func (m *mockStore) DeleteMeshAgent(context.Context, string) error           { return nil }
func (m *mockStore) DeleteMeshAgentsByOrigin(context.Context, string) (int, error) {
	return 0, nil
}

// Stubs — FileClaimStore (M7.4).
func (m *mockStore) InsertFileClaim(context.Context, *store.FileClaim) error       { return nil }
func (m *mockStore) UpsertRemoteFileClaim(context.Context, *store.FileClaim) error { return nil }
func (m *mockStore) GetFileClaim(context.Context, string) (*store.FileClaim, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListFileClaims(context.Context, store.FileClaimFilter) ([]store.FileClaim, error) {
	return nil, nil
}
func (m *mockStore) ReleaseFileClaim(context.Context, string, time.Time) error { return nil }

// Stubs — ToolDescriptionStore.
func (m *mockStore) CreateToolDescriptionVersion(context.Context, *store.ToolDescriptionVersion) error {
	return nil
}
func (m *mockStore) GetToolDescriptionVersion(context.Context, string) (*store.ToolDescriptionVersion, error) {
	return nil, nil
}
func (m *mockStore) ListToolDescriptionVersions(context.Context, store.ToolDescriptionFilter) ([]store.ToolDescriptionVersion, int, error) {
	return nil, 0, nil
}
func (m *mockStore) GetActiveDescriptions(context.Context) (map[string]string, error) {
	return nil, nil
}
func (m *mockStore) ActivateVersion(context.Context, string, string, string) error { return nil }
func (m *mockStore) RejectVersion(context.Context, string, string, string) error   { return nil }
func (m *mockStore) HasPendingForToolBySession(context.Context, string, string) (bool, error) {
	return false, nil
}

// Stubs — SkillInvocationStore.
func (m *mockStore) InsertSkillInvocation(_ context.Context, inv *store.SkillInvocation) error {
	if m.skillInvocations == nil {
		m.skillInvocations = make([]store.SkillInvocation, 0)
	}
	m.skillInvocations = append(m.skillInvocations, *inv)
	return nil
}
func (m *mockStore) ListSkillInvocations(_ context.Context, f store.SkillInvocationFilter) ([]store.SkillInvocation, error) {
	out := make([]store.SkillInvocation, 0)
	for _, inv := range m.skillInvocations {
		if f.SkillName != nil && inv.SkillName != *f.SkillName {
			continue
		}
		if f.Allowed != nil && inv.Allowed != *f.Allowed {
			continue
		}
		out = append(out, inv)
	}
	return out, nil
}

// Stubs — Store top-level.
func (m *mockStore) Tx(_ context.Context, _ func(store.Store) error) error { return nil }
func (m *mockStore) Ping(_ context.Context) error                          { return nil }
func (m *mockStore) Close() error                                          { return nil }

// Stubs — TelegramStore.
func (m *mockStore) UpsertTelegramChat(context.Context, *store.TelegramChat) error { return nil }
func (m *mockStore) GetTelegramChat(context.Context, string) (*store.TelegramChat, error) {
	return nil, nil
}
func (m *mockStore) GetTelegramChatByNative(context.Context, string, string) (*store.TelegramChat, error) {
	return nil, nil
}
func (m *mockStore) ListTelegramChats(context.Context) ([]store.TelegramChat, error) { return nil, nil }
func (m *mockStore) ListActiveTelegramChatsByWorkspace(context.Context, string) ([]store.TelegramChat, error) {
	return nil, nil
}
func (m *mockStore) UpdateTelegramChatMinPriority(context.Context, string, string) error { return nil }
func (m *mockStore) DeactivateTelegramChat(context.Context, string) error                { return nil }
func (m *mockStore) TouchTelegramChat(context.Context, string) error                     { return nil }
func (m *mockStore) CreateTelegramPairing(context.Context, *store.TelegramPairing) error { return nil }
func (m *mockStore) GetTelegramPairing(context.Context, string) (*store.TelegramPairing, error) {
	return nil, nil
}
func (m *mockStore) DeleteTelegramPairing(context.Context, string) error { return nil }
func (m *mockStore) SweepExpiredTelegramPairings(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (m *mockStore) InsertTelegramSentMessage(context.Context, *store.TelegramSentMessage) error {
	return nil
}
func (m *mockStore) GetTelegramSentMessage(context.Context, string, string, string) (*store.TelegramSentMessage, error) {
	return nil, nil
}

// Stubs — GoogleChatStore (migration 067).
func (m *mockStore) UpsertGoogleChatSpace(context.Context, *store.GoogleChatSpace) error { return nil }
func (m *mockStore) GetGoogleChatSpace(context.Context, string) (*store.GoogleChatSpace, error) {
	return nil, nil
}
func (m *mockStore) GetGoogleChatSpaceByName(context.Context, string) (*store.GoogleChatSpace, error) {
	return nil, nil
}
func (m *mockStore) ListGoogleChatSpaces(context.Context) ([]store.GoogleChatSpace, error) {
	return nil, nil
}
func (m *mockStore) ListActiveGoogleChatSpacesByWorkspace(context.Context, string) ([]store.GoogleChatSpace, error) {
	return nil, nil
}
func (m *mockStore) UpdateGoogleChatSpaceMinPriority(context.Context, string, string) error {
	return nil
}
func (m *mockStore) UpdateGoogleChatSpaceListenMode(context.Context, string, string) error {
	return nil
}
func (m *mockStore) DeactivateGoogleChatSpace(context.Context, string) error { return nil }
func (m *mockStore) TouchGoogleChatSpace(context.Context, string) error      { return nil }
func (m *mockStore) CreateGoogleChatPairing(context.Context, *store.GoogleChatPairing) error {
	return nil
}
func (m *mockStore) GetGoogleChatPairing(context.Context, string) (*store.GoogleChatPairing, error) {
	return nil, nil
}
func (m *mockStore) DeleteGoogleChatPairing(context.Context, string) error { return nil }
func (m *mockStore) SweepExpiredGoogleChatPairings(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (m *mockStore) InsertGoogleChatSentMessage(context.Context, *store.GoogleChatSentMessage) error {
	return nil
}
func (m *mockStore) GetGoogleChatSentMessage(context.Context, string, string) (*store.GoogleChatSentMessage, error) {
	return nil, nil
}

// Stubs — TrustedSignerStore.
func (m *mockStore) AddTrustedSigner(context.Context, *store.TrustedSigner) error { return nil }
func (m *mockStore) RemoveTrustedSigner(context.Context, string) error            { return nil }
func (m *mockStore) IsTrusted(context.Context, string) (bool, error)              { return false, nil }
func (m *mockStore) ListTrustedSigners(context.Context) ([]store.TrustedSigner, error) {
	return nil, nil
}

// Stubs — P2PPeerStore.
func (m *mockStore) AddPeer(context.Context, *store.P2PPeer) error             { return nil }
func (m *mockStore) GetPeer(context.Context, string) (*store.P2PPeer, error)   { return nil, nil }
func (m *mockStore) ListPeers(context.Context) ([]store.P2PPeer, error)        { return nil, nil }
func (m *mockStore) RevokePeer(context.Context, string) error                  { return nil }
func (m *mockStore) UnrevokePeer(context.Context, string) error                { return nil }
func (m *mockStore) GrantPeerScope(context.Context, string, string) error      { return nil }
func (m *mockStore) RevokePeerScope(context.Context, string, string) error     { return nil }
func (m *mockStore) UpdateLastSeen(context.Context, string, time.Time) error   { return nil }
func (m *mockStore) UpdateDisplayName(context.Context, string, string) error   { return nil }
func (m *mockStore) SetPeerSSHTarget(context.Context, string, string) error    { return nil }
func (m *mockStore) RememberPeerAddrs(context.Context, string, []string) error { return nil }
func (m *mockStore) LoadPeerAddrs(context.Context, string) ([]string, error)   { return nil, nil }
func (m *mockStore) CreatePendingPair(context.Context, *store.P2PPendingPair) error {
	return nil
}
func (m *mockStore) GetPendingPair(context.Context, string) (*store.P2PPendingPair, error) {
	return nil, nil
}
func (m *mockStore) DeletePendingPair(context.Context, string) error { return nil }
func (m *mockStore) SweepExpiredPendingPairs(context.Context, time.Time) (int, error) {
	return 0, nil
}

// Stubs — UserStore (M7.1).
func (m *mockStore) CreateUser(context.Context, *store.User) error               { return nil }
func (m *mockStore) GetUser(context.Context, string) (*store.User, error)        { return nil, nil }
func (m *mockStore) GetSelfUser(context.Context) (*store.User, error)            { return nil, nil }
func (m *mockStore) ListUsers(context.Context) ([]store.User, error)             { return nil, nil }
func (m *mockStore) UpdateUserDisplayName(context.Context, string, string) error { return nil }
func (m *mockStore) UpsertUser(context.Context, string, string) error            { return nil }
func (m *mockStore) LinkPeerToUser(context.Context, string, string) error        { return nil }
func (m *mockStore) GetUserForPeer(context.Context, string) (*store.User, error) { return nil, nil }
func (m *mockStore) ListPeersForUser(context.Context, string) ([]store.P2PPeer, error) {
	return nil, nil
}

// Stubs — InstalledSkillStore.
func (m *mockStore) UpsertInstalledSkill(context.Context, *store.InstalledSkill) error {
	return nil
}
func (m *mockStore) GetInstalledSkill(context.Context, string) (*store.InstalledSkill, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListInstalledSkills(context.Context) ([]store.InstalledSkill, error) {
	return nil, nil
}
func (m *mockStore) DeleteInstalledSkill(context.Context, string) error { return nil }

// Stubs — SkillRegistryStore.
func (m *mockStore) PublishSkillRegistryEntry(context.Context, *store.SkillRegistryEntry) (bool, error) {
	return false, nil
}
func (m *mockStore) GetSkillRegistryEntry(context.Context, *string, string, int) (*store.SkillRegistryEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) GetSkillRegistryBundle(context.Context, *string, string, int) ([]byte, string, error) {
	return nil, "", store.ErrNotFound
}
func (m *mockStore) GetSkillRegistryHead(context.Context, store.SkillScope, string) (*store.SkillRegistryEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListSkillRegistryHeads(context.Context, store.SkillScope, int) ([]store.SkillRegistryEntry, error) {
	return nil, nil
}
func (m *mockStore) ListSkillRegistryVersions(context.Context, store.SkillScope, string, bool) ([]store.SkillRegistryEntry, error) {
	return nil, nil
}
func (m *mockStore) SoftDeleteSkillRegistryEntry(context.Context, *string, string, int) error {
	return nil
}
func (m *mockStore) SetSkillRegistryTag(context.Context, *store.SkillRegistryTag) error {
	return nil
}
func (m *mockStore) GetSkillRegistryTag(context.Context, string, string) (*store.SkillRegistryTag, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) DeleteSkillRegistryTag(context.Context, string, string) error { return nil }

// Stubs — WorkerTemplateStore.
func (m *mockStore) PublishWorkerTemplate(context.Context, *store.WorkerTemplateEntry) (bool, error) {
	return false, nil
}
func (m *mockStore) GetWorkerTemplate(context.Context, *string, string, int) (*store.WorkerTemplateEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) GetWorkerTemplateHead(context.Context, store.SkillScope, string) (*store.WorkerTemplateEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListWorkerTemplateHeads(context.Context, store.SkillScope, int) ([]store.WorkerTemplateEntry, error) {
	return nil, nil
}
func (m *mockStore) ListWorkerTemplateVersions(context.Context, store.SkillScope, string, bool) ([]store.WorkerTemplateEntry, error) {
	return nil, nil
}
func (m *mockStore) SoftDeleteWorkerTemplate(context.Context, *string, string, int) error {
	return nil
}

// Stubs — MemoryStore.
func (m *mockStore) WriteMemory(context.Context, *store.MemoryEntry) error  { return nil }
func (m *mockStore) UpdateMemory(context.Context, *store.MemoryEntry) error { return nil }
func (m *mockStore) GetMemory(context.Context, string) (*store.MemoryEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) GetMemoryForPeer(context.Context, string, []string, bool) (*store.MemoryEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListMemories(context.Context, store.MemoryFilter) ([]store.MemoryEntry, error) {
	return nil, nil
}
func (m *mockStore) SearchMemories(context.Context, store.MemoryFilter, string) ([]store.MemoryHit, error) {
	return nil, nil
}
func (m *mockStore) VectorSearchMemories(context.Context, store.MemoryFilter, string, []float32, int) ([]store.MemoryHit, error) {
	return nil, nil
}
func (m *mockStore) UpsertMemoryEmbedding(context.Context, string, string, int, []float32) error {
	return nil
}
func (m *mockStore) ListMemoriesNeedingEmbedding(context.Context, int) ([]store.MemoryEmbedTarget, error) {
	return nil, nil
}
func (m *mockStore) CountMemoriesNeedingEmbedding(context.Context) (int, int, error) {
	return 0, 0, nil
}
func (m *mockStore) RecordMemoryConflicts(context.Context, []store.MemoryConflict) error {
	return nil
}
func (m *mockStore) ListOpenMemoryConflicts(context.Context, int) ([]store.MemoryConflict, error) {
	return nil, nil
}
func (m *mockStore) ResolveMemoryConflict(context.Context, string, string) error {
	return nil
}
func (m *mockStore) GetMemoryEmbedding(context.Context, string) (string, []float32, error) {
	return "", nil, store.ErrNotFound
}
func (m *mockStore) InvalidateMemory(context.Context, string, string) error { return nil }
func (m *mockStore) SoftDeleteMemory(context.Context, string) error         { return nil }
func (m *mockStore) ForgetMemoryBySource(context.Context, string, store.SkillScope) (int, error) {
	return 0, nil
}
func (m *mockStore) CountMemories(context.Context, store.SkillScope) (int, int, error) {
	return 0, 0, nil
}
func (m *mockStore) GetMemoryStats(context.Context, store.SkillScope) (store.MemoryStats, error) {
	return store.MemoryStats{}, nil
}
func (m *mockStore) SetMemoryPinned(context.Context, string, bool) error         { return nil }
func (m *mockStore) UpsertMemoryOffer(context.Context, *store.MemoryOffer) error { return nil }
func (m *mockStore) GetMemoryOffer(context.Context, string) (*store.MemoryOffer, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListMemoryOffers(context.Context, store.MemoryOfferFilter) ([]store.MemoryOffer, error) {
	return nil, nil
}
func (m *mockStore) AcceptMemoryOffer(context.Context, string, string) error { return nil }
func (m *mockStore) DeclineMemoryOffer(context.Context, string) error        { return nil }
func (m *mockStore) LinkMemoryEntity(context.Context, string, store.EntityRef, string) error {
	return nil
}
func (m *mockStore) UnlinkMemoryEntity(context.Context, string, store.EntityRef) error {
	return nil
}
func (m *mockStore) ListMemoryEntities(context.Context, string) ([]store.MemoryEntityRow, error) {
	return nil, nil
}
func (m *mockStore) WritePerson(context.Context, *store.PersonEntry) error  { return nil }
func (m *mockStore) UpdatePerson(context.Context, *store.PersonEntry) error { return nil }
func (m *mockStore) GetPerson(context.Context, string) (*store.PersonEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListPeople(context.Context, store.PersonFilter) ([]store.PersonEntry, error) {
	return nil, nil
}
func (m *mockStore) SearchPeople(context.Context, store.PersonFilter, string) ([]store.PersonHit, error) {
	return nil, nil
}
func (m *mockStore) SoftDeletePerson(context.Context, string) error { return nil }
func (m *mockStore) CountPeople(context.Context) (int, error)       { return 0, nil }
func (m *mockStore) LinkPersonEntity(context.Context, string, store.EntityRef, string) error {
	return nil
}
func (m *mockStore) UnlinkPersonEntity(context.Context, string, store.EntityRef) error {
	return nil
}
func (m *mockStore) ListPersonEntities(context.Context, string) ([]store.PersonEntityRow, error) {
	return nil, nil
}
func (m *mockStore) ListEntities(context.Context, store.EntityFilter) ([]store.EntitySummary, error) {
	return nil, nil
}
func (m *mockStore) RelatedEntities(context.Context, store.EntityRef, store.SkillScope, int) ([]store.EntityCoLink, error) {
	return nil, nil
}
func (m *mockStore) LogMemoryRecallEvents(context.Context, []store.MemoryRecallEvent) error {
	return nil
}
func (m *mockStore) CoRecalledMemories(context.Context, string, store.SkillScope, int) ([]store.CoRecalledMemory, error) {
	return nil, nil
}
func (m *mockStore) GetMemoryRecallStats(context.Context, []string) (map[string]store.MemoryRecallStat, error) {
	return nil, nil
}
func (m *mockStore) ForgetRecallEventsBySource(context.Context, string, store.SkillScope) (int, error) {
	return 0, nil
}
func (m *mockStore) InsertChatTurnSignal(context.Context, *store.ChatTurnSignal) error {
	return nil
}
func (m *mockStore) ListChatTurnSignals(context.Context, store.ChatTurnSignalFilter) ([]store.ChatTurnSignal, error) {
	return nil, nil
}
func (m *mockStore) MarkChatTurnSignalPromoted(context.Context, string, string) error {
	return nil
}
func (m *mockStore) ForgetChatTurnSignalsBySource(context.Context, string) (int, error) {
	return 0, nil
}

// Stubs — SecretPromptStore.
func (m *mockStore) CreateSecretPrompt(context.Context, *store.SecretPrompt) error { return nil }
func (m *mockStore) GetSecretPrompt(context.Context, string) (*store.SecretPrompt, error) {
	return nil, nil
}
func (m *mockStore) ListPendingSecretPrompts(context.Context) ([]store.SecretPrompt, error) {
	return nil, nil
}
func (m *mockStore) CompleteSecretPrompt(context.Context, string, string, string, time.Time) error {
	return nil
}
func (m *mockStore) ListExpiredSecretPrompts(context.Context, time.Time) ([]store.SecretPrompt, error) {
	return nil, nil
}

// MeshOutboundQueueStore — no-op stubs for the offline-delivery queue
// added in v0.7.4. The gateway tests don't exercise the queue path.
func (m *mockStore) EnqueueMeshOutbound(context.Context, *store.MeshOutbound) error {
	return nil
}
func (m *mockStore) ListDueMeshOutbound(context.Context, string, time.Time, int) ([]store.MeshOutbound, error) {
	return nil, nil
}
func (m *mockStore) MarkMeshOutboundDelivered(context.Context, string, time.Time) error {
	return nil
}
func (m *mockStore) BumpMeshOutboundAttempt(context.Context, string, string, time.Time) error {
	return nil
}
func (m *mockStore) ListPendingMeshOutbound(context.Context, time.Time, int) ([]store.MeshOutbound, error) {
	return nil, nil
}
func (m *mockStore) ListExpiredMeshOutbound(context.Context, time.Time, int) ([]store.MeshOutbound, error) {
	return nil, nil
}
func (m *mockStore) PruneMeshOutbound(context.Context, time.Time, time.Time) (int, error) {
	return 0, nil
}

// SkillRunStore stubs (W2). All no-ops — handler tests that exercise
// skill telemetry use the real sqlite-backed handler fixture in
// handler_skill_runs_test.go.
func (m *mockStore) RecordSkillRun(context.Context, *store.SkillRun) error { return nil }
func (m *mockStore) UpdateSkillRun(context.Context, string, store.SkillRunPatch) error {
	return nil
}
func (m *mockStore) GetSkillRun(context.Context, string) (*store.SkillRun, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListSkillRuns(context.Context, store.SkillRunFilter) ([]store.SkillRun, error) {
	return nil, nil
}

// SkillRefinementStore stubs (W3). Same posture as the W2 stubs.
func (m *mockStore) RecordRefinementProposal(context.Context, *store.SkillRefinementProposal) error {
	return nil
}
func (m *mockStore) UpdateRefinementProposal(context.Context, string, store.RefinementProposalPatch) error {
	return nil
}
func (m *mockStore) GetRefinementProposal(context.Context, string) (*store.SkillRefinementProposal, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListRefinementProposals(context.Context, store.RefinementFilter) ([]store.SkillRefinementProposal, error) {
	return nil, nil
}
func (m *mockStore) CountSimilarProposals(context.Context, string, string) (int, error) {
	return 0, nil
}

// RecipeStore stubs (for recipe harvest/search integration).
func (m *mockStore) UpsertRecipe(context.Context, *store.Recipe) error { return nil }
func (m *mockStore) GetRecipe(context.Context, string) (*store.Recipe, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) GetRecipeByToolName(context.Context, string) (*store.Recipe, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListRecipes(context.Context, store.RecipeFilter) ([]store.Recipe, error) {
	return nil, nil
}
func (m *mockStore) SearchRecipes(context.Context, store.RecipeFilter) ([]store.Recipe, error) {
	return nil, nil
}
func (m *mockStore) DeleteRecipe(context.Context, string) error { return store.ErrNotFound }

// --- Helpers ---

func toolsJSON(tools ...Tool) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"tools": tools})
	return data
}

func newTestHandler(lister ToolLister, servers []store.DownstreamServer) (*handler, *mockStore) {
	// Always include the internal virtual servers for built-in tool routing.
	allServers := append(servers,
		store.DownstreamServer{
			ID: "mcpx-builtin", Name: "MCPlexer Built-in Tools",
			Transport: "internal", ToolNamespace: "mcpx", Discovery: "static",
		},
		store.DownstreamServer{
			ID: "data-builtin", Name: "Data Workbench",
			Transport: "internal", ToolNamespace: "data", Discovery: "static",
		},
		store.DownstreamServer{
			ID: "kv-builtin", Name: "Code-mode KV",
			Transport: "internal", ToolNamespace: "kv", Discovery: "static",
		},
		store.DownstreamServer{
			ID: "index-builtin", Name: "Code Index",
			Transport: "internal", ToolNamespace: "index", Discovery: "static",
		},
	)
	ms := &mockStore{
		servers:    allServers,
		capUpdates: make(map[string]json.RawMessage),
		workspaces: []mockWorkspace{{id: "ws-global", rootPath: "/"}},
		routeRules: map[string][]store.RouteRule{
			"ws-global": {
				{
					ID: "builtin-allow", WorkspaceID: "ws-global",
					Priority: 100, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["mcpx__*"]`),
					DownstreamServerID: "mcpx-builtin",
				},
				{
					// Mirror production seed_routes: data__/kv__ code-mode
					// builtins dispatch via dedicated internal servers whose
					// namespace matches the tool prefix (the routing namespace
					// guard rejects a mismatched downstream). Sits above
					// allow-all so they reach handleBuiltinCall instead of the
					// empty-downstream fallback.
					ID: "data-allow", WorkspaceID: "ws-global",
					Priority: 95, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["data__*"]`),
					DownstreamServerID: "data-builtin",
				},
				{
					ID: "kv-allow", WorkspaceID: "ws-global",
					Priority: 95, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["kv__*"]`),
					DownstreamServerID: "kv-builtin",
				},
				{
					ID: "index-allow", WorkspaceID: "ws-global",
					Priority: 95, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["index__*"]`),
					DownstreamServerID: "index-builtin",
				},
				{
					ID: "allow-all", WorkspaceID: "ws-global",
					Priority: 1, PathGlob: "**", Policy: "allow",
					ToolMatch: json.RawMessage(`["*"]`),
				},
			},
		},
	}
	engine := routing.NewEngine(ms)
	h := newHandler(ms, engine, lister, nil, TransportSocket, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	// Bind session to the global workspace so tool filtering passes.
	h.sessions.clientPath = "/test"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/"}}
	return h, ms
}

func toolNames(result json.RawMessage) []string {
	var parsed struct {
		Tools []Tool `json:"tools"`
	}
	json.Unmarshal(result, &parsed) //nolint:errcheck
	names := make([]string, len(parsed.Tools))
	for i, t := range parsed.Tools {
		names[i] = t.Name
	}
	sort.Strings(names)
	return names
}

// --- Tests ---

func TestToolExtrasRoundTrip(t *testing.T) {
	// Downstream tool with annotations, title, outputSchema — extras must survive.
	raw := json.RawMessage(`{
		"name": "read_file",
		"description": "Read a file",
		"inputSchema": {"type":"object"},
		"annotations": {"readOnlyHint": true, "openWorldHint": false},
		"title": "Read File",
		"outputSchema": {"type":"object","properties":{"content":{"type":"string"}}}
	}`)

	var tool Tool
	if err := json.Unmarshal(raw, &tool); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if tool.Name != "read_file" {
		t.Errorf("name = %q, want %q", tool.Name, "read_file")
	}
	if tool.Description != "Read a file" {
		t.Errorf("description = %q, want %q", tool.Description, "Read a file")
	}
	if tool.Extras == nil {
		t.Fatal("extras is nil, expected annotations/title/outputSchema")
	}
	for _, key := range []string{"annotations", "title", "outputSchema"} {
		if _, ok := tool.Extras[key]; !ok {
			t.Errorf("extras missing key %q", key)
		}
	}

	// Re-marshal and verify extras appear in the output.
	out, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var flat map[string]json.RawMessage
	if err := json.Unmarshal(out, &flat); err != nil {
		t.Fatalf("unmarshal flat: %v", err)
	}
	for _, key := range []string{"name", "description", "inputSchema", "annotations", "title", "outputSchema"} {
		if _, ok := flat[key]; !ok {
			t.Errorf("marshalled output missing key %q", key)
		}
	}
}

func TestExtractNamespacedToolsPreservesExtras(t *testing.T) {
	// Simulate downstream tools/list response with extras.
	raw := json.RawMessage(`{"tools":[{
		"name": "create_issue",
		"description": "Create an issue",
		"inputSchema": {"type":"object"},
		"annotations": {"readOnlyHint": false}
	}]}`)

	tools, err := extractNamespacedTools("github", raw)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if tools[0].Name != "github__create_issue" {
		t.Errorf("name = %q, want %q", tools[0].Name, "github__create_issue")
	}
	if tools[0].Extras == nil {
		t.Fatal("extras nil after extractNamespacedTools")
	}
	if _, ok := tools[0].Extras["annotations"]; !ok {
		t.Error("annotations not preserved through extractNamespacedTools")
	}
}

func TestHandleToolsList_BuiltinExtrasPassthrough(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{},
	}

	// Disable slim tools to avoid minification stripping extras.
	t.Setenv("MCPLEXER_SLIM_TOOLS", "false")

	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)
	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}

	// Parse the result and check that at least one builtin has annotations.
	var parsed struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(parsed.Tools) == 0 {
		t.Fatal("expected at least one builtin tool")
	}

	foundAnnotations := false
	for _, raw := range parsed.Tools {
		var flat map[string]json.RawMessage
		if err := json.Unmarshal(raw, &flat); err != nil {
			continue
		}
		if _, ok := flat["annotations"]; ok {
			foundAnnotations = true
			break
		}
	}
	if !foundAnnotations {
		t.Error("no builtin tool has annotations in tools/list output")
	}
}

func TestExtractNamespacedTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)

	tests := []struct {
		name      string
		namespace string
		input     json.RawMessage
		wantNames []string
		wantErr   bool
	}{
		{"nil input", "ns", nil, nil, false},
		{"empty object", "ns", json.RawMessage(`{}`), nil, false},
		{"invalid json", "ns", json.RawMessage(`not json`), nil, true},
		{
			"single tool", "github",
			toolsJSON(Tool{Name: "create_issue", Description: "Create", InputSchema: schema}),
			[]string{"github__create_issue"}, false,
		},
		{
			"multiple tools", "slack",
			toolsJSON(
				Tool{Name: "post_message", Description: "Post"},
				Tool{Name: "list_channels", Description: "List"},
			),
			[]string{"slack__list_channels", "slack__post_message"}, false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractNamespacedTools(tt.namespace, tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantNames == nil {
				if len(got) != 0 {
					t.Fatalf("got %d tools, want 0", len(got))
				}
				return
			}
			names := make([]string, len(got))
			for i, tool := range got {
				names[i] = tool.Name
			}
			sort.Strings(names)
			sort.Strings(tt.wantNames)
			if len(names) != len(tt.wantNames) {
				t.Fatalf("got %v, want %v", names, tt.wantNames)
			}
			for i := range names {
				if names[i] != tt.wantNames[i] {
					t.Errorf("name[%d] = %q, want %q", i, names[i], tt.wantNames[i])
				}
			}
		})
	}
}

func TestHandleToolsList_ReturnsSlimSurface(t *testing.T) {
	servers := []store.DownstreamServer{
		{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
		{ID: "slack-server", ToolNamespace: "slack", Discovery: "static"},
	}
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"gh-server":    toolsJSON(Tool{Name: "create_issue", Description: "Create issue"}),
			"slack-server": toolsJSON(Tool{Name: "post_message", Description: "Post message"}),
		},
	}

	h, ms := newTestHandler(lister, servers)
	h.settingsSvc = config.NewSettingsService(ms)
	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}

	names := toolNames(result)
	// Default is SlimSurface=true. tools/list returns ONLY the 4 keep-list
	// entrypoints. Workflow tools (mesh__*, task__*, memory__*, skill__*,
	// reload_server, import_openapi, …) move to searchableBuiltins —
	// callable + discoverable via mcpx__search_tools, but absent here.
	// Downstream tools (github__*, slack__*) are never advertised regardless.
	want := []string{
		"mcpx__execute_code",
		"mcpx__search_tools",
	}
	gotSet := map[string]bool{}
	for _, n := range names {
		gotSet[n] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("missing slim-surface keeper %q in tools/list, got %v", w, names)
		}
	}
	// Allow secret__* entries when the test handler wires those services up,
	// but assert no workflow tool snuck through.
	forbidden := []string{
		"mcpx__reload_server", "mcpx__import_openapi",
		"mesh__send", "task__create", "memory__save",
		"skill__phase", "skill__propose_refinement", "skill__adopt_refinement",
		"skill__run_start", "skill__run_complete",
	}
	for _, f := range forbidden {
		if gotSet[f] {
			t.Errorf("non-keeper %q leaked into slim-surface tools/list: %v", f, names)
		}
	}
}

func TestHandleToolsList_AlwaysReturnsCoreEntrypoints(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{},
	}

	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)
	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}

	names := toolNames(result)
	// Under slim-surface (the default), the universal entrypoints
	// execute_code + search_tools must always be present. Without these
	// the agent loses access to downstream tools entirely.
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["mcpx__execute_code"] {
		t.Errorf("missing mcpx__execute_code, got %v", names)
	}
	if !found["mcpx__search_tools"] {
		t.Errorf("missing mcpx__search_tools, got %v", names)
	}
}

// TestHandleToolsList_FullSurfaceEscapeHatch confirms the SlimSurface=false
// override restores the pre-slim wide tool advertisement, so power users
// who explicitly opt out keep the full set in tools/list.
func TestHandleToolsList_FullSurfaceEscapeHatch(t *testing.T) {
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)

	settings := config.DefaultSettings()
	settings.SlimSurface = false
	if err := h.settingsSvc.Save(context.Background(), settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected error: %v", rpcErr)
	}
	names := toolNames(result)
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	// With slim disabled, the wide set returns. Spot-check a tool the
	// slim mode would have hidden.
	if !found["mcpx__reload_server"] {
		t.Errorf("expected mcpx__reload_server in full-surface mode, got %v", names)
	}
	if !found["mcpx__execute_code"] {
		t.Errorf("missing mcpx__execute_code in full-surface mode, got %v", names)
	}
}

func TestHandleToolsList_NoDownstreamServers(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{},
	}

	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)
	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}

	names := toolNames(result)
	// Still gets execute_code + search_tools even with no downstream servers.
	if len(names) < 2 {
		t.Fatalf("expected at least 2 tools (execute_code + search_tools), got %v", names)
	}
}

func TestHandleToolsList_NoDownstreamToolsExposed(t *testing.T) {
	servers := []store.DownstreamServer{
		{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
		{ID: "dyn-server", ToolNamespace: "dynns", Discovery: "dynamic"},
	}
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"gh-server":  toolsJSON(Tool{Name: "create_issue", Description: "Create issue"}),
			"dyn-server": toolsJSON(Tool{Name: "hidden_tool", Description: "Hidden"}),
		},
	}

	h, ms := newTestHandler(lister, servers)
	h.settingsSvc = config.NewSettingsService(ms)
	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}

	names := toolNames(result)
	// No downstream tools should appear — only builtins.
	for _, name := range names {
		if name == "github__create_issue" || name == "dynns__hidden_tool" {
			t.Errorf("downstream tool %q should not appear in tools/list", name)
		}
	}
	// Should have execute_code + search_tools.
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["mcpx__execute_code"] || !found["mcpx__search_tools"] {
		t.Errorf("expected execute_code and search_tools, got %v", names)
	}
}

func TestHandleDiscoverTools_FindsMatchingTools(t *testing.T) {
	servers := []store.DownstreamServer{
		{ID: "static-server", ToolNamespace: "github", Discovery: "static"},
		{ID: "dyn-server", ToolNamespace: "dynns", Discovery: "dynamic"},
	}
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"static-server": toolsJSON(Tool{
				Name: "create_issue", Description: "Create issue",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`),
			}),
			"dyn-server": toolsJSON(
				Tool{Name: "search_code", Description: "Search code in repo",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)},
				Tool{Name: "list_files", Description: "List files in directory"},
			),
		},
	}

	h, ms := newTestHandler(lister, servers)
	h.settingsSvc = config.NewSettingsService(ms)
	result, rpcErr := h.handleDiscoverTools(context.Background(), []string{"search"}, "full", "", nil, 0)
	if rpcErr != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}

	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(tr.Content) == 0 {
		t.Fatal("expected content in result")
	}
	text := tr.Content[0].Text
	if !contains(text, "dynns__search_code") {
		t.Errorf("expected matching tool in result, got: %s", text)
	}
	// Should include TypeScript definitions.
	if !contains(text, "Code API") {
		t.Errorf("expected Code API section in result, got: %s", text)
	}
}

func TestHandleDiscoverTools_NoResults(t *testing.T) {
	servers := []store.DownstreamServer{
		{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
	}
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"gh-server": toolsJSON(Tool{Name: "create_issue", Description: "Create issue"}),
		},
	}

	h, ms := newTestHandler(lister, servers)
	h.settingsSvc = config.NewSettingsService(ms)
	result, rpcErr := h.handleDiscoverTools(context.Background(), []string{"nonexistent_xyz"}, "", "", nil, 0)
	if rpcErr != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}

	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(tr.Content) == 0 || !contains(tr.Content[0].Text, "No tools found") {
		t.Errorf("expected 'No tools found' message, got: %v", tr.Content)
	}
}

func TestHandleDiscoverTools_AdminGateFiltersSearchResults(t *testing.T) {
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)
	h.adminGate = NewAdminCWDGate("/data")
	h.sessions.clientPath = "/project"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/project"}}

	result, rpcErr := h.handleDiscoverTools(context.Background(), []string{"reload server"}, "summary", "", nil, 0)
	if rpcErr != nil {
		t.Fatalf("unexpected search error: %v", rpcErr)
	}
	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal search result: %v", err)
	}
	if len(tr.Content) == 0 {
		t.Fatal("expected search content")
	}
	if contains(tr.Content[0].Text, "mcpx__reload_server") {
		t.Fatalf("admin tool leaked through search results: %s", tr.Content[0].Text)
	}

	exact, rpcErr := h.handleDiscoverTools(context.Background(), nil, "", "mcpx__reload_server", nil, 0)
	if rpcErr != nil {
		t.Fatalf("unexpected exact lookup error: %v", rpcErr)
	}
	if err := json.Unmarshal(exact, &tr); err != nil {
		t.Fatalf("unmarshal exact result: %v", err)
	}
	if !contains(tr.Content[0].Text, "not found") || contains(tr.Content[0].Text, "Code API") {
		t.Fatalf("expected gated exact lookup to be not found without hydrate, got: %s", tr.Content[0].Text)
	}

	h.sessions.clientPath = "/data/sub"
	allowed, rpcErr := h.handleDiscoverTools(context.Background(), nil, "", "mcpx__reload_server", nil, 0)
	if rpcErr != nil {
		t.Fatalf("unexpected admin exact lookup error: %v", rpcErr)
	}
	if err := json.Unmarshal(allowed, &tr); err != nil {
		t.Fatalf("unmarshal allowed exact result: %v", err)
	}
	if !contains(tr.Content[0].Text, "mcpx__reload_server") || !contains(tr.Content[0].Text, "Code API") {
		t.Fatalf("expected admin context to hydrate reload_server, got: %s", tr.Content[0].Text)
	}
}

func TestCodeModeToolDefs_AdminGateFiltersSearchableBuiltins(t *testing.T) {
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)
	h.adminGate = NewAdminCWDGate("/data")
	h.sessions.clientPath = "/project"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/project"}}

	defs, err := h.codeModeToolDefs(context.Background())
	if err != nil {
		t.Fatalf("codeModeToolDefs: %v", err)
	}
	for _, def := range defs {
		if def.Name == "mcpx__reload_server" {
			t.Fatalf("admin tool leaked into non-admin code-mode definitions")
		}
	}

	h.sessions.clientPath = "/data/sub"
	defs, err = h.codeModeToolDefs(context.Background())
	if err != nil {
		t.Fatalf("admin codeModeToolDefs: %v", err)
	}
	found := false
	for _, def := range defs {
		if def.Name == "mcpx__reload_server" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("admin context should include reload_server in code-mode definitions")
	}
}

func TestHandleToolsCall_InterceptsBuiltin(t *testing.T) {
	servers := []store.DownstreamServer{
		{ID: "dyn-server", ToolNamespace: "dynns", Discovery: "dynamic"},
	}
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"dyn-server": toolsJSON(Tool{
				Name: "some_tool", Description: "A tool",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
			}),
		},
	}

	h, ms := newTestHandler(lister, servers)
	h.settingsSvc = config.NewSettingsService(ms)
	params, _ := json.Marshal(CallToolRequest{
		Name:      "mcpx__search_tools",
		Arguments: json.RawMessage(`{"queries":["some"]}`),
	})
	result, rpcErr := h.handleToolsCall(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}

	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(tr.Content) == 0 {
		t.Fatal("expected content in result")
	}
	if !contains(tr.Content[0].Text, "dynns__some_tool") {
		t.Errorf("expected tool in discover result, got: %s", tr.Content[0].Text)
	}
}

func TestHandleToolsCall_BlocksDirectDownstreamCalls(t *testing.T) {
	servers := []store.DownstreamServer{
		{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
	}
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"gh-server": toolsJSON(Tool{Name: "create_issue", Description: "Create issue"}),
		},
	}

	h, _ := newTestHandler(lister, servers)

	params, _ := json.Marshal(CallToolRequest{
		Name:      "github__create_issue",
		Arguments: json.RawMessage(`{"title":"bug"}`),
	})
	result, rpcErr := h.handleToolsCall(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}
	if lister.callCount != 0 {
		t.Fatalf("downstream tool should not be called directly, got %d calls", lister.callCount)
	}

	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !tr.IsError || len(tr.Content) == 0 || !contains(tr.Content[0].Text, "mcpx__execute_code") {
		t.Fatalf("expected direct-call block message, got: %+v", tr)
	}
}

func TestHandleToolsCall_ExecuteCodeCanCallDownstreamTools(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"gh-server": toolsJSON(Tool{
				Name:        "create_issue",
				Description: "Create issue",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}}}`),
			}),
		},
	}

	ms := &mockStore{
		servers: []store.DownstreamServer{
			{ID: "gh-server", ToolNamespace: "github", Discovery: "static"},
			{
				ID: "mcpx-builtin", Name: "MCPlexer Built-in Tools",
				Transport: "internal", ToolNamespace: "mcpx", Discovery: "static",
			},
		},
		capUpdates: make(map[string]json.RawMessage),
		workspaces: []mockWorkspace{{id: "ws-global", rootPath: "/"}},
		routeRules: map[string][]store.RouteRule{
			"ws-global": {
				{
					ID: "builtin-allow", WorkspaceID: "ws-global",
					Priority: 100, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["mcpx__*"]`),
					DownstreamServerID: "mcpx-builtin",
				},
				{
					ID: "allow-gh", WorkspaceID: "ws-global",
					Priority: 10, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["github__*"]`),
					DownstreamServerID: "gh-server",
				},
			},
		},
	}
	h := newHandler(
		ms,
		routing.NewEngine(ms),
		lister,
		nil,
		TransportSocket,
		nil,
		nil,
		nil,
		config.NewSettingsService(ms),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	h.sessions.clientPath = "/test"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/"}}

	params, _ := json.Marshal(CallToolRequest{
		Name: "mcpx__execute_code",
		Arguments: json.RawMessage(`{
			"code": "github.create_issue({ title: 'bug' }); print('ok');"
		}`),
	})
	result, rpcErr := h.handleToolsCall(context.Background(), params)
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}
	if lister.callCount != 1 {
		t.Fatalf("expected 1 downstream call via execute_code, got %d", lister.callCount)
	}
	if lister.lastCall.toolName != "create_issue" {
		t.Fatalf("tool name = %q, want %q", lister.lastCall.toolName, "create_issue")
	}

	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(tr.Content) == 0 || !contains(tr.Content[0].Text, "ok") {
		t.Fatalf("expected execute_code output, got: %+v", tr)
	}
}

// TestHandleToolsCall_ExecuteCodeEmptyRejectsWithExample pins the
// qwen/cheap-model repair hint on the empty-code rejection in
// handler_builtin.go: callers that pass {"code":""} must get back a
// "code is required" error that ALSO includes a copy-pasteable
// corrected example payload. Without the example, a cheap model loops
// on the same empty-code call instead of producing a real script.
func TestHandleToolsCall_ExecuteCodeEmptyRejectsWithExample(t *testing.T) {
	lister := &mockToolLister{}
	h, _ := newTestHandler(lister, nil)

	// Three flavours of "empty" all hit the same path: literal empty
	// string, JSON null (unmarshal to zero value), and missing field
	// (zero value from empty map). Cover all three so a model that
	// passes any of them gets the same example.
	cases := []struct {
		name string
		body string
	}{
		{"empty string", `{"code": ""}`},
		{"null", `{"code": null}`},
		{"missing", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params, _ := json.Marshal(CallToolRequest{
				Name:      "mcpx__execute_code",
				Arguments: json.RawMessage(tc.body),
			})
			_, rpcErr := h.handleToolsCall(context.Background(), params)
			if rpcErr == nil {
				t.Fatal("expected RPC error for empty code, got nil")
			}
			if rpcErr.Code != CodeInvalidParams {
				t.Fatalf("code = %d, want %d", rpcErr.Code, CodeInvalidParams)
			}
			if !strings.Contains(rpcErr.Message, "code is required") {
				t.Errorf("err %q does not contain %q", rpcErr.Message, "code is required")
			}
			if !strings.Contains(rpcErr.Message, `Example: {"code":`) {
				t.Errorf("err %q does not contain example payload", rpcErr.Message)
			}
			if !strings.Contains(rpcErr.Message, "mcpx.search_tools") {
				t.Errorf("err %q example should reference mcpx.search_tools", rpcErr.Message)
			}
		})
	}
}

func TestHandleToolsCall_GitHubRepoAllowlistBlocksDisallowedRepo(t *testing.T) {
	lister := &mockToolLister{}
	ms := &mockStore{
		servers: []store.DownstreamServer{{ID: "gh", ToolNamespace: "github", Discovery: "static"}},
		workspaces: []mockWorkspace{
			{id: "ws-global", rootPath: "/"},
		},
		routeRules: map[string][]store.RouteRule{
			"ws-global": {
				{
					ID: "allow-gh", WorkspaceID: "ws-global",
					Priority: 1, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["github__*"]`),
					DownstreamServerID: "gh",
					ScopePolicy:        json.RawMessage(`{"repo":["acme/mcplexer"]}`),
				},
			},
		},
	}

	h := newHandler(ms, routing.NewEngine(ms), lister, nil, TransportSocket, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.sessions.clientPath = "/test"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/"}}

	// Use internal code mode context to simulate sandbox calls.
	ctx := withInternalCodeModeCall(context.Background())
	params, _ := json.Marshal(CallToolRequest{
		Name:      "github__create_issue",
		Arguments: json.RawMessage(`{"owner":"evilco","repo":"private-repo"}`),
	})
	_, rpcErr := h.handleToolsCall(ctx, params)
	if rpcErr == nil {
		t.Fatal("expected route policy error")
		return
	}
	if rpcErr.Code != CodeInvalidParams {
		t.Fatalf("code = %d, want %d", rpcErr.Code, CodeInvalidParams)
	}
	if lister.callCount != 0 {
		t.Fatalf("downstream call count = %d, want 0", lister.callCount)
	}
}

func TestHandleToolsCall_GitHubRepoAllowlistAllowsConfiguredRepo(t *testing.T) {
	lister := &mockToolLister{}
	ms := &mockStore{
		servers: []store.DownstreamServer{{ID: "gh", ToolNamespace: "github", Discovery: "static"}},
		workspaces: []mockWorkspace{
			{id: "ws-global", rootPath: "/"},
		},
		routeRules: map[string][]store.RouteRule{
			"ws-global": {
				{
					ID: "allow-gh", WorkspaceID: "ws-global",
					Priority: 1, PathGlob: "**", Policy: "allow",
					ToolMatch:          json.RawMessage(`["github__*"]`),
					DownstreamServerID: "gh",
					ScopePolicy:        json.RawMessage(`{"repo":["acme/mcplexer"]}`),
				},
			},
		},
	}

	h := newHandler(ms, routing.NewEngine(ms), lister, nil, TransportSocket, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.sessions.clientPath = "/test"
	h.sessions.wsChain = []routing.WorkspaceAncestor{{ID: "ws-global", RootPath: "/"}}

	// Use internal code mode context to simulate sandbox calls.
	ctx := withInternalCodeModeCall(context.Background())
	params, _ := json.Marshal(CallToolRequest{
		Name:      "github__create_issue",
		Arguments: json.RawMessage(`{"owner":"acme","repo":"mcplexer"}`),
	})
	_, rpcErr := h.handleToolsCall(ctx, params)
	if rpcErr != nil {
		t.Fatalf("unexpected error: code=%d msg=%s", rpcErr.Code, rpcErr.Message)
	}
	if lister.callCount != 1 {
		t.Fatalf("downstream call count = %d, want 1", lister.callCount)
	}
}

func TestExtractAndRemoveCacheBust(t *testing.T) {
	tests := []struct {
		name     string
		input    json.RawMessage
		wantBust bool
		wantArgs string
	}{
		{"nil args", nil, false, ""},
		{"empty args", json.RawMessage(`{}`), false, "{}"},
		{"no _cache_bust", json.RawMessage(`{"id":"1"}`), false, `{"id":"1"}`},
		{"_cache_bust true", json.RawMessage(`{"id":"1","_cache_bust":true}`), true, `{"id":"1"}`},
		{"_cache_bust false", json.RawMessage(`{"id":"1","_cache_bust":false}`), false, `{"id":"1","_cache_bust":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.input
			got := extractAndRemoveCacheBust(&args)
			if got != tt.wantBust {
				t.Errorf("bust = %v, want %v", got, tt.wantBust)
			}
			if tt.wantArgs != "" && string(args) != tt.wantArgs {
				t.Errorf("args = %s, want %s", args, tt.wantArgs)
			}
		})
	}
}

func TestInjectCacheMeta(t *testing.T) {
	// Cache miss: should inject _meta.cache.cached=false.
	result := json.RawMessage(`{"content":[{"type":"text","text":"hello"}]}`)
	got := injectCacheMeta(result, false, 0)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(got, &envelope); err != nil {
		t.Fatal(err)
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(envelope["_meta"], &meta); err != nil {
		t.Fatal(err)
	}
	var cacheMeta map[string]any
	if err := json.Unmarshal(meta["cache"], &cacheMeta); err != nil {
		t.Fatal(err)
	}
	if cacheMeta["cached"] != false {
		t.Errorf("cached = %v, want false", cacheMeta["cached"])
	}

	// Cache hit: should include age_seconds.
	got2 := injectCacheMeta(result, true, 45*time.Second)
	json.Unmarshal(got2, &envelope)           //nolint:errcheck
	json.Unmarshal(envelope["_meta"], &meta)  //nolint:errcheck
	json.Unmarshal(meta["cache"], &cacheMeta) //nolint:errcheck
	if cacheMeta["cached"] != true {
		t.Errorf("cached = %v, want true", cacheMeta["cached"])
	}
	if cacheMeta["age_seconds"] != float64(45) {
		t.Errorf("age_seconds = %v, want 45", cacheMeta["age_seconds"])
	}
}

// --- DB Cache Startup Tests ---

// slowToolLister simulates a downstream manager that blocks for a long
// time on ListToolsForServers. The ready channel gates when the call
// completes so tests can control timing.
type slowToolLister struct {
	tools map[string]json.RawMessage
	ready chan struct{} // close to unblock ListToolsForServers
	calls int           // number of ListToolsForServers calls
}

func (m *slowToolLister) ListAllTools(_ context.Context) (map[string]json.RawMessage, error) {
	return m.tools, nil
}

func (m *slowToolLister) ListToolsForServers(_ context.Context, serverIDs []string) (map[string]json.RawMessage, error) {
	m.calls++
	if m.ready != nil {
		<-m.ready // block until test unblocks
	}
	result := make(map[string]json.RawMessage)
	for _, id := range serverIDs {
		if tools, ok := m.tools[id]; ok {
			result[id] = tools
		}
	}
	return result, nil
}

func (m *slowToolLister) Call(_ context.Context, _, _, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func TestToolsList_ReturnsBuiltinsWithSlowDownstream(t *testing.T) {
	// Even with a slow downstream, tools/list should return quickly
	// with builtin tools (execute_code + search_tools).
	ready := make(chan struct{})
	lister := &slowToolLister{
		tools: map[string]json.RawMessage{
			"srv1": toolsJSON(Tool{Name: "create_issue", Description: "Create an issue"}),
		},
		ready: ready,
	}

	servers := []store.DownstreamServer{
		{
			ID: "srv1", Name: "Jira", Transport: "stdio",
			ToolNamespace: "jira", Discovery: "static",
			CapabilitiesCache: toolsJSON(Tool{Name: "create_issue", Description: "Create an issue (cached)"}),
		},
	}

	h, ms := newTestHandler(lister, servers)
	h.settingsSvc = config.NewSettingsService(ms)
	h.bgCtx = context.Background()

	ctx := context.Background()
	result, rpcErr := h.handleToolsList(ctx)
	if rpcErr != nil {
		t.Fatalf("unexpected error: %v", rpcErr)
	}

	names := toolNames(result)
	if len(names) < 2 {
		t.Fatalf("expected at least 2 builtin tools, got %v", names)
	}
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["mcpx__execute_code"] || !found["mcpx__search_tools"] {
		t.Errorf("expected execute_code and search_tools, got %v", names)
	}

	// Unblock the background refresh.
	close(ready)
	time.Sleep(100 * time.Millisecond)
}

func TestBuildFromDBCache_SkipsEmptyCache(t *testing.T) {
	lister := &mockToolLister{}
	servers := []store.DownstreamServer{
		{ID: "srv1", Name: "A", ToolNamespace: "a", Discovery: "static"},
		{ID: "srv2", Name: "B", ToolNamespace: "b", Discovery: "static",
			CapabilitiesCache: json.RawMessage(`{}`)},
	}
	h, _ := newTestHandler(lister, servers)

	result := h.buildFromDBCache(context.Background(), []string{"srv1", "srv2"})
	if result != nil {
		t.Errorf("expected nil for empty caches, got %v", result)
	}
}

func TestBuildFromDBCache_ReturnsPopulatedCache(t *testing.T) {
	lister := &mockToolLister{}
	cached := toolsJSON(Tool{Name: "search", Description: "Search"})
	servers := []store.DownstreamServer{
		{ID: "srv1", Name: "A", ToolNamespace: "a", Discovery: "static",
			CapabilitiesCache: cached},
		{ID: "srv2", Name: "B", ToolNamespace: "b", Discovery: "static"},
	}
	h, _ := newTestHandler(lister, servers)

	result := h.buildFromDBCache(context.Background(), []string{"srv1", "srv2"})
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if _, ok := result["srv1"]; !ok {
		t.Error("expected srv1 in result")
	}
	if _, ok := result["srv2"]; ok {
		t.Error("srv2 should not be in result (empty cache)")
	}
}

func TestCachedListToolsSkipsStdioServersOnColdCache(t *testing.T) {
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"http":       toolsJSON(Tool{Name: "http_tool", Description: "HTTP"}),
			"safe-stdio": toolsJSON(Tool{Name: "stdio_tool", Description: "Stdio"}),
			"vercel":     toolsJSON(Tool{Name: "deploy", Description: "Deploy"}),
			"playwright": toolsJSON(Tool{Name: "browser_click", Description: "Click"}),
		},
	}
	servers := []store.DownstreamServer{
		{ID: "http", Name: "HTTP", Transport: "http", ToolNamespace: "http", Discovery: "dynamic"},
		{ID: "safe-stdio", Name: "Safe Stdio", Transport: "stdio", Command: "safe-mcp", ToolNamespace: "safe", Discovery: "dynamic"},
		{
			ID: "vercel", Name: "Vercel", Transport: "stdio", Command: "npx",
			Args:          json.RawMessage(`["mcp-remote","https://mcp.vercel.com"]`),
			ToolNamespace: "vercel", Discovery: "dynamic",
		},
		{
			ID: "playwright", Name: "Playwright", Transport: "stdio", Command: "npx",
			Args:          json.RawMessage(`["-y","@playwright/mcp@latest","--headless","--isolated"]`),
			ToolNamespace: "playwright", Discovery: "dynamic",
		},
	}
	h, _ := newTestHandler(lister, servers)

	got, err := h.cachedListToolsForServers(context.Background(), []string{"http", "safe-stdio", "vercel", "playwright"})
	if err != nil {
		t.Fatalf("cachedListToolsForServers: %v", err)
	}
	if _, ok := got["http"]; !ok {
		t.Fatal("expected http server tools")
	}
	if _, ok := got["safe-stdio"]; ok {
		t.Fatal("stdio server should not be queried on cold automatic catalog discovery")
	}
	if _, ok := got["vercel"]; ok {
		t.Fatal("vercel should not be queried on cold automatic catalog discovery")
	}
	if _, ok := got["playwright"]; ok {
		t.Fatal("playwright should not be queried on cold automatic catalog discovery")
	}
	if len(lister.listRequests) != 1 {
		t.Fatalf("ListToolsForServers calls = %d, want 1", len(lister.listRequests))
	}
	if strings.Join(lister.listRequests[0], ",") != "http" {
		t.Fatalf("queried servers = %v, want [http]", lister.listRequests[0])
	}
}

func TestBackgroundRefreshSkipsStdioServers(t *testing.T) {
	listed := make(chan []string, 1)
	lister := &mockToolLister{
		listed: listed,
		tools: map[string]json.RawMessage{
			"http":       toolsJSON(Tool{Name: "http_tool", Description: "HTTP"}),
			"safe-stdio": toolsJSON(Tool{Name: "stdio_tool", Description: "Stdio"}),
			"vercel":     toolsJSON(Tool{Name: "deploy", Description: "Deploy"}),
			"playwright": toolsJSON(Tool{Name: "browser_click", Description: "Click"}),
		},
	}
	servers := []store.DownstreamServer{
		{
			ID: "http", Name: "HTTP", Transport: "http",
			ToolNamespace:     "http",
			Discovery:         "dynamic",
			CapabilitiesCache: toolsJSON(Tool{Name: "http_cached", Description: "Cached"}),
		},
		{
			ID: "safe-stdio", Name: "Safe Stdio", Transport: "stdio", Command: "safe-mcp",
			ToolNamespace: "safe", Discovery: "dynamic",
			CapabilitiesCache: toolsJSON(Tool{Name: "stdio_cached", Description: "Cached"}),
		},
		{
			ID: "vercel", Name: "Vercel", Transport: "stdio", Command: "npx",
			Args:          json.RawMessage(`["mcp-remote","https://mcp.vercel.com"]`),
			ToolNamespace: "vercel", Discovery: "dynamic",
			CapabilitiesCache: toolsJSON(Tool{Name: "deploy_cached", Description: "Cached"}),
		},
		{
			ID: "playwright", Name: "Playwright", Transport: "stdio", Command: "npx",
			Args:              json.RawMessage(`["-y","@playwright/mcp@latest","--headless","--isolated"]`),
			ToolNamespace:     "playwright",
			Discovery:         "dynamic",
			CapabilitiesCache: toolsJSON(Tool{Name: "browser_click_cached", Description: "Cached"}),
		},
	}
	h, _ := newTestHandler(lister, servers)
	h.bgCtx = context.Background()

	got, err := h.cachedListToolsForServers(context.Background(), []string{"http", "safe-stdio", "vercel", "playwright"})
	if err != nil {
		t.Fatalf("cachedListToolsForServers: %v", err)
	}
	if _, ok := got["safe-stdio"]; !ok {
		t.Fatal("expected stale cached stdio tools to remain available")
	}
	if _, ok := got["vercel"]; !ok {
		t.Fatal("expected stale cached vercel tools to remain available")
	}
	if _, ok := got["playwright"]; !ok {
		t.Fatal("expected stale cached playwright tools to remain available")
	}

	select {
	case ids := <-listed:
		if strings.Join(ids, ",") != "http" {
			t.Fatalf("background refresh queried %v, want [http]", ids)
		}
	case <-time.After(time.Second):
		t.Fatal("background refresh did not run")
	}
}

func TestSearchPrefetchSkipsOnDemandServers(t *testing.T) {
	lister := &prefetchToolLister{
		prefetched: make(chan string, 4),
	}
	servers := []store.DownstreamServer{
		{ID: "http", Name: "HTTP", Transport: "http", ToolNamespace: "http", Discovery: "dynamic"},
		{ID: "safe-stdio", Name: "Safe Stdio", Transport: "stdio", Command: "safe-mcp", ToolNamespace: "safe", Discovery: "dynamic"},
		{
			ID: "vercel", Name: "Vercel", Transport: "stdio", Command: "npx",
			Args:          json.RawMessage(`["mcp-remote","https://mcp.vercel.com"]`),
			ToolNamespace: "vercel", Discovery: "dynamic",
		},
		{
			ID: "playwright", Name: "Playwright", Transport: "stdio", Command: "npx",
			Args:          json.RawMessage(`["-y","@playwright/mcp@latest","--headless","--isolated"]`),
			ToolNamespace: "playwright", Discovery: "dynamic",
		},
	}
	h, _ := newTestHandler(lister, servers)
	h.bgCtx = context.Background()

	h.prefetchServers(context.Background(), map[string]Tool{
		"http__search":                 {Name: "http__search"},
		"safe__search":                 {Name: "safe__search"},
		"vercel__deploy":               {Name: "vercel__deploy"},
		"playwright__browser_navigate": {Name: "playwright__browser_navigate"},
	})

	select {
	case got := <-lister.prefetched:
		if got != "http" {
			t.Fatalf("prefetched server = %q, want http", got)
		}
	case <-time.After(time.Second):
		t.Fatal("http server was not prefetched")
	}
	select {
	case got := <-lister.prefetched:
		t.Fatalf("unexpected on-demand prefetch for %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

// --- Reload Server Tests ---

func TestHandleReloadServer_UnknownServer(t *testing.T) {
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)
	h.bgCtx = context.Background()

	result, rpcErr := h.handleReloadServer(context.Background(), "nonexistent-id")
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %v", rpcErr)
	}
	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !tr.IsError || len(tr.Content) == 0 || !contains(tr.Content[0].Text, "not found") {
		t.Errorf("expected 'not found' error result, got: %+v", tr)
	}
}

func TestHandleReloadServer_AllServers(t *testing.T) {
	servers := []store.DownstreamServer{
		{ID: "srv1", ToolNamespace: "ns1", Discovery: "static"},
	}
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"srv1": toolsJSON(Tool{Name: "new_tool", Description: "A brand new tool"}),
		},
	}
	h, ms := newTestHandler(lister, servers)
	h.settingsSvc = config.NewSettingsService(ms)
	h.bgCtx = context.Background()

	result, rpcErr := h.handleReloadServer(context.Background(), "")
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %v", rpcErr)
	}
	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tr.IsError {
		t.Fatalf("unexpected error in result: %+v", tr)
	}
	if len(tr.Content) == 0 || !contains(tr.Content[0].Text, "srv1") {
		t.Errorf("expected srv1 in reload result, got: %+v", tr)
	}
	// After reload, capabilities cache in store should be updated.
	if _, ok := ms.capUpdates["srv1"]; !ok {
		t.Error("expected capabilities cache to be updated for srv1")
	}
	// The in-memory tools/list cache should be empty after reload (was flushed).
	if h.toolsListCache.Len() != 0 {
		t.Errorf("expected empty toolsListCache after reload, got %d entries", h.toolsListCache.Len())
	}
}

func TestHandleReloadServer_EvictsInstancesBeforeListing(t *testing.T) {
	servers := []store.DownstreamServer{
		{ID: "srv1", ToolNamespace: "ns1", Discovery: "static"},
	}
	lister := &reloadTrackingToolLister{
		mockToolLister: mockToolLister{
			tools: map[string]json.RawMessage{
				"srv1": toolsJSON(Tool{Name: "new_tool", Description: "A brand new tool"}),
			},
		},
	}
	h, _ := newTestHandler(lister, servers)

	result, rpcErr := h.handleReloadServer(context.Background(), "srv1")
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %v", rpcErr)
	}
	if got := strings.Join(lister.events, "|"); got != "reload:srv1|list:srv1" {
		t.Fatalf("events = %s, want reload before list", got)
	}
	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(tr.Content) == 0 || !contains(tr.Content[0].Text, "Evicted 1 live instance") {
		t.Fatalf("reload result did not report evicted instance: %+v", tr)
	}
}

func TestHandleReloadServer_AllServersSkipsOnDemandServers(t *testing.T) {
	servers := []store.DownstreamServer{
		{ID: "safe", ToolNamespace: "safe", Discovery: "static", Transport: "stdio", Command: "safe-mcp"},
		{
			ID: "vercel", ToolNamespace: "vercel", Discovery: "static", Transport: "stdio", Command: "npx",
			Args: json.RawMessage(`["mcp-remote","https://mcp.vercel.com"]`),
		},
		{
			ID: "playwright", ToolNamespace: "playwright", Discovery: "static", Transport: "stdio", Command: "npx",
			Args: json.RawMessage(`["-y","@playwright/mcp@latest","--headless","--isolated"]`),
		},
	}
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"safe":       toolsJSON(Tool{Name: "safe_tool", Description: "Safe"}),
			"vercel":     toolsJSON(Tool{Name: "deploy", Description: "Deploy"}),
			"playwright": toolsJSON(Tool{Name: "browser_click", Description: "Click"}),
		},
	}
	h, _ := newTestHandler(lister, servers)

	result, rpcErr := h.handleReloadServer(context.Background(), "")
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %v", rpcErr)
	}
	if len(lister.listRequests) != 1 {
		t.Fatalf("ListToolsForServers calls = %d, want 1", len(lister.listRequests))
	}
	if strings.Join(lister.listRequests[0], ",") != "safe" {
		t.Fatalf("queried servers = %v, want [safe]", lister.listRequests[0])
	}
	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(tr.Content) == 0 || !contains(tr.Content[0].Text, "safe") || contains(tr.Content[0].Text, "vercel") {
		t.Fatalf("expected only safe reload result, got %+v", tr)
	}
	if contains(tr.Content[0].Text, "playwright") {
		t.Fatalf("expected playwright to be skipped by all-server reload, got %+v", tr)
	}
}

func TestHandleReloadServer_SpecificOnDemandServerIsExplicit(t *testing.T) {
	servers := []store.DownstreamServer{
		{
			ID: "vercel", ToolNamespace: "vercel", Discovery: "static", Transport: "stdio", Command: "npx",
			Args: json.RawMessage(`["mcp-remote","https://mcp.vercel.com"]`),
		},
		{
			ID: "playwright", ToolNamespace: "playwright", Discovery: "static", Transport: "stdio", Command: "npx",
			Args: json.RawMessage(`["-y","@playwright/mcp@latest","--headless","--isolated"]`),
		},
	}
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"vercel":     toolsJSON(Tool{Name: "deploy", Description: "Deploy"}),
			"playwright": toolsJSON(Tool{Name: "browser_click", Description: "Click"}),
		},
	}
	h, _ := newTestHandler(lister, servers)

	for _, id := range []string{"vercel", "playwright"} {
		_, rpcErr := h.handleReloadServer(context.Background(), id)
		if rpcErr != nil {
			t.Fatalf("reload %s: unexpected RPC error: %v", id, rpcErr)
		}
	}
	if len(lister.listRequests) != 2 {
		t.Fatalf("ListToolsForServers calls = %d, want 2", len(lister.listRequests))
	}
	for i, want := range []string{"vercel", "playwright"} {
		if strings.Join(lister.listRequests[i], ",") != want {
			t.Fatalf("request %d queried servers = %v, want [%s]", i, lister.listRequests[i], want)
		}
	}
}

func TestHandleReloadServer_SpecificServer(t *testing.T) {
	servers := []store.DownstreamServer{
		{ID: "srv1", ToolNamespace: "ns1", Discovery: "static"},
		{ID: "srv2", ToolNamespace: "ns2", Discovery: "static"},
	}
	lister := &mockToolLister{
		tools: map[string]json.RawMessage{
			"srv1": toolsJSON(Tool{Name: "tool_a", Description: "A"}),
			"srv2": toolsJSON(Tool{Name: "tool_b", Description: "B"}),
		},
	}
	h, ms := newTestHandler(lister, servers)
	h.settingsSvc = config.NewSettingsService(ms)
	h.bgCtx = context.Background()

	result, rpcErr := h.handleReloadServer(context.Background(), "srv1")
	if rpcErr != nil {
		t.Fatalf("unexpected RPC error: %v", rpcErr)
	}
	var tr CallToolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tr.IsError {
		t.Fatalf("unexpected error: %+v", tr)
	}
	if _, ok := ms.capUpdates["srv1"]; !ok {
		t.Error("expected capabilities cache updated for srv1")
	}
	// srv2 should not have been touched.
	if _, ok := ms.capUpdates["srv2"]; ok {
		t.Error("srv2 should not have been updated")
	}
}

// TestHandleToolsList_ReloadServerDeferredUnderSlim verifies that
// mcpx__reload_server is hidden from the static tools/list under slim
// mode (the default) but still discoverable via searchableBuiltins —
// the canonical "callable but not advertised" pattern.
func TestHandleToolsList_ReloadServerDeferredUnderSlim(t *testing.T) {
	lister := &mockToolLister{tools: map[string]json.RawMessage{}}
	h, ms := newTestHandler(lister, nil)
	h.settingsSvc = config.NewSettingsService(ms)

	result, rpcErr := h.handleToolsList(context.Background())
	if rpcErr != nil {
		t.Fatalf("unexpected error: %v", rpcErr)
	}
	names := toolNames(result)
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if found["mcpx__reload_server"] {
		t.Errorf("mcpx__reload_server should be deferred under slim-surface mode, got %v", names)
	}

	// And it must remain discoverable: searchableBuiltins includes it under slim mode.
	deferred := h.searchableBuiltins(context.Background())
	deferredNames := map[string]bool{}
	for _, tool := range deferred {
		deferredNames[tool.Name] = true
	}
	if !deferredNames["mcpx__reload_server"] {
		t.Errorf("mcpx__reload_server missing from searchableBuiltins under slim-surface mode")
	}
}

// contains is a test helper for substring matching.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- MonitoringStore stubs (migration 128) ---

func (m *mockStore) CreateRemoteHost(context.Context, *store.RemoteHost) error { return nil }
func (m *mockStore) GetRemoteHost(context.Context, string) (*store.RemoteHost, error) {
	return nil, store.ErrRemoteHostNotFound
}
func (m *mockStore) ListRemoteHosts(context.Context, string) ([]*store.RemoteHost, error) {
	return nil, nil
}
func (m *mockStore) UpdateRemoteHost(context.Context, *store.RemoteHost) error { return nil }
func (m *mockStore) DeleteRemoteHost(context.Context, string) error            { return nil }
func (m *mockStore) SetRemoteHostPin(context.Context, string, string) error    { return nil }
func (m *mockStore) CreateLogSource(context.Context, *store.LogSource) error   { return nil }
func (m *mockStore) GetLogSource(context.Context, string) (*store.LogSource, error) {
	return nil, store.ErrLogSourceNotFound
}
func (m *mockStore) ListLogSources(context.Context, string) ([]*store.LogSource, error) {
	return nil, nil
}
func (m *mockStore) ListEnabledLogSources(context.Context) ([]*store.LogSource, error) {
	return nil, nil
}
func (m *mockStore) UpdateLogSource(context.Context, *store.LogSource) error { return nil }
func (m *mockStore) DeleteLogSource(context.Context, string) error           { return nil }
func (m *mockStore) UpdateLogSourceCursor(context.Context, string, time.Time, string) error {
	return nil
}
func (m *mockStore) SetLogSourceFailures(context.Context, string, int) error { return nil }
func (m *mockStore) CreateMonitoringChannel(context.Context, *store.MonitoringChannel) error {
	return nil
}
func (m *mockStore) GetMonitoringChannel(context.Context, string) (*store.MonitoringChannel, error) {
	return nil, store.ErrMonitoringChannelNotFound
}
func (m *mockStore) ListMonitoringChannels(context.Context, string) ([]*store.MonitoringChannel, error) {
	return nil, nil
}
func (m *mockStore) UpdateMonitoringChannel(context.Context, *store.MonitoringChannel) error {
	return nil
}
func (m *mockStore) DeleteMonitoringChannel(context.Context, string) error { return nil }

func (m *mockStore) UpsertLogTemplate(context.Context, *store.LogTemplate, int64) (bool, error) {
	return false, nil
}
func (m *mockStore) GetLogTemplate(context.Context, string) (*store.LogTemplate, error) {
	return nil, store.ErrLogTemplateNotFound
}
func (m *mockStore) ListLogTemplates(context.Context, []string, time.Time, int) ([]*store.LogTemplate, error) {
	return nil, nil
}
func (m *mockStore) AckLogTemplate(context.Context, string, string) error  { return nil }
func (m *mockStore) InsertLogLines(context.Context, []store.LogLine) error { return nil }
func (m *mockStore) PruneLogLines(context.Context, string, time.Time, int64) (int64, error) {
	return 0, nil
}
func (m *mockStore) CountLinesByTemplate(context.Context, []string, time.Time) (map[string]int64, error) {
	return nil, nil
}
func (m *mockStore) SearchLogLines(context.Context, string, string, int) ([]*store.LogLine, error) {
	return nil, nil
}
func (m *mockStore) ListLogLinesByTemplate(context.Context, string, int) ([]*store.LogLine, error) {
	return nil, nil
}
func (m *mockStore) CountErrorLinesInWindows(context.Context, string, time.Time, time.Time) (int64, int64, error) {
	return 0, 0, nil
}
func (m *mockStore) GetLogSourceErrorSpikeActive(context.Context, string) (bool, error) {
	return false, nil
}
func (m *mockStore) SetLogSourceErrorSpikeActive(context.Context, string, bool) error {
	return nil
}
