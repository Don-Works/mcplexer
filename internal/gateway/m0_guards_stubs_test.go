package gateway

import (
	"context"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// M0 (Guards) added four new narrow stores to the aggregate `store.Store`
// interface. These stubs satisfy them so the existing handler-test mock
// keeps implementing `store.Store`. No behaviour — gateway tests don't
// exercise these surfaces yet.

func (m *mockStore) CreateScheduledJob(context.Context, *store.ScheduledJob) error { return nil }
func (m *mockStore) GetScheduledJob(context.Context, string) (*store.ScheduledJob, error) {
	return nil, nil
}
func (m *mockStore) ListScheduledJobs(context.Context) ([]store.ScheduledJob, error) {
	return nil, nil
}
func (m *mockStore) UpdateScheduledJob(context.Context, *store.ScheduledJob) error { return nil }
func (m *mockStore) DeleteScheduledJob(context.Context, string) error              { return nil }
func (m *mockStore) DueScheduledJobs(context.Context, time.Time, int) ([]store.ScheduledJob, error) {
	return nil, nil
}

func (m *mockStore) GetSanitizerMeta(context.Context, string, string) (*store.SanitizerMeta, error) {
	return nil, nil
}
func (m *mockStore) UpsertSanitizerMeta(context.Context, *store.SanitizerMeta) error { return nil }
func (m *mockStore) ListSanitizerMeta(context.Context) ([]store.SanitizerMeta, error) {
	return nil, nil
}
func (m *mockStore) IncrementSanitizerCounter(context.Context, string, string, string) error {
	return nil
}

func (m *mockStore) UpsertInstalledClient(context.Context, *store.InstalledClient) error {
	return nil
}
func (m *mockStore) GetInstalledClient(context.Context, string) (*store.InstalledClient, error) {
	return nil, nil
}
func (m *mockStore) ListInstalledClients(context.Context) ([]store.InstalledClient, error) {
	return nil, nil
}
func (m *mockStore) CreateInstallReceipt(context.Context, *store.InstallReceipt) error {
	return nil
}
func (m *mockStore) ListInstallReceipts(context.Context, string, bool) ([]store.InstallReceipt, error) {
	return nil, nil
}
func (m *mockStore) MarkReceiptReversed(context.Context, string, string) error { return nil }

func (m *mockStore) CreateApprovalRule(context.Context, *store.ApprovalRule) error { return nil }
func (m *mockStore) GetApprovalRule(context.Context, string) (*store.ApprovalRule, error) {
	return nil, nil
}
func (m *mockStore) ListApprovalRules(context.Context, string) ([]store.ApprovalRule, error) {
	return nil, nil
}
func (m *mockStore) UpdateApprovalRule(context.Context, *store.ApprovalRule) error { return nil }
func (m *mockStore) DeleteApprovalRule(context.Context, string) error              { return nil }
func (m *mockStore) IncrementHitCount(context.Context, string, time.Time) error    { return nil }

func (m *mockStore) IngestDataCollection(context.Context, *store.DataCollection, []store.DataItem) error {
	return nil
}
func (m *mockStore) GetDataCollection(context.Context, string, string) (*store.DataCollection, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListDataCollections(context.Context, store.DataCollectionFilter) ([]store.DataCollection, error) {
	return nil, nil
}
func (m *mockStore) DropDataCollection(context.Context, string, string) error { return nil }
func (m *mockStore) QueryDataCollection(context.Context, store.DataQuery) ([]map[string]any, error) {
	return nil, nil
}
func (m *mockStore) SearchDataCollection(context.Context, store.DataSearch) ([]store.DataHit, error) {
	return nil, nil
}
func (m *mockStore) PruneExpiredDataCollections(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (m *mockStore) SetCodeState(context.Context, *store.CodeStateEntry) error { return nil }
func (m *mockStore) GetCodeState(context.Context, string, string) (*store.CodeStateEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListCodeState(context.Context, store.CodeStateFilter) ([]store.CodeStateEntry, error) {
	return nil, nil
}
func (m *mockStore) DeleteCodeState(context.Context, string, string) error         { return nil }
func (m *mockStore) PruneExpiredCodeState(context.Context, time.Time) (int, error) { return 0, nil }

// M0.1 (Workers) — stubs for the WorkerStore methods. Gateway handler
// tests don't exercise this surface yet; the runner / admin tools (M0.3,
// M0.5) will populate proper test doubles when they land.

func (m *mockStore) ListWorkers(context.Context, string, bool) ([]*store.Worker, error) {
	return nil, nil
}
func (m *mockStore) GetWorker(context.Context, string) (*store.Worker, error) { return nil, nil }
func (m *mockStore) GetWorkerByName(context.Context, string, string) (*store.Worker, error) {
	return nil, nil
}
func (m *mockStore) CreateWorker(context.Context, *store.Worker) error { return nil }
func (m *mockStore) UpdateWorker(context.Context, *store.Worker) error { return nil }
func (m *mockStore) DeleteWorker(context.Context, string) error        { return nil }
func (m *mockStore) ListWorkerWorkspaceAccess(context.Context, string) ([]store.WorkerWorkspaceAccess, error) {
	return nil, nil
}
func (m *mockStore) ReplaceWorkerWorkspaceAccess(context.Context, string, []store.WorkerWorkspaceAccess) error {
	return nil
}

func (m *mockStore) CreateWorkerRun(context.Context, *store.WorkerRun) error { return nil }
func (m *mockStore) ReapOrphanedRunningRuns(context.Context, time.Time, time.Time) (int, error) {
	return 0, nil
}
func (m *mockStore) ListOrphanedDelegationRuns(context.Context) ([]*store.WorkerRun, error) {
	return nil, nil
}
func (m *mockStore) UpdateWorkerRunStatus(context.Context, string, store.WorkerRunFinalize) error {
	return nil
}
func (m *mockStore) GetWorkerRun(context.Context, string) (*store.WorkerRun, error) {
	return nil, nil
}
func (m *mockStore) ListWorkerRuns(context.Context, string, int) ([]*store.WorkerRun, error) {
	return nil, nil
}
func (m *mockStore) ListRecentWorkerRunsByWorkerIDs(context.Context, []string, int) (map[string][]*store.WorkerRun, error) {
	return map[string][]*store.WorkerRun{}, nil
}
func (m *mockStore) CountRunningWorkerRuns(context.Context, string) (int, error) { return 0, nil }
func (m *mockStore) CancelRun(context.Context, string, time.Time, string) error  { return nil }
func (m *mockStore) ReconcileOrphanedRuns(context.Context, time.Time, time.Time, string) (int64, error) {
	return 0, nil
}

// M1 (Workers safety) — auto-pause + approval surface stubs.
func (m *mockStore) SumCostThisMonth(context.Context, string, time.Time) (float64, error) {
	return 0, nil
}
func (m *mockStore) LastFailureStatuses(context.Context, string, int) ([]string, error) {
	return nil, nil
}
func (m *mockStore) CreateWorkerApproval(context.Context, *store.WorkerApproval) error {
	return nil
}
func (m *mockStore) GetWorkerApproval(context.Context, string) (*store.WorkerApproval, error) {
	return nil, nil
}
func (m *mockStore) ListWorkerApprovals(context.Context, string, int) ([]*store.WorkerApproval, error) {
	return nil, nil
}
func (m *mockStore) DecideWorkerApproval(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (m *mockStore) CountPendingWorkerApprovals(context.Context) (int, error) { return 0, nil }
func (m *mockStore) WorkerCostAggregate(context.Context, string, int, time.Time) ([]store.WorkerCostAggregate, error) {
	return nil, nil
}
func (m *mockStore) RecordCompression(context.Context, string, time.Time, []store.CompressionObservation) error {
	return nil
}
func (m *mockStore) CompressionAggregate(context.Context, string, int, time.Time) (store.CompressionAggregate, error) {
	return store.CompressionAggregate{}, nil
}

// Retention prune stubs — gateway tests don't exercise them.
func (m *mockStore) PruneAuditRecords(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (m *mockStore) PruneWorkerRuns(context.Context, int, time.Time) (int64, error) {
	return 0, nil
}
func (m *mockStore) CountChildCLIToolCalls(context.Context, string, time.Time, time.Time, []string) (int, error) {
	return 0, nil
}

// Audit overhaul — search / alerts / saved-search surface. Gateway tests
// don't exercise these.
func (m *mockStore) SearchAuditRecords(context.Context, store.AuditFilter, int) ([]store.AuditRecord, string, error) {
	return nil, "fts", nil
}
func (m *mockStore) AuditAnomalies(context.Context, string, time.Duration) ([]store.AuditAlert, error) {
	return nil, nil
}
func (m *mockStore) AuditSecurityEvents(context.Context, string, time.Duration) ([]store.AuditAlert, error) {
	return nil, nil
}
func (m *mockStore) CountAuditMatching(context.Context, store.AuditFilter) (int, error) {
	return 0, nil
}
func (m *mockStore) ListSavedSearches(context.Context) ([]store.SavedSearch, error) {
	return nil, nil
}
func (m *mockStore) GetSavedSearch(context.Context, string) (*store.SavedSearch, error) {
	return nil, nil
}
func (m *mockStore) CreateSavedSearch(context.Context, *store.SavedSearch) error { return nil }
func (m *mockStore) UpdateSavedSearch(context.Context, *store.SavedSearch) error { return nil }
func (m *mockStore) DeleteSavedSearch(context.Context, string) error             { return nil }
func (m *mockStore) EvaluateSavedSearches(context.Context, time.Time) ([]store.FiredSavedSearch, error) {
	return nil, nil
}

// M4 (Workers mesh triggers) — stubs for the WorkerMeshTrigger surface
// + peer-scope check. Gateway tests don't exercise these.
func (m *mockStore) ListWorkerMeshTriggers(context.Context, string) ([]*store.WorkerMeshTrigger, error) {
	return nil, nil
}
func (m *mockStore) ListAllEnabledMeshTriggers(context.Context) ([]*store.WorkerMeshTrigger, error) {
	return nil, nil
}
func (m *mockStore) GetWorkerMeshTrigger(context.Context, string) (*store.WorkerMeshTrigger, error) {
	return nil, nil
}
func (m *mockStore) CreateWorkerMeshTrigger(context.Context, *store.WorkerMeshTrigger) error {
	return nil
}
func (m *mockStore) UpdateWorkerMeshTrigger(context.Context, *store.WorkerMeshTrigger) error {
	return nil
}
func (m *mockStore) DeleteWorkerMeshTrigger(context.Context, string) error { return nil }
func (m *mockStore) HasPeerScope(context.Context, string, string) (bool, error) {
	return false, nil
}

// Tasks (migration 061) — stubs for TaskStore. Gateway tests don't exercise.
func (m *mockStore) CreateTask(context.Context, *store.Task) error        { return nil }
func (m *mockStore) GetTask(context.Context, string) (*store.Task, error) { return nil, nil }
func (m *mockStore) UpdateTask(context.Context, *store.Task) error        { return nil }
func (m *mockStore) ClaimTask(context.Context, *store.Task, string) error { return nil }
func (m *mockStore) SoftDeleteTask(context.Context, string) error         { return nil }
func (m *mockStore) ListTasks(context.Context, store.TaskFilter) ([]store.Task, error) {
	return nil, nil
}
func (m *mockStore) ListTaskIDsByPrefix(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}
func (m *mockStore) SearchTasks(context.Context, store.TaskFilter, string) ([]store.Task, error) {
	return nil, nil
}
func (m *mockStore) CountTasksByStatus(context.Context, string) (map[string]int, error) {
	return nil, nil
}
func (m *mockStore) ListTasksSinceHLC(context.Context, string, string, int) ([]store.Task, error) {
	return nil, nil
}
func (m *mockStore) MaxHLCForWorkspace(context.Context, string) (string, error) {
	return "", nil
}
func (m *mockStore) AppendTaskNote(context.Context, *store.TaskNote) error { return nil }
func (m *mockStore) ListTaskNotes(context.Context, string, int) ([]store.TaskNote, error) {
	return nil, nil
}
func (m *mockStore) InsertTaskAttachment(context.Context, *store.TaskAttachment) error { return nil }
func (m *mockStore) GetTaskAttachment(context.Context, string) (*store.TaskAttachment, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListTaskAttachments(context.Context, string) ([]store.TaskAttachment, error) {
	return nil, nil
}
func (m *mockStore) SoftDeleteTaskAttachment(context.Context, string) error { return nil }
func (m *mockStore) UpsertTaskStatusVocab(context.Context, *store.TaskStatusVocab) error {
	return nil
}
func (m *mockStore) ListTaskStatusVocab(context.Context, string) ([]store.TaskStatusVocab, error) {
	return nil, nil
}
func (m *mockStore) DeleteTaskStatusVocab(context.Context, string, string) error { return nil }
func (m *mockStore) IsTerminalStatus(context.Context, string, string) (bool, error) {
	return false, nil
}
func (m *mockStore) UpsertWorkspacePeerBinding(context.Context, *store.WorkspacePeerBinding) error {
	return nil
}
func (m *mockStore) GetWorkspacePeerBinding(context.Context, string, string) (*store.WorkspacePeerBinding, error) {
	return nil, nil
}
func (m *mockStore) ListLocalWorkspaceIDsForPeer(context.Context, string) ([]string, error) {
	return nil, nil
}
func (m *mockStore) ListWorkspacePeerBindingsForPeer(context.Context, string) ([]store.WorkspacePeerBinding, error) {
	return nil, nil
}
func (m *mockStore) SetWorkspaceLink(context.Context, *store.WorkspacePeerBinding, string) error {
	return nil
}
func (m *mockStore) ClearWorkspaceLink(context.Context, string, string) error { return nil }
func (m *mockStore) ListWorkspaceLinks(context.Context) ([]store.WorkspacePeerBinding, error) {
	return nil, nil
}
func (m *mockStore) ListLinkedPeersForWorkspace(context.Context, string) ([]string, error) {
	return nil, nil
}
func (m *mockStore) FindLocalTaskForRemoteOffer(context.Context, string, string) (string, error) {
	return "", nil
}
func (m *mockStore) ListResolvedApprovals(context.Context, int) ([]store.ToolApproval, error) {
	return nil, nil
}
func (m *mockStore) ListActiveMeshAgentsInWorkspaces(context.Context, []string, time.Time) ([]store.MeshAgent, error) {
	return nil, nil
}
func (m *mockStore) CreateTaskOffer(context.Context, *store.TaskOffer) error { return nil }
func (m *mockStore) GetTaskOffer(context.Context, string) (*store.TaskOffer, error) {
	return nil, nil
}
func (m *mockStore) ListTaskOffers(context.Context, store.TaskOfferFilter) ([]store.TaskOffer, error) {
	return nil, nil
}
func (m *mockStore) UpdateTaskOfferState(context.Context, string, string, *time.Time, *time.Time, string, string, string) error {
	return nil
}
func (m *mockStore) UpsertTaskAssignThrottle(context.Context, *store.TaskAssignThrottle) error {
	return nil
}
func (m *mockStore) GetTaskAssignThrottle(context.Context, string, string) (*store.TaskAssignThrottle, error) {
	return nil, nil
}
func (m *mockStore) SelectDistinctTaskStatuses(context.Context, string) (map[string]int, error) {
	return nil, nil
}
func (m *mockStore) RebindPeerInTasks(context.Context, string, string) (map[string]int, error) {
	return nil, nil
}
func (m *mockStore) HeartbeatTask(context.Context, string, string, time.Duration) (bool, error) {
	return false, nil
}
func (m *mockStore) ClearExpiredTaskLeases(context.Context, time.Time) ([]string, error) {
	return nil, nil
}
func (m *mockStore) ClearSessionTaskLeases(context.Context, string) ([]string, error) {
	return nil, nil
}
func (m *mockStore) ListMilestonesWithBurndown(context.Context, string) ([]store.MilestoneBurndown, error) {
	return nil, nil
}
func (m *mockStore) UpdateSecretTransferRecipient(context.Context, string, string) error {
	return nil
}
func (m *mockStore) InsertSecretOffer(context.Context, *store.SecretOffer) error { return nil }
func (m *mockStore) GetSecretOffer(context.Context, string) (*store.SecretOffer, error) {
	return nil, nil
}
func (m *mockStore) ListPendingSecretOffers(context.Context, string) ([]*store.SecretOffer, error) {
	return nil, nil
}
func (m *mockStore) DecideSecretOffer(context.Context, string, string, time.Time, string) error {
	return nil
}
func (m *mockStore) InsertSkillOffer(context.Context, *store.SkillOffer) error { return nil }
func (m *mockStore) GetSkillOffer(context.Context, string) (*store.SkillOffer, error) {
	return nil, nil
}
func (m *mockStore) ListPendingSkillOffers(context.Context, string) ([]*store.SkillOffer, error) {
	return nil, nil
}
func (m *mockStore) DecideSkillOffer(context.Context, string, string, time.Time, int) error {
	return nil
}
func (m *mockStore) ExpireOldSkillOffers(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (m *mockStore) ExpireOldSecretOffers(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// BrainIndexStore (migration 090) — index_files + brain_errors. No
// behaviour; the gateway tests don't exercise the brain index.
func (m *mockStore) UpsertIndexFile(context.Context, *store.IndexFile) error { return nil }
func (m *mockStore) GetIndexFile(context.Context, string) (*store.IndexFile, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) DeleteIndexFile(context.Context, string) error { return nil }
func (m *mockStore) ListIndexFiles(context.Context, string) ([]store.IndexFile, error) {
	return nil, nil
}
func (m *mockStore) RecordBrainError(context.Context, *store.BrainError) error { return nil }
func (m *mockStore) ClearBrainErrorsForPath(context.Context, string) error     { return nil }
func (m *mockStore) ListBrainErrors(context.Context) ([]store.BrainError, error) {
	return nil, nil
}
func (m *mockStore) SuppressCandidate(context.Context, string, string) error { return nil }
func (m *mockStore) IsCandidateSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}
