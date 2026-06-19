package routing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// mockRouteStore implements store.Store for routing engine tests.
// Only ListRouteRules and GetDownstreamServer are meaningful; all other methods are stubs.
type mockRouteStore struct {
	rules       map[string][]store.RouteRule
	downstreams map[string]*store.DownstreamServer
	// getDownstreamErr, when set, is returned for the given server ID by
	// GetDownstreamServer to simulate a transient (non-NotFound) store
	// failure. Used to exercise the namespace-resolution fail-closed path.
	getDownstreamErr map[string]error
}

func (m *mockRouteStore) ListRouteRules(_ context.Context, wsID string) ([]store.RouteRule, error) {
	return m.rules[wsID], nil
}
func (m *mockRouteStore) GetDownstreamServer(_ context.Context, id string) (*store.DownstreamServer, error) {
	if m.getDownstreamErr != nil {
		if err, ok := m.getDownstreamErr[id]; ok {
			return nil, err
		}
	}
	if m.downstreams != nil {
		if ds, ok := m.downstreams[id]; ok {
			return ds, nil
		}
	}
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) CreateRouteRule(context.Context, *store.RouteRule) error { return nil }
func (m *mockRouteStore) GetRouteRule(context.Context, string) (*store.RouteRule, error) {
	return nil, nil
}
func (m *mockRouteStore) UpdateRouteRule(context.Context, *store.RouteRule) error { return nil }
func (m *mockRouteStore) DeleteRouteRule(context.Context, string) error           { return nil }
func (m *mockRouteStore) CreateWorkspace(context.Context, *store.Workspace) error { return nil }
func (m *mockRouteStore) GetWorkspace(context.Context, string) (*store.Workspace, error) {
	return nil, nil
}
func (m *mockRouteStore) GetWorkspaceByName(context.Context, string) (*store.Workspace, error) {
	return nil, nil
}
func (m *mockRouteStore) ListWorkspaces(context.Context) ([]store.Workspace, error) { return nil, nil }
func (m *mockRouteStore) UpdateWorkspace(context.Context, *store.Workspace) error   { return nil }
func (m *mockRouteStore) DeleteWorkspace(context.Context, string) error             { return nil }
func (m *mockRouteStore) CreateAuthScope(context.Context, *store.AuthScope) error   { return nil }
func (m *mockRouteStore) GetAuthScope(context.Context, string) (*store.AuthScope, error) {
	return nil, nil
}
func (m *mockRouteStore) GetAuthScopeByName(context.Context, string) (*store.AuthScope, error) {
	return nil, nil
}
func (m *mockRouteStore) ListAuthScopes(context.Context) ([]store.AuthScope, error)      { return nil, nil }
func (m *mockRouteStore) UpdateAuthScope(context.Context, *store.AuthScope) error        { return nil }
func (m *mockRouteStore) DeleteAuthScope(context.Context, string) error                  { return nil }
func (m *mockRouteStore) UpdateAuthScopeTokenData(context.Context, string, []byte) error { return nil }
func (m *mockRouteStore) UpdateAuthScopeEncryptedData(context.Context, string, []byte) error {
	return nil
}
func (m *mockRouteStore) ListModelProfiles(context.Context) ([]store.ModelProfile, error) {
	return nil, nil
}
func (m *mockRouteStore) GetModelProfile(context.Context, string) (store.ModelProfile, error) {
	return store.ModelProfile{}, nil
}
func (m *mockRouteStore) GetModelProfileByName(context.Context, string) (store.ModelProfile, error) {
	return store.ModelProfile{}, nil
}
func (m *mockRouteStore) CreateModelProfile(context.Context, *store.ModelProfile) error   { return nil }
func (m *mockRouteStore) UpdateModelProfile(context.Context, *store.ModelProfile) error   { return nil }
func (m *mockRouteStore) DeleteModelProfile(context.Context, string) error                { return nil }
func (m *mockRouteStore) CreateOAuthProvider(context.Context, *store.OAuthProvider) error { return nil }
func (m *mockRouteStore) GetOAuthProvider(context.Context, string) (*store.OAuthProvider, error) {
	return nil, nil
}
func (m *mockRouteStore) GetOAuthProviderByName(context.Context, string) (*store.OAuthProvider, error) {
	return nil, nil
}
func (m *mockRouteStore) ListOAuthProviders(context.Context) ([]store.OAuthProvider, error) {
	return nil, nil
}
func (m *mockRouteStore) UpdateOAuthProvider(context.Context, *store.OAuthProvider) error { return nil }
func (m *mockRouteStore) DeleteOAuthProvider(context.Context, string) error               { return nil }
func (m *mockRouteStore) CreateDownstreamServer(context.Context, *store.DownstreamServer) error {
	return nil
}
func (m *mockRouteStore) GetDownstreamServerByName(context.Context, string) (*store.DownstreamServer, error) {
	return nil, nil
}
func (m *mockRouteStore) ListDownstreamServers(context.Context) ([]store.DownstreamServer, error) {
	return nil, nil
}
func (m *mockRouteStore) UpdateDownstreamServer(context.Context, *store.DownstreamServer) error {
	return nil
}
func (m *mockRouteStore) DeleteDownstreamServer(context.Context, string) error { return nil }
func (m *mockRouteStore) UpdateCapabilitiesCache(context.Context, string, json.RawMessage) error {
	return nil
}
func (m *mockRouteStore) CreateSession(context.Context, *store.Session) error        { return nil }
func (m *mockRouteStore) GetSession(context.Context, string) (*store.Session, error) { return nil, nil }
func (m *mockRouteStore) DisconnectSession(context.Context, string) error            { return nil }
func (m *mockRouteStore) DisconnectAllSessions(context.Context) (int, error)         { return 0, nil }
func (m *mockRouteStore) ListActiveSessions(context.Context) ([]store.Session, error) {
	return nil, nil
}
func (m *mockRouteStore) CleanupStaleSessions(context.Context, time.Time) (int, error)  { return 0, nil }
func (m *mockRouteStore) RecordHarnessInitialize(context.Context, string, string) error { return nil }
func (m *mockRouteStore) GetHarnessInitialization(context.Context, string) (*store.HarnessInitialization, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListHarnessInitializations(context.Context) ([]store.HarnessInitialization, error) {
	return nil, nil
}
func (m *mockRouteStore) UpsertHarnessBootstrap(context.Context, *store.HarnessInitialization) error {
	return nil
}
func (m *mockRouteStore) InsertAuditRecord(context.Context, *store.AuditRecord) error { return nil }
func (m *mockRouteStore) QueryAuditRecords(context.Context, store.AuditFilter) ([]store.AuditRecord, int, error) {
	return nil, 0, nil
}
func (m *mockRouteStore) GetAuditStats(context.Context, string, time.Time, time.Time) (*store.AuditStats, error) {
	return nil, nil
}
func (m *mockRouteStore) GetDashboardTimeSeries(context.Context, time.Time, time.Time) ([]store.TimeSeriesPoint, error) {
	return nil, nil
}
func (m *mockRouteStore) GetDashboardTimeSeriesBucketed(context.Context, time.Time, time.Time, int) ([]store.TimeSeriesPoint, error) {
	return nil, nil
}
func (m *mockRouteStore) GetToolLeaderboard(context.Context, time.Time, time.Time, int) ([]store.ToolLeaderboardEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) GetServerHealth(context.Context, time.Time, time.Time) ([]store.ServerHealthEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) GetErrorBreakdown(context.Context, time.Time, time.Time, int) ([]store.ErrorBreakdownEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) GetRouteHitMap(context.Context, time.Time, time.Time) ([]store.RouteHitEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) GetAuditCacheStats(context.Context, time.Time, time.Time) (*store.AuditCacheStats, error) {
	return nil, nil
}
func (m *mockRouteStore) CreateToolApproval(context.Context, *store.ToolApproval) error { return nil }
func (m *mockRouteStore) GetToolApproval(context.Context, string) (*store.ToolApproval, error) {
	return nil, nil
}
func (m *mockRouteStore) ListPendingApprovals(context.Context) ([]store.ToolApproval, error) {
	return nil, nil
}
func (m *mockRouteStore) ResolveToolApproval(context.Context, string, string, string, string, string) error {
	return nil
}
func (m *mockRouteStore) ExpirePendingApprovals(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) GetApprovalMetrics(context.Context, time.Time, time.Time) (*store.ApprovalMetrics, error) {
	return nil, nil
}
func (m *mockRouteStore) GetSettings(context.Context) (json.RawMessage, error) {
	return json.RawMessage("{}"), nil
}
func (m *mockRouteStore) UpdateSettings(context.Context, json.RawMessage) error       { return nil }
func (m *mockRouteStore) InsertMeshMessage(context.Context, *store.MeshMessage) error { return nil }
func (m *mockRouteStore) QueryMeshMessages(context.Context, store.MeshMessageFilter) ([]store.MeshMessage, error) {
	return nil, nil
}
func (m *mockRouteStore) GetMeshMessage(context.Context, string) (*store.MeshMessage, error) {
	return nil, nil
}
func (m *mockRouteStore) IncrementReplyCount(context.Context, string) error            { return nil }
func (m *mockRouteStore) ExtendMessageExpiry(context.Context, string, time.Time) error { return nil }
func (m *mockRouteStore) ArchiveExpiredMessages(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) ArchiveMessagesBySenderAndKinds(context.Context, []string, []string) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) ArchiveOldWorkerFindings(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (m *mockRouteStore) ListOrphanedDelegationRuns(context.Context) ([]*store.WorkerRun, error) {
	return nil, nil
}
func (m *mockRouteStore) DeleteArchivedMessages(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) CountLiveMessages(context.Context, string) (int, error) { return 0, nil }
func (m *mockRouteStore) ArchiveLowestPriority(context.Context, string, int) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) UpsertMeshAgent(context.Context, *store.MeshAgent) error { return nil }
func (m *mockRouteStore) SetMeshAgentStatus(context.Context, string, string, time.Time) error {
	return nil
}
func (m *mockRouteStore) SetMeshAgentTerminalLocator(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (m *mockRouteStore) FindRecentLocalAgentByClient(context.Context, string, string, string) (*store.MeshAgent, error) {
	return nil, nil
}
func (m *mockRouteStore) GetMeshAgent(context.Context, string) (*store.MeshAgent, error) {
	return nil, nil
}
func (m *mockRouteStore) ListActiveMeshAgents(context.Context, string, time.Time) ([]store.MeshAgent, error) {
	return nil, nil
}
func (m *mockRouteStore) UpdateAgentCursor(context.Context, string, string) error { return nil }
func (m *mockRouteStore) TouchMeshAgent(context.Context, string) error            { return nil }
func (m *mockRouteStore) DeleteMeshAgent(context.Context, string) error           { return nil }
func (m *mockRouteStore) DeleteMeshAgentsByOrigin(context.Context, string) (int, error) {
	return 0, nil
}

// RecipeStore implementations
func (m *mockRouteStore) UpsertRecipe(context.Context, *store.Recipe) error { return nil }
func (m *mockRouteStore) GetRecipe(context.Context, string) (*store.Recipe, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) GetRecipeByToolName(context.Context, string) (*store.Recipe, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListRecipes(context.Context, store.RecipeFilter) ([]store.Recipe, error) {
	return nil, nil
}
func (m *mockRouteStore) SearchRecipes(context.Context, store.RecipeFilter) ([]store.Recipe, error) {
	return nil, nil
}
func (m *mockRouteStore) DeleteRecipe(context.Context, string) error                    { return nil }
func (m *mockRouteStore) InsertFileClaim(context.Context, *store.FileClaim) error       { return nil }
func (m *mockRouteStore) UpsertRemoteFileClaim(context.Context, *store.FileClaim) error { return nil }
func (m *mockRouteStore) GetFileClaim(context.Context, string) (*store.FileClaim, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListFileClaims(context.Context, store.FileClaimFilter) ([]store.FileClaim, error) {
	return nil, nil
}
func (m *mockRouteStore) ReleaseFileClaim(context.Context, string, time.Time) error { return nil }
func (m *mockRouteStore) CreateToolDescriptionVersion(context.Context, *store.ToolDescriptionVersion) error {
	return nil
}
func (m *mockRouteStore) GetToolDescriptionVersion(context.Context, string) (*store.ToolDescriptionVersion, error) {
	return nil, nil
}
func (m *mockRouteStore) ListToolDescriptionVersions(context.Context, store.ToolDescriptionFilter) ([]store.ToolDescriptionVersion, int, error) {
	return nil, 0, nil
}
func (m *mockRouteStore) GetActiveDescriptions(context.Context) (map[string]string, error) {
	return nil, nil
}
func (m *mockRouteStore) ActivateVersion(context.Context, string, string, string) error { return nil }
func (m *mockRouteStore) RejectVersion(context.Context, string, string, string) error   { return nil }
func (m *mockRouteStore) HasPendingForToolBySession(context.Context, string, string) (bool, error) {
	return false, nil
}
func (m *mockRouteStore) Tx(context.Context, func(store.Store) error) error             { return nil }
func (m *mockRouteStore) Ping(context.Context) error                                    { return nil }
func (m *mockRouteStore) Close() error                                                  { return nil }
func (m *mockRouteStore) UpsertTelegramChat(context.Context, *store.TelegramChat) error { return nil }
func (m *mockRouteStore) GetTelegramChat(context.Context, string) (*store.TelegramChat, error) {
	return nil, nil
}
func (m *mockRouteStore) GetTelegramChatByNative(context.Context, string, string) (*store.TelegramChat, error) {
	return nil, nil
}
func (m *mockRouteStore) ListTelegramChats(context.Context) ([]store.TelegramChat, error) {
	return nil, nil
}
func (m *mockRouteStore) ListActiveTelegramChatsByWorkspace(context.Context, string) ([]store.TelegramChat, error) {
	return nil, nil
}
func (m *mockRouteStore) UpdateTelegramChatMinPriority(context.Context, string, string) error {
	return nil
}
func (m *mockRouteStore) DeactivateTelegramChat(context.Context, string) error { return nil }
func (m *mockRouteStore) TouchTelegramChat(context.Context, string) error      { return nil }
func (m *mockRouteStore) CreateTelegramPairing(context.Context, *store.TelegramPairing) error {
	return nil
}
func (m *mockRouteStore) GetTelegramPairing(context.Context, string) (*store.TelegramPairing, error) {
	return nil, nil
}
func (m *mockRouteStore) DeleteTelegramPairing(context.Context, string) error { return nil }
func (m *mockRouteStore) SweepExpiredTelegramPairings(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) InsertTelegramSentMessage(context.Context, *store.TelegramSentMessage) error {
	return nil
}
func (m *mockRouteStore) GetTelegramSentMessage(context.Context, string, string, string) (*store.TelegramSentMessage, error) {
	return nil, nil
}
func (m *mockRouteStore) UpsertGoogleChatSpace(context.Context, *store.GoogleChatSpace) error {
	return nil
}
func (m *mockRouteStore) GetGoogleChatSpace(context.Context, string) (*store.GoogleChatSpace, error) {
	return nil, nil
}
func (m *mockRouteStore) GetGoogleChatSpaceByName(context.Context, string) (*store.GoogleChatSpace, error) {
	return nil, nil
}
func (m *mockRouteStore) ListGoogleChatSpaces(context.Context) ([]store.GoogleChatSpace, error) {
	return nil, nil
}
func (m *mockRouteStore) ListActiveGoogleChatSpacesByWorkspace(context.Context, string) ([]store.GoogleChatSpace, error) {
	return nil, nil
}
func (m *mockRouteStore) UpdateGoogleChatSpaceMinPriority(context.Context, string, string) error {
	return nil
}
func (m *mockRouteStore) UpdateGoogleChatSpaceListenMode(context.Context, string, string) error {
	return nil
}
func (m *mockRouteStore) DeactivateGoogleChatSpace(context.Context, string) error { return nil }
func (m *mockRouteStore) TouchGoogleChatSpace(context.Context, string) error      { return nil }
func (m *mockRouteStore) CreateGoogleChatPairing(context.Context, *store.GoogleChatPairing) error {
	return nil
}
func (m *mockRouteStore) GetGoogleChatPairing(context.Context, string) (*store.GoogleChatPairing, error) {
	return nil, nil
}
func (m *mockRouteStore) DeleteGoogleChatPairing(context.Context, string) error { return nil }
func (m *mockRouteStore) SweepExpiredGoogleChatPairings(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) InsertGoogleChatSentMessage(context.Context, *store.GoogleChatSentMessage) error {
	return nil
}
func (m *mockRouteStore) GetGoogleChatSentMessage(context.Context, string, string) (*store.GoogleChatSentMessage, error) {
	return nil, nil
}
func (m *mockRouteStore) InsertSkillInvocation(context.Context, *store.SkillInvocation) error {
	return nil
}
func (m *mockRouteStore) ListSkillInvocations(context.Context, store.SkillInvocationFilter) ([]store.SkillInvocation, error) {
	return nil, nil
}
func (m *mockRouteStore) AddTrustedSigner(context.Context, *store.TrustedSigner) error { return nil }
func (m *mockRouteStore) RemoveTrustedSigner(context.Context, string) error            { return nil }
func (m *mockRouteStore) IsTrusted(context.Context, string) (bool, error)              { return false, nil }
func (m *mockRouteStore) ListTrustedSigners(context.Context) ([]store.TrustedSigner, error) {
	return nil, nil
}
func (m *mockRouteStore) UpsertInstalledSkill(context.Context, *store.InstalledSkill) error {
	return nil
}
func (m *mockRouteStore) GetInstalledSkill(context.Context, string) (*store.InstalledSkill, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListInstalledSkills(context.Context) ([]store.InstalledSkill, error) {
	return nil, nil
}
func (m *mockRouteStore) DeleteInstalledSkill(context.Context, string) error { return nil }

// SkillRegistryStore stubs — never exercised by routing tests.
func (m *mockRouteStore) PublishSkillRegistryEntry(context.Context, *store.SkillRegistryEntry) (bool, error) {
	return false, nil
}
func (m *mockRouteStore) GetSkillRegistryBundle(context.Context, *string, string, int) ([]byte, string, error) {
	return nil, "", store.ErrNotFound
}
func (m *mockRouteStore) GetSkillRegistryEntry(context.Context, *string, string, int) (*store.SkillRegistryEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) GetSkillRegistryHead(context.Context, store.SkillScope, string) (*store.SkillRegistryEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListSkillRegistryHeads(context.Context, store.SkillScope, int) ([]store.SkillRegistryEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) ListSkillRegistryVersions(context.Context, store.SkillScope, string, bool) ([]store.SkillRegistryEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) SoftDeleteSkillRegistryEntry(context.Context, *string, string, int) error {
	return nil
}
func (m *mockRouteStore) SetSkillRegistryTag(context.Context, *store.SkillRegistryTag) error {
	return nil
}
func (m *mockRouteStore) GetSkillRegistryTag(context.Context, string, string) (*store.SkillRegistryTag, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) DeleteSkillRegistryTag(context.Context, string, string) error { return nil }

// WorkerTemplateStore stubs — never exercised by routing tests.
func (m *mockRouteStore) PublishWorkerTemplate(context.Context, *store.WorkerTemplateEntry) (bool, error) {
	return false, nil
}
func (m *mockRouteStore) GetWorkerTemplate(context.Context, *string, string, int) (*store.WorkerTemplateEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) GetWorkerTemplateHead(context.Context, store.SkillScope, string) (*store.WorkerTemplateEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListWorkerTemplateHeads(context.Context, store.SkillScope, int) ([]store.WorkerTemplateEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) ListWorkerTemplateVersions(context.Context, store.SkillScope, string, bool) ([]store.WorkerTemplateEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) SoftDeleteWorkerTemplate(context.Context, *string, string, int) error {
	return nil
}

// MemoryStore stubs — never exercised by routing tests.
func (m *mockRouteStore) WriteMemory(context.Context, *store.MemoryEntry) error  { return nil }
func (m *mockRouteStore) UpdateMemory(context.Context, *store.MemoryEntry) error { return nil }
func (m *mockRouteStore) GetMemory(context.Context, string) (*store.MemoryEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) GetMemoryForPeer(context.Context, string, []string, bool) (*store.MemoryEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListMemories(context.Context, store.MemoryFilter) ([]store.MemoryEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) SearchMemories(context.Context, store.MemoryFilter, string) ([]store.MemoryHit, error) {
	return nil, nil
}
func (m *mockRouteStore) VectorSearchMemories(context.Context, store.MemoryFilter, string, []float32, int) ([]store.MemoryHit, error) {
	return nil, nil
}
func (m *mockRouteStore) UpsertMemoryEmbedding(context.Context, string, string, int, []float32) error {
	return nil
}
func (m *mockRouteStore) GetMemoryEmbedding(context.Context, string) (string, []float32, error) {
	return "", nil, store.ErrNotFound
}
func (m *mockRouteStore) InvalidateMemory(context.Context, string, string) error { return nil }
func (m *mockRouteStore) SoftDeleteMemory(context.Context, string) error         { return nil }
func (m *mockRouteStore) ForgetMemoryBySource(context.Context, string, store.SkillScope) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) CountMemories(context.Context, store.SkillScope) (int, int, error) {
	return 0, 0, nil
}
func (m *mockRouteStore) GetMemoryStats(context.Context, store.SkillScope) (store.MemoryStats, error) {
	return store.MemoryStats{}, nil
}
func (m *mockRouteStore) SetMemoryPinned(context.Context, string, bool) error         { return nil }
func (m *mockRouteStore) UpsertMemoryOffer(context.Context, *store.MemoryOffer) error { return nil }
func (m *mockRouteStore) GetMemoryOffer(context.Context, string) (*store.MemoryOffer, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListMemoryOffers(context.Context, store.MemoryOfferFilter) ([]store.MemoryOffer, error) {
	return nil, nil
}
func (m *mockRouteStore) AcceptMemoryOffer(context.Context, string, string) error { return nil }
func (m *mockRouteStore) DeclineMemoryOffer(context.Context, string) error        { return nil }
func (m *mockRouteStore) LinkMemoryEntity(context.Context, string, store.EntityRef, string) error {
	return nil
}
func (m *mockRouteStore) UnlinkMemoryEntity(context.Context, string, store.EntityRef) error {
	return nil
}
func (m *mockRouteStore) ListMemoryEntities(context.Context, string) ([]store.MemoryEntityRow, error) {
	return nil, nil
}
func (m *mockRouteStore) WritePerson(context.Context, *store.PersonEntry) error  { return nil }
func (m *mockRouteStore) UpdatePerson(context.Context, *store.PersonEntry) error { return nil }
func (m *mockRouteStore) GetPerson(context.Context, string) (*store.PersonEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListPeople(context.Context, store.PersonFilter) ([]store.PersonEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) SearchPeople(context.Context, store.PersonFilter, string) ([]store.PersonHit, error) {
	return nil, nil
}
func (m *mockRouteStore) SoftDeletePerson(context.Context, string) error { return nil }
func (m *mockRouteStore) CountPeople(context.Context) (int, error)       { return 0, nil }
func (m *mockRouteStore) LinkPersonEntity(context.Context, string, store.EntityRef, string) error {
	return nil
}
func (m *mockRouteStore) UnlinkPersonEntity(context.Context, string, store.EntityRef) error {
	return nil
}
func (m *mockRouteStore) ListPersonEntities(context.Context, string) ([]store.PersonEntityRow, error) {
	return nil, nil
}
func (m *mockRouteStore) ListEntities(context.Context, store.EntityFilter) ([]store.EntitySummary, error) {
	return nil, nil
}
func (m *mockRouteStore) RelatedEntities(context.Context, store.EntityRef, store.SkillScope, int) ([]store.EntityCoLink, error) {
	return nil, nil
}
func (m *mockRouteStore) BuildEntityGraph(context.Context, store.SkillScope, int, int) (store.EntityGraph, error) {
	return store.EntityGraph{}, nil
}
func (m *mockRouteStore) LogMemoryRecallEvents(context.Context, []store.MemoryRecallEvent) error {
	return nil
}
func (m *mockRouteStore) CoRecalledMemories(context.Context, string, store.SkillScope, int) ([]store.CoRecalledMemory, error) {
	return nil, nil
}
func (m *mockRouteStore) GetMemoryRecallStats(context.Context, []string) (map[string]store.MemoryRecallStat, error) {
	return nil, nil
}
func (m *mockRouteStore) ForgetRecallEventsBySource(context.Context, string, store.SkillScope) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) InsertChatTurnSignal(context.Context, *store.ChatTurnSignal) error {
	return nil
}
func (m *mockRouteStore) ListChatTurnSignals(context.Context, store.ChatTurnSignalFilter) ([]store.ChatTurnSignal, error) {
	return nil, nil
}
func (m *mockRouteStore) MarkChatTurnSignalPromoted(context.Context, string, string) error {
	return nil
}
func (m *mockRouteStore) ForgetChatTurnSignalsBySource(context.Context, string) (int, error) {
	return 0, nil
}

// P2PPeerStore stubs — never exercised by routing tests.
func (m *mockRouteStore) AddPeer(context.Context, *store.P2PPeer) error             { return nil }
func (m *mockRouteStore) GetPeer(context.Context, string) (*store.P2PPeer, error)   { return nil, nil }
func (m *mockRouteStore) ListPeers(context.Context) ([]store.P2PPeer, error)        { return nil, nil }
func (m *mockRouteStore) RevokePeer(context.Context, string) error                  { return nil }
func (m *mockRouteStore) UnrevokePeer(context.Context, string) error                { return nil }
func (m *mockRouteStore) GrantPeerScope(context.Context, string, string) error      { return nil }
func (m *mockRouteStore) RevokePeerScope(context.Context, string, string) error     { return nil }
func (m *mockRouteStore) UpdateLastSeen(context.Context, string, time.Time) error   { return nil }
func (m *mockRouteStore) UpdateDisplayName(context.Context, string, string) error   { return nil }
func (m *mockRouteStore) SetPeerSSHTarget(context.Context, string, string) error    { return nil }
func (m *mockRouteStore) RememberPeerAddrs(context.Context, string, []string) error { return nil }
func (m *mockRouteStore) LoadPeerAddrs(context.Context, string) ([]string, error)   { return nil, nil }
func (m *mockRouteStore) CreatePendingPair(context.Context, *store.P2PPendingPair) error {
	return nil
}
func (m *mockRouteStore) GetPendingPair(context.Context, string) (*store.P2PPendingPair, error) {
	return nil, nil
}
func (m *mockRouteStore) DeletePendingPair(context.Context, string) error { return nil }
func (m *mockRouteStore) SweepExpiredPendingPairs(context.Context, time.Time) (int, error) {
	return 0, nil
}

// UserStore stubs (M7.1) — routing tests never exercise users.
func (m *mockRouteStore) CreateUser(context.Context, *store.User) error               { return nil }
func (m *mockRouteStore) GetUser(context.Context, string) (*store.User, error)        { return nil, nil }
func (m *mockRouteStore) GetSelfUser(context.Context) (*store.User, error)            { return nil, nil }
func (m *mockRouteStore) ListUsers(context.Context) ([]store.User, error)             { return nil, nil }
func (m *mockRouteStore) UpdateUserDisplayName(context.Context, string, string) error { return nil }
func (m *mockRouteStore) UpsertUser(context.Context, string, string) error            { return nil }
func (m *mockRouteStore) LinkPeerToUser(context.Context, string, string) error        { return nil }
func (m *mockRouteStore) GetUserForPeer(context.Context, string) (*store.User, error) {
	return nil, nil
}
func (m *mockRouteStore) ListPeersForUser(context.Context, string) ([]store.P2PPeer, error) {
	return nil, nil
}

// SecretPromptStore stubs.
func (m *mockRouteStore) CreateSecretPrompt(context.Context, *store.SecretPrompt) error {
	return nil
}
func (m *mockRouteStore) GetSecretPrompt(context.Context, string) (*store.SecretPrompt, error) {
	return nil, nil
}
func (m *mockRouteStore) ListPendingSecretPrompts(context.Context) ([]store.SecretPrompt, error) {
	return nil, nil
}
func (m *mockRouteStore) CompleteSecretPrompt(context.Context, string, string, string, time.Time) error {
	return nil
}
func (m *mockRouteStore) ListExpiredSecretPrompts(context.Context, time.Time) ([]store.SecretPrompt, error) {
	return nil, nil
}

// MeshOutboundQueueStore — no-op stubs for the offline-delivery queue
// added in v0.7.4. Routing tests don't exercise mesh delivery.
func (m *mockRouteStore) EnqueueMeshOutbound(context.Context, *store.MeshOutbound) error { return nil }
func (m *mockRouteStore) ListDueMeshOutbound(context.Context, string, time.Time, int) ([]store.MeshOutbound, error) {
	return nil, nil
}
func (m *mockRouteStore) MarkMeshOutboundDelivered(context.Context, string, time.Time) error {
	return nil
}
func (m *mockRouteStore) BumpMeshOutboundAttempt(context.Context, string, string, time.Time) error {
	return nil
}
func (m *mockRouteStore) ListPendingMeshOutbound(context.Context, time.Time, int) ([]store.MeshOutbound, error) {
	return nil, nil
}
func (m *mockRouteStore) ListExpiredMeshOutbound(context.Context, time.Time, int) ([]store.MeshOutbound, error) {
	return nil, nil
}
func (m *mockRouteStore) PruneMeshOutbound(context.Context, time.Time, time.Time) (int, error) {
	return 0, nil
}

// Skill telemetry (W2) — stubs.
func (m *mockRouteStore) RecordSkillRun(context.Context, *store.SkillRun) error { return nil }
func (m *mockRouteStore) UpdateSkillRun(context.Context, string, store.SkillRunPatch) error {
	return nil
}
func (m *mockRouteStore) GetSkillRun(context.Context, string) (*store.SkillRun, error) {
	return nil, nil
}
func (m *mockRouteStore) ListSkillRuns(context.Context, store.SkillRunFilter) ([]store.SkillRun, error) {
	return nil, nil
}

// Skill refinement (W3) — stubs.
func (m *mockRouteStore) RecordRefinementProposal(context.Context, *store.SkillRefinementProposal) error {
	return nil
}
func (m *mockRouteStore) UpdateRefinementProposal(context.Context, string, store.RefinementProposalPatch) error {
	return nil
}
func (m *mockRouteStore) GetRefinementProposal(context.Context, string) (*store.SkillRefinementProposal, error) {
	return nil, nil
}
func (m *mockRouteStore) ListRefinementProposals(context.Context, store.RefinementFilter) ([]store.SkillRefinementProposal, error) {
	return nil, nil
}
func (m *mockRouteStore) CountSimilarProposals(context.Context, string, string) (int, error) {
	return 0, nil
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**", "", true},
		{"**", "a/b/c", true},
		{"*", "foo", true},
		{"*", "foo/bar", false},
		{"src/**", "src/main.go", true},
		{"src/**", "src/pkg/util.go", true},
		{"src/**", "lib/main.go", false},
		{"src/*/main.go", "src/pkg/main.go", true},
		{"src/*/main.go", "src/pkg/sub/main.go", false},
		{"exact/path", "exact/path", true},
		{"exact/path", "exact/other", false},
		{"**/test", "a/b/test", true},
		{"**/test", "test", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.path, func(t *testing.T) {
			got := GlobMatch(tt.pattern, tt.path)
			if got != tt.want {
				t.Errorf("GlobMatch(%q, %q) = %v, want %v",
					tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestGlobSpecificity(t *testing.T) {
	tests := []struct {
		pattern string
		want    int
	}{
		{"**", 0},
		{"*", 1},
		{"src/**", 10},
		{"src/pkg/*", 21},
		{"src/pkg/main.go", 30},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got := GlobSpecificity(tt.pattern)
			if got != tt.want {
				t.Errorf("GlobSpecificity(%q) = %d, want %d",
					tt.pattern, got, tt.want)
			}
		})
	}
}

func TestMatchTool(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		patterns []string
		want     bool
	}{
		{"wildcard", "anything", []string{"*"}, true},
		{"exact", "github__create_issue", []string{"github__create_issue"}, true},
		{"prefix", "github__create_issue", []string{"github__*"}, true},
		{"no match", "slack__post", []string{"github__*"}, false},
		{"multi", "slack__post", []string{"github__*", "slack__*"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchTool(tt.tool, tt.patterns)
			if got != tt.want {
				t.Errorf("matchTool(%q, %v) = %v, want %v",
					tt.tool, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestMatchRoute(t *testing.T) {
	rules := []parsedRule{
		{
			RouteRule: store.RouteRule{
				ID: "deny-rule", Priority: 100, PathGlob: "**",
				DownstreamServerID: "ds1", Policy: "deny",
			},
			toolPatterns: []string{"dangerous__*"},
			specificity:  0,
		},
		{
			RouteRule: store.RouteRule{
				ID: "allow-gh", Priority: 100, PathGlob: "**",
				DownstreamServerID: "gh-server", AuthScopeID: "gh-auth",
				Policy: "allow",
			},
			toolPatterns: []string{"github__*"},
			specificity:  0,
		},
		{
			RouteRule: store.RouteRule{
				ID: "allow-slack", Priority: 50, PathGlob: "**",
				DownstreamServerID: "slack-server", Policy: "allow",
			},
			toolPatterns: []string{"slack__*"},
			specificity:  0,
		},
	}
	sortRules(rules)

	tests := []struct {
		name    string
		ctx     RouteContext
		wantDS  string
		wantErr error
	}{
		{
			"match github",
			RouteContext{ToolName: "github__create_issue", Subpath: "src"},
			"gh-server", nil,
		},
		{
			"match slack lower priority",
			RouteContext{ToolName: "slack__post_message", Subpath: "any"},
			"slack-server", nil,
		},
		{
			"deny dangerous",
			RouteContext{ToolName: "dangerous__delete_all", Subpath: "any"},
			"", ErrDenied,
		},
		{
			"specificity override: more specific allow wins over less specific deny",
			RouteContext{ToolName: "linear__search", Subpath: "work/gateway/src"},
			"linear-srv", nil,
		},
		{
			"specificity override: less specific deny still blocks others",
			RouteContext{ToolName: "linear__search", Subpath: "work/other"},
			"", ErrDenied,
		},
		{
			"no match",
			RouteContext{ToolName: "unknown__tool", Subpath: "any"},
			"", ErrNoRoute,
		},
	}

	// Add specificity override rules for the new test cases.
	rules = append(rules,
		parsedRule{
			RouteRule: store.RouteRule{
				ID: "linear-deny", Priority: 0, PathGlob: "work/**",
				Policy: "deny",
			},
			toolPatterns: []string{"linear__*"},
			specificity:  10,
		},
		parsedRule{
			RouteRule: store.RouteRule{
				ID: "linear-allow-specific", Priority: 0, PathGlob: "work/gateway/**",
				DownstreamServerID: "linear-srv", Policy: "allow",
			},
			toolPatterns: []string{"linear__*"},
			specificity:  20,
		},
	)
	sortRules(rules)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := matchRoute(rules, tt.ctx)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if result.DownstreamServerID != tt.wantDS {
				t.Errorf("ds = %q, want %q",
					result.DownstreamServerID, tt.wantDS)
			}
		})
	}
}

func TestSortRules(t *testing.T) {
	rules := []parsedRule{
		{RouteRule: store.RouteRule{ID: "c", Priority: 50, PathGlob: "**"}, specificity: 0, toolSpecificity: 0},
		{RouteRule: store.RouteRule{ID: "a", Priority: 100, PathGlob: "src/*"}, specificity: 11, toolSpecificity: 0},
		{RouteRule: store.RouteRule{ID: "b", Priority: 100, PathGlob: "**"}, specificity: 0, toolSpecificity: 0},
		{RouteRule: store.RouteRule{ID: "d", Priority: 100, PathGlob: "**"}, specificity: 0, toolSpecificity: 1}, // More specific tool wins over "b"
	}

	sortRules(rules)

	want := []string{"a", "d", "b", "c"}
	for i, r := range rules {
		if r.ID != want[i] {
			t.Errorf("rules[%d].ID = %q, want %q", i, r.ID, want[i])
		}
	}
}

func TestSortRules_SpecificityBeatsPriority(t *testing.T) {
	// A high-priority catch-all must NOT beat a low-priority specific path.
	rules := []parsedRule{
		{RouteRule: store.RouteRule{ID: "catchall", Priority: 1000, PathGlob: "**"}, specificity: 0},
		{RouteRule: store.RouteRule{ID: "specific", Priority: 1, PathGlob: "src/components/auth"}, specificity: 30},
	}

	sortRules(rules)

	if rules[0].ID != "specific" {
		t.Errorf("expected most-specific path first, got %q", rules[0].ID)
	}
	if rules[1].ID != "catchall" {
		t.Errorf("expected catch-all second, got %q", rules[1].ID)
	}
}

// Verify parseToolMatch handles edge cases.
func TestParseToolMatch(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want []string
	}{
		{"nil", nil, []string{"*"}},
		{"empty array", json.RawMessage(`[]`), []string{"*"}},
		{"valid", json.RawMessage(`["github__*","slack__post"]`), []string{"github__*", "slack__post"}},
		{"invalid json", json.RawMessage(`not json`), []string{"*"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseToolMatch(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestRouteWithFallback(t *testing.T) {
	ms := &mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws-project": {
				// Project workspace has a specific rule for github tools.
				{
					ID: "proj-gh", WorkspaceID: "ws-project",
					Priority: 100, PathGlob: "**",
					DownstreamServerID: "gh-server", AuthScopeID: "gh-auth",
					Policy: "allow", ToolMatch: json.RawMessage(`["github__*"]`),
				},
			},
			"ws-global": {
				// Global workspace allows postgres tools.
				{
					ID: "global-pg", WorkspaceID: "ws-global",
					Priority: 100, PathGlob: "**",
					DownstreamServerID: "pg-server", AuthScopeID: "pg-auth",
					Policy: "allow", ToolMatch: json.RawMessage(`["postgres__*"]`),
				},
				// Global also allows github tools (should not be reached if project matches).
				{
					ID: "global-gh", WorkspaceID: "ws-global",
					Priority: 100, PathGlob: "**",
					DownstreamServerID: "gh-server-global", AuthScopeID: "gh-auth",
					Policy: "allow", ToolMatch: json.RawMessage(`["github__*"]`),
				},
			},
			"ws-deny": {
				// Deny workspace blocks postgres tools.
				{
					ID: "deny-pg", WorkspaceID: "ws-deny",
					Priority: 100, PathGlob: "**",
					Policy: "deny", ToolMatch: json.RawMessage(`["postgres__*"]`),
				},
			},
		},
	}
	engine := NewEngine(ms)

	tests := []struct {
		name       string
		tool       string
		clientRoot string
		ancestors  []WorkspaceAncestor
		wantDS     string
		wantWsID   string
		wantWsName string
		wantSub    string
		wantErr    error
	}{
		{
			name:       "first workspace matches",
			tool:       "github__create_issue",
			clientRoot: "/home/user/project",
			ancestors: []WorkspaceAncestor{
				{ID: "ws-project", Name: "Project", RootPath: "/home/user/project"},
				{ID: "ws-global", Name: "Global", RootPath: "/"},
			},
			wantDS: "gh-server", wantWsID: "ws-project", wantWsName: "Project", wantSub: "",
		},
		{
			name:       "fallback to parent workspace",
			tool:       "postgres__query",
			clientRoot: "/home/user/project",
			ancestors: []WorkspaceAncestor{
				{ID: "ws-project", Name: "Project", RootPath: "/home/user/project"},
				{ID: "ws-global", Name: "Global", RootPath: "/"},
			},
			wantDS: "pg-server", wantWsID: "ws-global", wantWsName: "Global", wantSub: "home/user/project",
		},
		{
			name:       "deny blocks fallback",
			tool:       "postgres__query",
			clientRoot: "/home/user/project",
			ancestors: []WorkspaceAncestor{
				{ID: "ws-deny", Name: "Deny", RootPath: "/home/user/project"},
				{ID: "ws-global", Name: "Global", RootPath: "/"},
			},
			wantErr: ErrDenied,
		},
		{
			name:       "empty chain uses default route",
			tool:       "github__create_issue",
			clientRoot: "",
			ancestors:  nil,
			wantErr:    ErrNoRoute,
		},
		{
			name:       "all workspaces miss",
			tool:       "unknown__tool",
			clientRoot: "/home/user/project",
			ancestors: []WorkspaceAncestor{
				{ID: "ws-project", Name: "Project", RootPath: "/home/user/project"},
				{ID: "ws-global", Name: "Global", RootPath: "/"},
			},
			wantErr: ErrNoRoute,
		},
		{
			name:       "single workspace match",
			tool:       "postgres__query",
			clientRoot: "/home/user/project",
			ancestors:  []WorkspaceAncestor{{ID: "ws-global", Name: "Global", RootPath: "/"}},
			wantDS:     "pg-server", wantWsID: "ws-global", wantWsName: "Global", wantSub: "home/user/project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.RouteWithFallback(t.Context(), RouteContext{
				ToolName: tt.tool,
			}, tt.clientRoot, tt.ancestors)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if result.DownstreamServerID != tt.wantDS {
				t.Errorf("ds = %q, want %q",
					result.DownstreamServerID, tt.wantDS)
			}
			if result.MatchedWorkspaceID != tt.wantWsID {
				t.Errorf("workspace_id = %q, want %q",
					result.MatchedWorkspaceID, tt.wantWsID)
			}
			if result.MatchedWorkspaceName != tt.wantWsName {
				t.Errorf("workspace_name = %q, want %q",
					result.MatchedWorkspaceName, tt.wantWsName)
			}
			if result.Subpath != tt.wantSub {
				t.Errorf("subpath = %q, want %q",
					result.Subpath, tt.wantSub)
			}
		})
	}
}

func TestRouteWithFallback_PathScoped(t *testing.T) {
	ms := &mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws-project": {
				// Only allows github tools under src/**.
				{
					ID: "src-only", WorkspaceID: "ws-project",
					Priority: 100, PathGlob: "src/**",
					DownstreamServerID: "gh-server", AuthScopeID: "gh-auth",
					Policy: "allow", ToolMatch: json.RawMessage(`["github__*"]`),
				},
				// A catch-all for slack tools from anywhere.
				{
					ID: "slack-all", WorkspaceID: "ws-project",
					Priority: 50, PathGlob: "**",
					DownstreamServerID: "slack-server",
					Policy:             "allow", ToolMatch: json.RawMessage(`["slack__*"]`),
				},
			},
		},
	}
	engine := NewEngine(ms)

	tests := []struct {
		name       string
		tool       string
		clientRoot string
		wantDS     string
		wantSub    string
		wantErr    error
	}{
		{
			"path-scoped rule matches when client is under src",
			"github__create_issue",
			"/home/user/project/src/api",
			"gh-server", "src/api", nil,
		},
		{
			"path-scoped rule does NOT match at workspace root",
			"github__create_issue",
			"/home/user/project",
			"", "", ErrNoRoute,
		},
		{
			"path-scoped rule does NOT match outside src",
			"github__create_issue",
			"/home/user/project/docs",
			"", "", ErrNoRoute,
		},
		{
			"catch-all glob matches from workspace root",
			"slack__post_message",
			"/home/user/project",
			"slack-server", "", nil,
		},
		{
			"catch-all glob matches from subdirectory",
			"slack__post_message",
			"/home/user/project/src/api",
			"slack-server", "src/api", nil,
		},
	}

	ancestors := []WorkspaceAncestor{
		{ID: "ws-project", Name: "Project", RootPath: "/home/user/project"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.RouteWithFallback(t.Context(), RouteContext{
				ToolName: tt.tool,
			}, tt.clientRoot, ancestors)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if result.DownstreamServerID != tt.wantDS {
				t.Errorf("ds = %q, want %q",
					result.DownstreamServerID, tt.wantDS)
			}
			if result.Subpath != tt.wantSub {
				t.Errorf("subpath = %q, want %q",
					result.Subpath, tt.wantSub)
			}
			if result.MatchedWorkspaceID != "ws-project" {
				t.Errorf("workspace_id = %q, want %q",
					result.MatchedWorkspaceID, "ws-project")
			}
		})
	}
}

func TestComputeSubpath(t *testing.T) {
	tests := []struct {
		name       string
		clientRoot string
		wsRoot     string
		want       string
	}{
		{"same path", "/home/user/project", "/home/user/project", ""},
		{"client under workspace", "/home/user/project/src/api", "/home/user/project", "src/api"},
		{"at workspace root", "/home/user/project", "/home/user/project", ""},
		{"empty client", "", "/home/user/project", ""},
		{"empty workspace", "/home/user/project", "", ""},
		{"client outside workspace", "/opt/other", "/home/user/project", ""},
		{"root workspace", "/home/user/project", "/", "home/user/project"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeSubpath(tt.clientRoot, tt.wsRoot)
			if got != tt.want {
				t.Errorf("ComputeSubpath(%q, %q) = %q, want %q",
					tt.clientRoot, tt.wsRoot, got, tt.want)
			}
		})
	}
}

func TestMatchRoute_NamespaceAware(t *testing.T) {
	rules := []parsedRule{
		{
			RouteRule: store.RouteRule{
				ID: "allow-linear", Priority: 100, PathGlob: "**",
				DownstreamServerID: "linear-server", Policy: "allow",
			},
			toolPatterns: []string{"*"},
			specificity:  0,
			namespace:    "linear",
		},
		{
			RouteRule: store.RouteRule{
				ID: "global-deny", Priority: 0, PathGlob: "**",
				Policy: "deny",
			},
			toolPatterns: []string{"*"},
			specificity:  0,
		},
	}
	sortRules(rules)

	tests := []struct {
		name    string
		tool    string
		wantDS  string
		wantErr error
	}{
		{"linear tool matches linear rule", "linear__search", "linear-server", nil},
		{"github tool skips linear rule, hits deny", "github__get_label", "", ErrDenied},
		{"no-namespace tool skips linear rule, hits deny", "some_tool", "", ErrDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := matchRoute(rules, RouteContext{ToolName: tt.tool})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if result.DownstreamServerID != tt.wantDS {
				t.Errorf("ds = %q, want %q", result.DownstreamServerID, tt.wantDS)
			}
		})
	}
}

// TestRoute_NamespaceResolutionFailsClosed is the regression test for the
// namespace-guard bypass bug. Previously resolveNamespaces swallowed the error
// from GetDownstreamServer, leaving the rule's namespace empty so the guard at
// matchRoute was skipped. A wildcard ["*"] allow rule whose downstream HAS a
// namespace would then catch tools for OTHER namespaces and route them to the
// wrong server (defeating the guard's purpose), and the bad parse was cached
// for the whole TTL. The engine must instead fail closed: a transient lookup
// failure returns an error rather than misrouting, and the partial result is
// NOT cached. A deleted server (store.ErrNotFound) is still treated as a legit
// empty namespace.
func TestRoute_NamespaceResolutionFailsClosed(t *testing.T) {
	transient := errors.New("transient store failure")

	newEngine := func(getErr map[string]error, downstreams map[string]*store.DownstreamServer) *Engine {
		ms := &mockRouteStore{
			rules: map[string][]store.RouteRule{
				"ws1": {
					// Wildcard allow rule pointing at a namespaced server.
					{
						ID: "allow-linear", WorkspaceID: "ws1",
						Priority: 100, PathGlob: "**",
						DownstreamServerID: "linear-server",
						Policy:             "allow", ToolMatch: json.RawMessage(`["*"]`),
					},
				},
			},
			downstreams:      downstreams,
			getDownstreamErr: getErr,
		}
		return NewEngine(ms)
	}

	t.Run("transient failure fails closed, does not misroute foreign tool", func(t *testing.T) {
		engine := newEngine(
			map[string]error{"linear-server": transient},
			map[string]*store.DownstreamServer{
				"linear-server": {ID: "linear-server", ToolNamespace: "linear"},
			},
		)

		// A github tool must NOT be routed to the linear server just because
		// the namespace could not be resolved. The route must error.
		result, err := engine.Route(t.Context(), RouteContext{
			WorkspaceID: "ws1",
			ToolName:    "github__get_label",
		})
		if err == nil {
			t.Fatalf("expected error on transient namespace-resolution failure, got result %+v", result)
		}
		if !errors.Is(err, transient) {
			t.Fatalf("expected wrapped transient error, got %v", err)
		}
		if result != nil {
			t.Fatalf("expected nil result on fail-closed, got %+v", result)
		}
	})

	t.Run("transient failure is not cached, retry resolves once store recovers", func(t *testing.T) {
		getErr := map[string]error{"linear-server": transient}
		ms := &mockRouteStore{
			rules: map[string][]store.RouteRule{
				"ws1": {
					{
						ID: "allow-linear", WorkspaceID: "ws1",
						Priority: 100, PathGlob: "**",
						DownstreamServerID: "linear-server",
						Policy:             "allow", ToolMatch: json.RawMessage(`["*"]`),
					},
				},
			},
			downstreams: map[string]*store.DownstreamServer{
				"linear-server": {ID: "linear-server", ToolNamespace: "linear"},
			},
			getDownstreamErr: getErr,
		}
		engine := NewEngine(ms)

		// First attempt fails (transient).
		if _, err := engine.Route(t.Context(), RouteContext{WorkspaceID: "ws1", ToolName: "linear__search"}); err == nil {
			t.Fatal("expected first attempt to fail closed")
		}

		// Store recovers. Because the failure was NOT cached, the next attempt
		// re-resolves and now matches the linear tool to the linear server.
		delete(ms.getDownstreamErr, "linear-server")
		result, err := engine.Route(t.Context(), RouteContext{WorkspaceID: "ws1", ToolName: "linear__search"})
		if err != nil {
			t.Fatalf("expected success after store recovery, got %v", err)
		}
		if result.DownstreamServerID != "linear-server" {
			t.Errorf("ds = %q, want %q", result.DownstreamServerID, "linear-server")
		}
		// And a foreign-namespace tool is still guarded (no namespaced rule matches).
		if _, err := engine.Route(t.Context(), RouteContext{WorkspaceID: "ws1", ToolName: "github__get_label"}); !errors.Is(err, ErrNoRoute) {
			t.Errorf("foreign tool err = %v, want ErrNoRoute", err)
		}
	})

	t.Run("deleted server (ErrNotFound) is legit empty namespace", func(t *testing.T) {
		// No downstreams map entry and no injected error => GetDownstreamServer
		// returns store.ErrNotFound. This is a legitimate empty namespace: the
		// rule keeps namespace="" and the guard is intentionally not applied,
		// so the wildcard rule matches as a plain allow.
		engine := newEngine(nil, nil)

		result, err := engine.Route(t.Context(), RouteContext{
			WorkspaceID: "ws1",
			ToolName:    "anything__goes",
		})
		if err != nil {
			t.Fatalf("expected ErrNotFound to be treated as legit empty namespace, got %v", err)
		}
		if result.DownstreamServerID != "linear-server" {
			t.Errorf("ds = %q, want %q", result.DownstreamServerID, "linear-server")
		}
	})
}

func TestRoute_NamespaceAware(t *testing.T) {
	ms := &mockRouteStore{
		rules: map[string][]store.RouteRule{
			"ws1": {
				{
					ID: "allow-linear", WorkspaceID: "ws1",
					Priority: 100, PathGlob: "**",
					DownstreamServerID: "linear-server",
					Policy:             "allow", ToolMatch: json.RawMessage(`["*"]`),
				},
				{
					ID: "allow-github", WorkspaceID: "ws1",
					Priority: 100, PathGlob: "**",
					DownstreamServerID: "github-server",
					Policy:             "allow", ToolMatch: json.RawMessage(`["*"]`),
				},
				{
					ID: "global-deny", WorkspaceID: "ws1",
					Priority: 0, PathGlob: "**",
					Policy: "deny", ToolMatch: json.RawMessage(`["*"]`),
				},
			},
		},
		downstreams: map[string]*store.DownstreamServer{
			"linear-server": {ID: "linear-server", ToolNamespace: "linear"},
			"github-server": {ID: "github-server", ToolNamespace: "github"},
		},
	}
	engine := NewEngine(ms)

	tests := []struct {
		name    string
		tool    string
		wantDS  string
		wantErr error
	}{
		{"github tool routes to github", "github__get_label", "github-server", nil},
		{"linear tool routes to linear", "linear__search", "linear-server", nil},
		{"unknown tool hits deny", "slack__post", "", ErrDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Route(t.Context(), RouteContext{
				WorkspaceID: "ws1",
				ToolName:    tt.tool,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if result.DownstreamServerID != tt.wantDS {
				t.Errorf("ds = %q, want %q", result.DownstreamServerID, tt.wantDS)
			}
		})
	}
}
