package routing

import (
	"context"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// M0 (Guards) added four new narrow stores to the aggregate `store.Store`
// interface. These stubs satisfy them so the existing mock keeps
// implementing `store.Store`. No behaviour — the routing tests don't
// exercise these surfaces.

func (m *mockRouteStore) CreateScheduledJob(context.Context, *store.ScheduledJob) error {
	return nil
}
func (m *mockRouteStore) GetScheduledJob(context.Context, string) (*store.ScheduledJob, error) {
	return nil, nil
}
func (m *mockRouteStore) ListScheduledJobs(context.Context) ([]store.ScheduledJob, error) {
	return nil, nil
}
func (m *mockRouteStore) UpdateScheduledJob(context.Context, *store.ScheduledJob) error {
	return nil
}
func (m *mockRouteStore) DeleteScheduledJob(context.Context, string) error { return nil }
func (m *mockRouteStore) DueScheduledJobs(context.Context, time.Time, int) ([]store.ScheduledJob, error) {
	return nil, nil
}

func (m *mockRouteStore) GetSanitizerMeta(context.Context, string, string) (*store.SanitizerMeta, error) {
	return nil, nil
}
func (m *mockRouteStore) UpsertSanitizerMeta(context.Context, *store.SanitizerMeta) error {
	return nil
}
func (m *mockRouteStore) ListSanitizerMeta(context.Context) ([]store.SanitizerMeta, error) {
	return nil, nil
}
func (m *mockRouteStore) IncrementSanitizerCounter(context.Context, string, string, string) error {
	return nil
}

func (m *mockRouteStore) UpsertInstalledClient(context.Context, *store.InstalledClient) error {
	return nil
}
func (m *mockRouteStore) GetInstalledClient(context.Context, string) (*store.InstalledClient, error) {
	return nil, nil
}
func (m *mockRouteStore) ListInstalledClients(context.Context) ([]store.InstalledClient, error) {
	return nil, nil
}
func (m *mockRouteStore) CreateInstallReceipt(context.Context, *store.InstallReceipt) error {
	return nil
}
func (m *mockRouteStore) ListInstallReceipts(context.Context, string, bool) ([]store.InstallReceipt, error) {
	return nil, nil
}
func (m *mockRouteStore) MarkReceiptReversed(context.Context, string, string) error { return nil }

func (m *mockRouteStore) CreateApprovalRule(context.Context, *store.ApprovalRule) error {
	return nil
}
func (m *mockRouteStore) GetApprovalRule(context.Context, string) (*store.ApprovalRule, error) {
	return nil, nil
}
func (m *mockRouteStore) ListApprovalRules(context.Context, string) ([]store.ApprovalRule, error) {
	return nil, nil
}
func (m *mockRouteStore) UpdateApprovalRule(context.Context, *store.ApprovalRule) error {
	return nil
}
func (m *mockRouteStore) DeleteApprovalRule(context.Context, string) error           { return nil }
func (m *mockRouteStore) IncrementHitCount(context.Context, string, time.Time) error { return nil }

func (m *mockRouteStore) IngestDataCollection(context.Context, *store.DataCollection, []store.DataItem) error {
	return nil
}
func (m *mockRouteStore) GetDataCollection(context.Context, string, string) (*store.DataCollection, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListDataCollections(context.Context, store.DataCollectionFilter) ([]store.DataCollection, error) {
	return nil, nil
}
func (m *mockRouteStore) DropDataCollection(context.Context, string, string) error { return nil }
func (m *mockRouteStore) QueryDataCollection(context.Context, store.DataQuery) ([]map[string]any, error) {
	return nil, nil
}
func (m *mockRouteStore) SearchDataCollection(context.Context, store.DataSearch) ([]store.DataHit, error) {
	return nil, nil
}
func (m *mockRouteStore) PruneExpiredDataCollections(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (m *mockRouteStore) SetCodeState(context.Context, *store.CodeStateEntry) error { return nil }
func (m *mockRouteStore) GetCodeState(context.Context, string, string) (*store.CodeStateEntry, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListCodeState(context.Context, store.CodeStateFilter) ([]store.CodeStateEntry, error) {
	return nil, nil
}
func (m *mockRouteStore) DeleteCodeState(context.Context, string, string) error { return nil }
func (m *mockRouteStore) PruneExpiredCodeState(context.Context, time.Time) (int, error) {
	return 0, nil
}

// M0.1 (Workers) — stubs for the WorkerStore methods. Routing tests
// don't exercise this surface yet.

func (m *mockRouteStore) ListWorkers(context.Context, string, bool) ([]*store.Worker, error) {
	return nil, nil
}
func (m *mockRouteStore) GetWorker(context.Context, string) (*store.Worker, error) { return nil, nil }
func (m *mockRouteStore) GetWorkerByName(context.Context, string, string) (*store.Worker, error) {
	return nil, nil
}
func (m *mockRouteStore) CreateWorker(context.Context, *store.Worker) error { return nil }
func (m *mockRouteStore) UpdateWorker(context.Context, *store.Worker) error { return nil }
func (m *mockRouteStore) ListWorkerWorkspaceAccess(context.Context, string) ([]store.WorkerWorkspaceAccess, error) {
	return nil, nil
}
func (m *mockRouteStore) ReplaceWorkerWorkspaceAccess(context.Context, string, []store.WorkerWorkspaceAccess) error {
	return nil
}
func (m *mockRouteStore) DeleteWorker(context.Context, string) error { return nil }

func (m *mockRouteStore) CreateWorkerRun(context.Context, *store.WorkerRun) error { return nil }
func (m *mockRouteStore) ReapOrphanedRunningRuns(context.Context, time.Time, time.Time) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) UpdateWorkerRunStatus(context.Context, string, store.WorkerRunFinalize) error {
	return nil
}
func (m *mockRouteStore) GetWorkerRun(context.Context, string) (*store.WorkerRun, error) {
	return nil, nil
}
func (m *mockRouteStore) ListWorkerRuns(context.Context, string, int) ([]*store.WorkerRun, error) {
	return nil, nil
}
func (m *mockRouteStore) ListRecentWorkerRunsByWorkerIDs(context.Context, []string, int) (map[string][]*store.WorkerRun, error) {
	return map[string][]*store.WorkerRun{}, nil
}
func (m *mockRouteStore) CountRunningWorkerRuns(context.Context, string) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) CancelRun(context.Context, string, time.Time, string) error { return nil }
func (m *mockRouteStore) ReconcileOrphanedRuns(context.Context, time.Time, time.Time, string) (int64, error) {
	return 0, nil
}

// M1 (Workers safety) — auto-pause + approval surface stubs.
func (m *mockRouteStore) SumCostThisMonth(context.Context, string, time.Time) (float64, error) {
	return 0, nil
}
func (m *mockRouteStore) LastFailureStatuses(context.Context, string, int) ([]string, error) {
	return nil, nil
}
func (m *mockRouteStore) CreateWorkerApproval(context.Context, *store.WorkerApproval) error {
	return nil
}
func (m *mockRouteStore) GetWorkerApproval(context.Context, string) (*store.WorkerApproval, error) {
	return nil, nil
}
func (m *mockRouteStore) ListWorkerApprovals(context.Context, string, int) ([]*store.WorkerApproval, error) {
	return nil, nil
}
func (m *mockRouteStore) DecideWorkerApproval(context.Context, string, string, string, string, time.Time) error {
	return nil
}
func (m *mockRouteStore) CountPendingWorkerApprovals(context.Context) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) WorkerCostAggregate(context.Context, string, int, time.Time) ([]store.WorkerCostAggregate, error) {
	return nil, nil
}

// Retention prune stubs — routing tests don't exercise them.
func (m *mockRouteStore) PruneAuditRecords(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (m *mockRouteStore) PruneWorkerRuns(context.Context, int, time.Time) (int64, error) {
	return 0, nil
}
func (m *mockRouteStore) CountChildCLIToolCalls(context.Context, string, time.Time, time.Time, []string) (int, error) {
	return 0, nil
}

// Audit overhaul — search / alerts / saved-search surface. Routing tests
// never reach these.
func (m *mockRouteStore) SearchAuditRecords(context.Context, store.AuditFilter, int) ([]store.AuditRecord, string, error) {
	return nil, "fts", nil
}
func (m *mockRouteStore) AuditAnomalies(context.Context, string, time.Duration) ([]store.AuditAlert, error) {
	return nil, nil
}
func (m *mockRouteStore) AuditSecurityEvents(context.Context, string, time.Duration) ([]store.AuditAlert, error) {
	return nil, nil
}
func (m *mockRouteStore) CountAuditMatching(context.Context, store.AuditFilter) (int, error) {
	return 0, nil
}
func (m *mockRouteStore) ListSavedSearches(context.Context) ([]store.SavedSearch, error) {
	return nil, nil
}
func (m *mockRouteStore) GetSavedSearch(context.Context, string) (*store.SavedSearch, error) {
	return nil, nil
}
func (m *mockRouteStore) CreateSavedSearch(context.Context, *store.SavedSearch) error { return nil }
func (m *mockRouteStore) UpdateSavedSearch(context.Context, *store.SavedSearch) error { return nil }
func (m *mockRouteStore) DeleteSavedSearch(context.Context, string) error             { return nil }
func (m *mockRouteStore) EvaluateSavedSearches(context.Context, time.Time) ([]store.FiredSavedSearch, error) {
	return nil, nil
}

// M4 (Workers mesh triggers) — stubs for the WorkerMeshTrigger surface
// + peer-scope check. Routing tests never reach these.
func (m *mockRouteStore) ListWorkerMeshTriggers(context.Context, string) ([]*store.WorkerMeshTrigger, error) {
	return nil, nil
}
func (m *mockRouteStore) ListAllEnabledMeshTriggers(context.Context) ([]*store.WorkerMeshTrigger, error) {
	return nil, nil
}
func (m *mockRouteStore) GetWorkerMeshTrigger(context.Context, string) (*store.WorkerMeshTrigger, error) {
	return nil, nil
}
func (m *mockRouteStore) CreateWorkerMeshTrigger(context.Context, *store.WorkerMeshTrigger) error {
	return nil
}
func (m *mockRouteStore) UpdateWorkerMeshTrigger(context.Context, *store.WorkerMeshTrigger) error {
	return nil
}
func (m *mockRouteStore) DeleteWorkerMeshTrigger(context.Context, string) error { return nil }
func (m *mockRouteStore) HasPeerScope(context.Context, string, string) (bool, error) {
	return false, nil
}

// Tasks (migration 061) — stubs for TaskStore. Routing tests don't exercise.
func (m *mockRouteStore) CreateTask(context.Context, *store.Task) error        { return nil }
func (m *mockRouteStore) GetTask(context.Context, string) (*store.Task, error) { return nil, nil }
func (m *mockRouteStore) UpdateTask(context.Context, *store.Task) error        { return nil }
func (m *mockRouteStore) ClaimTask(context.Context, *store.Task, string) error { return nil }
func (m *mockRouteStore) SoftDeleteTask(context.Context, string) error         { return nil }
func (m *mockRouteStore) ListTasks(context.Context, store.TaskFilter) ([]store.Task, error) {
	return nil, nil
}
func (m *mockRouteStore) ListTaskIDsByPrefix(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}
func (m *mockRouteStore) SearchTasks(context.Context, store.TaskFilter, string) ([]store.Task, error) {
	return nil, nil
}
func (m *mockRouteStore) CountTasksByStatus(context.Context, string) (map[string]int, error) {
	return nil, nil
}
func (m *mockRouteStore) ListTasksSinceHLC(context.Context, string, string, int) ([]store.Task, error) {
	return nil, nil
}
func (m *mockRouteStore) MaxHLCForWorkspace(context.Context, string) (string, error) {
	return "", nil
}
func (m *mockRouteStore) AppendTaskNote(context.Context, *store.TaskNote) error { return nil }
func (m *mockRouteStore) ListTaskNotes(context.Context, string, int) ([]store.TaskNote, error) {
	return nil, nil
}
func (m *mockRouteStore) InsertTaskAttachment(context.Context, *store.TaskAttachment) error {
	return nil
}
func (m *mockRouteStore) GetTaskAttachment(context.Context, string) (*store.TaskAttachment, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) ListTaskAttachments(context.Context, string) ([]store.TaskAttachment, error) {
	return nil, nil
}
func (m *mockRouteStore) SoftDeleteTaskAttachment(context.Context, string) error { return nil }
func (m *mockRouteStore) UpsertTaskStatusVocab(context.Context, *store.TaskStatusVocab) error {
	return nil
}
func (m *mockRouteStore) ListTaskStatusVocab(context.Context, string) ([]store.TaskStatusVocab, error) {
	return nil, nil
}
func (m *mockRouteStore) DeleteTaskStatusVocab(context.Context, string, string) error { return nil }
func (m *mockRouteStore) IsTerminalStatus(context.Context, string, string) (bool, error) {
	return false, nil
}
func (m *mockRouteStore) UpsertWorkspacePeerBinding(context.Context, *store.WorkspacePeerBinding) error {
	return nil
}
func (m *mockRouteStore) GetWorkspacePeerBinding(context.Context, string, string) (*store.WorkspacePeerBinding, error) {
	return nil, nil
}
func (m *mockRouteStore) ListLocalWorkspaceIDsForPeer(context.Context, string) ([]string, error) {
	return nil, nil
}
func (m *mockRouteStore) ListWorkspacePeerBindingsForPeer(context.Context, string) ([]store.WorkspacePeerBinding, error) {
	return nil, nil
}
func (m *mockRouteStore) SetWorkspaceLink(context.Context, *store.WorkspacePeerBinding, string) error {
	return nil
}
func (m *mockRouteStore) ClearWorkspaceLink(context.Context, string, string) error { return nil }
func (m *mockRouteStore) ListWorkspaceLinks(context.Context) ([]store.WorkspacePeerBinding, error) {
	return nil, nil
}
func (m *mockRouteStore) ListLinkedPeersForWorkspace(context.Context, string) ([]string, error) {
	return nil, nil
}
func (m *mockRouteStore) FindLocalTaskForRemoteOffer(context.Context, string, string) (string, error) {
	return "", nil
}
func (m *mockRouteStore) ListResolvedApprovals(context.Context, int) ([]store.ToolApproval, error) {
	return nil, nil
}
func (m *mockRouteStore) ListActiveMeshAgentsInWorkspaces(context.Context, []string, time.Time) ([]store.MeshAgent, error) {
	return nil, nil
}
func (m *mockRouteStore) CreateTaskOffer(context.Context, *store.TaskOffer) error { return nil }
func (m *mockRouteStore) GetTaskOffer(context.Context, string) (*store.TaskOffer, error) {
	return nil, nil
}
func (m *mockRouteStore) ListTaskOffers(context.Context, store.TaskOfferFilter) ([]store.TaskOffer, error) {
	return nil, nil
}
func (m *mockRouteStore) UpdateTaskOfferState(context.Context, string, string, *time.Time, *time.Time, string, string, string) error {
	return nil
}
func (m *mockRouteStore) UpsertTaskAssignThrottle(context.Context, *store.TaskAssignThrottle) error {
	return nil
}
func (m *mockRouteStore) GetTaskAssignThrottle(context.Context, string, string) (*store.TaskAssignThrottle, error) {
	return nil, nil
}
func (m *mockRouteStore) SelectDistinctTaskStatuses(context.Context, string) (map[string]int, error) {
	return nil, nil
}
func (m *mockRouteStore) RebindPeerInTasks(context.Context, string, string) (map[string]int, error) {
	return nil, nil
}
func (m *mockRouteStore) HeartbeatTask(context.Context, string, string, time.Duration) (bool, error) {
	return false, nil
}
func (m *mockRouteStore) ClearExpiredTaskLeases(context.Context, time.Time) ([]string, error) {
	return nil, nil
}
func (m *mockRouteStore) ClearSessionTaskLeases(context.Context, string) ([]string, error) {
	return nil, nil
}
func (m *mockRouteStore) ListMilestonesWithBurndown(context.Context, string) ([]store.MilestoneBurndown, error) {
	return nil, nil
}
func (m *mockRouteStore) UpdateSecretTransferRecipient(context.Context, string, string) error {
	return nil
}
func (m *mockRouteStore) InsertSecretOffer(context.Context, *store.SecretOffer) error { return nil }
func (m *mockRouteStore) GetSecretOffer(context.Context, string) (*store.SecretOffer, error) {
	return nil, nil
}
func (m *mockRouteStore) ListPendingSecretOffers(context.Context, string) ([]*store.SecretOffer, error) {
	return nil, nil
}
func (m *mockRouteStore) DecideSecretOffer(context.Context, string, string, time.Time, string) error {
	return nil
}
func (m *mockRouteStore) InsertSkillOffer(context.Context, *store.SkillOffer) error { return nil }
func (m *mockRouteStore) GetSkillOffer(context.Context, string) (*store.SkillOffer, error) {
	return nil, nil
}
func (m *mockRouteStore) ListPendingSkillOffers(context.Context, string) ([]*store.SkillOffer, error) {
	return nil, nil
}
func (m *mockRouteStore) DecideSkillOffer(context.Context, string, string, time.Time, int) error {
	return nil
}
func (m *mockRouteStore) ExpireOldSkillOffers(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (m *mockRouteStore) ExpireOldSecretOffers(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// BrainIndexStore (migration 090) — index_files + brain_errors. No
// behaviour; the routing tests don't exercise the brain index.
func (m *mockRouteStore) UpsertIndexFile(context.Context, *store.IndexFile) error { return nil }
func (m *mockRouteStore) GetIndexFile(context.Context, string) (*store.IndexFile, error) {
	return nil, store.ErrNotFound
}
func (m *mockRouteStore) DeleteIndexFile(context.Context, string) error { return nil }
func (m *mockRouteStore) ListIndexFiles(context.Context, string) ([]store.IndexFile, error) {
	return nil, nil
}
func (m *mockRouteStore) RecordBrainError(context.Context, *store.BrainError) error { return nil }
func (m *mockRouteStore) ClearBrainErrorsForPath(context.Context, string) error     { return nil }
func (m *mockRouteStore) ListBrainErrors(context.Context) ([]store.BrainError, error) {
	return nil, nil
}
func (m *mockRouteStore) SuppressCandidate(context.Context, string, string) error { return nil }
func (m *mockRouteStore) IsCandidateSuppressed(context.Context, string, string) (bool, error) {
	return false, nil
}
