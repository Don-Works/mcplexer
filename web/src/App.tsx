import { lazy, Suspense } from 'react'
import { BrowserRouter, Link, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { TooltipProvider } from '@/components/ui/tooltip'
import { Toaster } from '@/components/ui/sonner'
import { useDocumentTitle } from '@/hooks/use-pending-title'
import { useCommandPalette } from '@/components/command-palette/use-command-palette'
import { useSignalStream, useSignalTray } from '@/components/notifications/use-signal'
import { useOsNotifications } from '@/components/notifications/use-os-notifications'
import { useApprovalStream } from '@/hooks/use-approval-stream'
import { SecretPromptModal } from '@/components/SecretPromptModal'
import { AppLayout } from '@/components/layout/AppLayout'
import { BrainGate } from '@/components/BrainGate'
import { serverHomePath } from '@/lib/server-profile'
import { HealthProvider, useHealth } from '@/hooks/use-health'

const DashboardPage = lazy(() => import('@/pages/DashboardPage').then(m => ({ default: m.DashboardPage })))
const CommandPalette = lazy(() => import('@/components/command-palette/CommandPalette').then(m => ({ default: m.CommandPalette })))
const HarnessSetupPage = lazy(() => import('@/pages/HarnessSetupPage').then(m => ({ default: m.HarnessSetupPage })))
const AuditPage = lazy(() => import('@/pages/AuditPage').then(m => ({ default: m.AuditPage })))
const ConfigPage = lazy(() => import('@/pages/config/ConfigPage').then(m => ({ default: m.ConfigPage })))
const LinkedWorkspacesPage = lazy(() => import('@/pages/config/LinkedWorkspacesPage').then(m => ({ default: m.LinkedWorkspacesPage })))
const DryRunPage = lazy(() => import('@/pages/DryRunPage').then(m => ({ default: m.DryRunPage })))
const CreateMCPPage = lazy(() => import('@/pages/CreateMCP').then(m => ({ default: m.CreateMCPPage })))
const ConnectionsPage = lazy(() => import('@/pages/ConnectionsPage').then(m => ({ default: m.ConnectionsPage })))
const ApprovalsPage = lazy(() => import('@/pages/ApprovalsPage').then(m => ({ default: m.ApprovalsPage })))
const MobileAppPage = lazy(() => import('@/pages/MobileAppPage').then(m => ({ default: m.MobileAppPage })))
const SettingsPage = lazy(() => import('@/pages/SettingsPage').then(m => ({ default: m.SettingsPage })))
const CompressionPage = lazy(() => import('@/pages/CompressionPage').then(m => ({ default: m.CompressionPage })))
const MeshPage = lazy(() => import('@/pages/MeshPage').then(m => ({ default: m.MeshPage })))
const ChatView = lazy(() => import('@/pages/chat/ChatView').then(m => ({ default: m.ChatView })))
const PairingPage = lazy(() => import('@/pages/Pairing').then(m => ({ default: m.PairingPage })))
const CollaborationPage = lazy(() => import('@/pages/CollaborationPage').then(m => ({ default: m.CollaborationPage })))
const BackupsPage = lazy(() => import('@/pages/BackupsPage').then(m => ({ default: m.BackupsPage })))
const BrainStatusPage = lazy(() => import('@/pages/BrainStatusPage').then(m => ({ default: m.BrainStatusPage })))
const BrainBrowserPage = lazy(() => import('@/pages/brain/BrainBrowserPage').then(m => ({ default: m.BrainBrowserPage })))
const SkillRegistryPage = lazy(() => import('@/pages/SkillRegistryPage').then(m => ({ default: m.SkillRegistryPage })))
const SignalsPage = lazy(() => import('@/pages/SignalsPage').then(m => ({ default: m.SignalsPage })))
const GuardsOverviewPage = lazy(() => import('@/pages/guards/GuardsOverviewPage').then(m => ({ default: m.GuardsOverviewPage })))
const ShellGuardPage = lazy(() => import('@/pages/guards/ShellGuardPage').then(m => ({ default: m.ShellGuardPage })))
const SanitizerGuardPage = lazy(() => import('@/pages/guards/SanitizerGuardPage').then(m => ({ default: m.SanitizerGuardPage })))
const ScheduleGuardPage = lazy(() => import('@/pages/guards/ScheduleGuardPage').then(m => ({ default: m.ScheduleGuardPage })))
const SandboxGuardPage = lazy(() => import('@/pages/guards/SandboxGuardPage').then(m => ({ default: m.SandboxGuardPage })))
const WorkersListPage = lazy(() => import('@/pages/workers/WorkersListPage').then(m => ({ default: m.WorkersListPage })))
const DelegationsPage = lazy(() => import('@/pages/workers/DelegationsPage').then(m => ({ default: m.DelegationsPage })))
const ModelRanksPage = lazy(() => import('@/pages/workers/ModelRanksPage').then(m => ({ default: m.ModelRanksPage })))
const WorkerDetailPage = lazy(() => import('@/pages/workers/WorkerDetailPage').then(m => ({ default: m.WorkerDetailPage })))
const WorkerEditorPage = lazy(() => import('@/pages/workers/WorkerEditorPage').then(m => ({ default: m.WorkerEditorPage })))
const WorkerApprovalsPage = lazy(() => import('@/pages/workers/WorkerApprovalsPage').then(m => ({ default: m.WorkerApprovalsPage })))
const WorkerCostDashboardPage = lazy(() => import('@/pages/workers/WorkerCostDashboardPage').then(m => ({ default: m.WorkerCostDashboardPage })))
const ModelLeaderboardPage = lazy(() => import('@/pages/workers/ModelLeaderboardPage').then(m => ({ default: m.ModelLeaderboardPage })))
const MonitoringPage = lazy(() => import('@/pages/monitoring/MonitoringPage').then(m => ({ default: m.MonitoringPage })))
const UsageDashboardPage = lazy(() => import('@/pages/UsageDashboardPage').then(m => ({ default: m.UsageDashboardPage })))
const ModelProvidersPage = lazy(() => import('@/pages/ModelProvidersPage').then(m => ({ default: m.ModelProvidersPage })))
const MemoryLandingPage = lazy(() => import('@/pages/memory/MemoryLandingPage').then(m => ({ default: m.MemoryLandingPage })))
const MemoryListPage = lazy(() => import('@/pages/memory/MemoryListPage').then(m => ({ default: m.MemoryListPage })))
const MemoryOffersPage = lazy(() => import('@/pages/memory/MemoryOffersPage').then(m => ({ default: m.MemoryOffersPage })))
const MemoryConsolidationPage = lazy(() => import('@/pages/memory/MemoryConsolidationPage').then(m => ({ default: m.MemoryConsolidationPage })))
const MemoryEmbeddingsPage = lazy(() => import('@/pages/memory/MemoryEmbeddingsPage').then(m => ({ default: m.MemoryEmbeddingsPage })))
const MemoryConflictsPage = lazy(() => import('@/pages/memory/MemoryConflictsPage').then(m => ({ default: m.MemoryConflictsPage })))
const MemoryActivityPage = lazy(() => import('@/pages/memory/MemoryActivityPage').then(m => ({ default: m.MemoryActivityPage })))
const MemoryAboutPage = lazy(() => import('@/pages/memory/MemoryAboutPage').then(m => ({ default: m.MemoryAboutPage })))
const TasksListPage = lazy(() => import('@/pages/tasks/TasksListPage').then(m => ({ default: m.TasksListPage })))
const TaskDetailPage = lazy(() => import('@/pages/tasks/TaskDetailPage').then(m => ({ default: m.TaskDetailPage })))
const TaskOffersPage = lazy(() => import('@/pages/tasks/TaskOffersPage').then(m => ({ default: m.TaskOffersPage })))
const TasksLandingPage = lazy(() => import('@/pages/tasks/TasksLandingPage').then(m => ({ default: m.TasksLandingPage })))

function PageLoader() {
  return (
    <div className="flex items-center justify-center py-32">
      <div className="animate-pulse text-muted-foreground text-sm">Loading...</div>
    </div>
  )
}

function NotFound() {
  return (
    <div className="flex flex-col items-center justify-center py-32 gap-4">
      <h1 className="text-4xl font-bold text-foreground">404</h1>
      <p className="text-muted-foreground">Page not found</p>
      <Link to="/" className="text-sm text-primary hover:underline">Back to home</Link>
    </div>
  )
}

function NotifyBridge() {
  // Signal — wires SSE + REST backfill + unread-count poll into the
  // global Signal store. Replaces the old toast-pushing useNotifyStream.
  useSignalStream()
  // cmd+J / ctrl+J global shortcut to toggle the Signal tray.
  useSignalTray()
  // OS-native browser notifications (replaces the old Electron shell).
  // This hook is permission-management only; the actual notification
	// firing lives inside useSignalStream so we don't
  // double-subscribe to the same SSE endpoints (Chrome's 6-per-origin
  // HTTP/1.1 cap was getting blown otherwise).
  useOsNotifications()
	// Approval stream — singleton, ref-counted. Keep it always-on at the
	// root so the pending queue stays current on every page.
  // DashboardPage + ApprovalsPage subscribe to the same module-level
  // EventSource via this hook; refcount=1+ keeps it open globally.
  useApprovalStream()
  useDocumentTitle()
  return null
}

function CommandBridge() {
  const { open, setOpen } = useCommandPalette()
  if (!open) return null
  return (
    <Suspense fallback={null}>
      <CommandPalette open={open} onOpenChange={setOpen} />
    </Suspense>
  )
}

function ProfileHome() {
  const { data, loading } = useHealth()
  const home = serverHomePath(data?.system)
  if (home !== '/') return <Navigate to={home} replace />
  if (loading && !data) return <PageLoader />
  return <DashboardPage />
}

function RedirectWithSearch({ to }: { to: string }) {
  const location = useLocation()
  const [path, rawTargetSearch = ''] = to.split('?')
  const merged = new URLSearchParams(rawTargetSearch)
  const current = new URLSearchParams(location.search)
  for (const [key, value] of current.entries()) {
    if (!merged.has(key)) merged.append(key, value)
  }
  const search = merged.toString()
  return <Navigate to={`${path}${search ? `?${search}` : ''}`} replace />
}

function App() {
  return (
    <TooltipProvider>
    <HealthProvider>
    <BrowserRouter>
      <NotifyBridge />
      <CommandBridge />
      <AppLayout>
        <Suspense fallback={<PageLoader />}>
          <Routes>
            <Route path="/" element={<ProfileHome />} />
            <Route path="/harness-setup" element={<HarnessSetupPage />} />
            <Route path="/setup" element={<RedirectWithSearch to="/workspaces?view=add-server" />} />
            <Route path="/connections" element={<RedirectWithSearch to="/workspaces" />} />
            <Route path="/audit" element={<AuditPage />} />
            <Route path="/app" element={<MobileAppPage />} />
            <Route path="/approvals" element={<ApprovalsPage />} />
            <Route path="/mesh" element={<MeshPage />} />
            <Route path="/chat" element={<ChatView />} />
            <Route path="/skills" element={<SkillRegistryPage />} />
            <Route path="/signals" element={<SignalsPage />} />

          {/* Tasks — per-workspace operational primitive. /tasks is the
              calm landing (vitals + activity + workspace breakdown);
              /tasks/all is the deep list with filters + bulk-actions;
              /tasks/offers is the cross-peer offer tray (incoming +
              outgoing + history); /tasks/:id is the detail page with
              autolinked task:<id> refs from mesh / audit / signals /
              worker output. The legacy `?focus=<id>` query on /tasks
              forwards to /tasks/all so TaskRef fallbacks keep
              working. */}
            <Route path="/tasks" element={<TasksLandingPage />} />
            <Route path="/tasks/all" element={<TasksListPage />} />
            <Route path="/tasks/offers" element={<TaskOffersPage />} />
            <Route path="/tasks/:id" element={<TaskDetailPage />} />

          {/* Memory — cross-harness fact + note store. Landing → list,
              offers, consolidation. URL-backed selection (?selected=<id>)
              on the list page opens the detail drawer. */}
            <Route path="/memory" element={<MemoryLandingPage />} />
            <Route path="/memory/all" element={<MemoryListPage />} />
            <Route path="/memory/activity" element={<MemoryActivityPage />} />
            <Route path="/memory/about/:entityKind/:entityId" element={<MemoryAboutPage />} />
            <Route path="/memory/shared" element={<MemoryOffersPage />} />
            <Route path="/memory/consolidation" element={<MemoryConsolidationPage />} />
            <Route path="/memory/embeddings" element={<MemoryEmbeddingsPage />} />
            <Route path="/memory/conflicts" element={<MemoryConflictsPage />} />

          {/* Guards (M1-D) — five enforcement-layer subpages plus an
              overview card grid. /guards is the landing; each detail
              page lives at /guards/{shell,sanitizer,schedule,sandbox}. */}
            <Route path="/guards" element={<GuardsOverviewPage />} />
            <Route path="/guards/shell" element={<ShellGuardPage />} />
            <Route path="/guards/sanitizer" element={<SanitizerGuardPage />} />
            <Route path="/guards/schedule" element={<ScheduleGuardPage />} />
            <Route path="/guards/sandbox" element={<SandboxGuardPage />} />

          {/* Workers (M0.6) — always-on AI agents on a schedule. List,
              detail, and create/edit pages all share /api/v1/workers. */}
            <Route path="/workers" element={<WorkersListPage />} />
            <Route path="/delegations" element={<DelegationsPage />} />
            <Route path="/delegations/models" element={<ModelRanksPage />} />
            <Route path="/monitoring" element={<MonitoringPage />} />
            <Route path="/usage" element={<UsageDashboardPage />} />
            <Route path="/workers/cost" element={<WorkerCostDashboardPage />} />
            <Route path="/workers/model-leaderboard" element={<ModelLeaderboardPage />} />
            <Route path="/workers/new" element={<WorkerEditorPage />} />
            <Route path="/workers/:id" element={<WorkerDetailPage />} />
            <Route path="/workers/:id/edit" element={<WorkerEditorPage />} />
            <Route path="/worker-approvals" element={<WorkerApprovalsPage />} />
            <Route path="/model-providers" element={<ModelProvidersPage />} />

          {/* Workspaces — one canonical console for access, activity,
              metadata, server setup, and the global server library. */}
            <Route path="/workspaces" element={<ConnectionsPage />} />
            <Route path="/workspaces/routes" element={<RedirectWithSearch to="/workspaces?view=access&advanced=1" />} />
            <Route path="/workspaces/manage" element={<RedirectWithSearch to="/workspaces?view=settings" />} />
            <Route path="/workspace-links" element={<LinkedWorkspacesPage />} />

          {/* Advanced — raw config surfaces: credentials, OAuth providers,
              descriptions. /config also serves this shell for backwards-compat;
              query-param redirects live inside ConfigPage. */}
            <Route path="/config" element={<ConfigPage />} />
            <Route path="/advanced" element={<ConfigPage />} />
            <Route path="/advanced/credentials" element={<ConfigPage />} />
            <Route path="/advanced/oauth-providers" element={<ConfigPage />} />
            <Route path="/advanced/routes" element={<RedirectWithSearch to="/workspaces?view=access&advanced=1" />} />
            <Route path="/advanced/descriptions" element={<ConfigPage />} />

          {/* Legacy per-resource routes — redirect to new canonical URLs. */}
            <Route path="/servers" element={<RedirectWithSearch to="/workspaces?view=servers&server_tab=installed" />} />
            <Route path="/servers/available" element={<RedirectWithSearch to="/workspaces?view=servers&server_tab=available" />} />
            <Route path="/config/downstreams" element={<RedirectWithSearch to="/workspaces?view=servers&server_tab=installed" />} />
            <Route path="/config/routes" element={<RedirectWithSearch to="/workspaces?view=access&advanced=1" />} />
            <Route path="/config/auth-scopes" element={<Navigate to="/advanced/credentials" replace />} />
            <Route path="/config/oauth-providers" element={<Navigate to="/advanced/oauth-providers" replace />} />
            <Route path="/config/workspaces" element={<RedirectWithSearch to="/workspaces?view=settings" />} />
            <Route path="/descriptions" element={<Navigate to="/advanced/descriptions" replace />} />

            <Route path="/pairing" element={<PairingPage />} />
            <Route path="/collaboration" element={<CollaborationPage />} />
            <Route path="/workspaces/access" element={<CollaborationPage />} />
            <Route path="/install" element={<RedirectWithSearch to="/harness-setup" />} />
            <Route path="/create-mcp" element={<CreateMCPPage />} />
            <Route path="/dry-run" element={<DryRunPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="/settings/compression" element={<CompressionPage />} />
            <Route path="/backups" element={<BackupsPage />} />
            <Route path="/brain" element={<BrainStatusPage />} />
            <Route path="/brain/browse" element={<BrainGate><BrainBrowserPage /></BrainGate>} />
            <Route path="/brain/browse/:ws/:kind/:id" element={<BrainGate><BrainBrowserPage /></BrainGate>} />
            <Route path="*" element={<NotFound />} />
          </Routes>
        </Suspense>
      </AppLayout>
    </BrowserRouter>
    <SecretPromptModal />
    <Toaster />
    </HealthProvider>
    </TooltipProvider>
  )
}

export default App
