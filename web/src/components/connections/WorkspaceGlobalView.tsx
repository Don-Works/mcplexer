import { lazy, Suspense } from 'react'
import { Library, Server } from 'lucide-react'
import type { Workspace } from '@/api/types'

const QuickSetupPage = lazy(() => import('@/pages/QuickSetupPage').then((module) => ({ default: module.QuickSetupPage })))
const DownstreamsPage = lazy(() => import('@/pages/config/DownstreamsPage').then((module) => ({ default: module.DownstreamsPage })))

function LoadingPanel() {
  return <div className="h-72 animate-pulse bg-muted" />
}

function PanelHeading({ title, body, icon }: { title: string; body: string; icon: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3 border border-border/60 bg-card/20 px-4 py-3">
      <span className="mt-0.5 text-muted-foreground">{icon}</span>
      <div>
        <h2 className="text-base font-semibold">{title}</h2>
        <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">{body}</p>
      </div>
    </div>
  )
}

export function WorkspaceGlobalView({
  view,
  workspace,
  workspaceId,
  serverTab,
  setupServerId,
  onConfigurationChanged,
  onServerReady,
  onManageAccess,
}: {
  view: 'servers' | 'add-server'
  workspace: Workspace | null
  workspaceId: string
  serverTab: string | null
  setupServerId: string | null
  onConfigurationChanged: () => void
  onServerReady: (serverId: string) => void
  onManageAccess: (serverId: string) => void
}) {
  if (view === 'servers') {
    return (
      <>
        <PanelHeading
          title="Server library"
          icon={<Library className="h-5 w-5" />}
          body="Install and manage servers globally. Workspace access is configured separately, so enabling a server never grants it to every project."
        />
        <Suspense fallback={<LoadingPanel />}>
          <DownstreamsPage
            mode={serverTab === 'available' ? 'available' : 'installed'}
            embedded
            onServerReady={onServerReady}
          />
        </Suspense>
      </>
    )
  }

  return (
    <>
      <PanelHeading
        title={workspace ? `Add server to ${workspace.name}` : 'Add server'}
        icon={<Server className="h-5 w-5" />}
        body="Pick an existing server or create one, configure credentials, then review the workspace access before anything is changed."
      />
      <Suspense fallback={<LoadingPanel />}>
        <QuickSetupPage
          embedded
          initialWorkspaceId={workspaceId}
          initialServerId={setupServerId ?? undefined}
          onComplete={onConfigurationChanged}
          onManageAccess={onManageAccess}
        />
      </Suspense>
    </>
  )
}
