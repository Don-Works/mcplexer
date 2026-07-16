import { useCallback, useEffect, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { Check, Copy, Loader2, Sparkles, Terminal } from 'lucide-react'
import { toast } from 'sonner'
import { getHealth, launchSystemTerminal } from '@/api/client'
import { cn } from '@/lib/utils'

// Open a terminal at ~/.mcplexer so the user can drive mcplexer's MCP tools
// from claude / opencode / codex / grok / mimo. This is the canonical "configure" path —
// the sidebar button is the single, persistent surface. Per-page banners
// were removed because banner blindness eats them anyway.

interface Props {
  variant?: 'sidebar' | 'hero'
  className?: string
}

const PROMPT_TEMPLATE_BY_PAGE: Record<string, string> = {
  workspaces: 'Help me add a workspace. Use mcpx__execute_code to call admin.create_workspace().',
  routes: 'Help me add a route rule. Use mcpx__execute_code to call admin.create_route().',
  credentials: 'Help me add a credential / auth scope. Use mcpx__execute_code to call admin.create_auth_scope().',
  'auth-scopes': 'Help me add a credential / auth scope. Use mcpx__execute_code to call admin.create_auth_scope().',
  oauth: 'Help me register an OAuth provider. Use mcpx__execute_code to call admin.create_oauth_provider().',
  'oauth-providers': 'Help me register an OAuth provider. Use mcpx__execute_code to call admin.create_oauth_provider().',
  descriptions: 'Help me refine a tool description.',
  'create-mcp': 'Help me register a custom MCP server. Search MCPlexer tools first, then add it to the right workspace.',
  default: 'Help me configure mcplexer using mcpx__* tools. Run mcpx__search_tools first to see what is available.',
}

function pageContextFromPath(pathname: string, search: string): string {
  if (pathname === '/workspaces') {
    const view = new URLSearchParams(search).get('view')
    if (view === 'settings' || view === 'new-workspace') return 'workspaces'
    if (!view || view === 'access') return 'routes'
  }
  const seg = pathname.split('/').filter(Boolean).pop() ?? ''
  return PROMPT_TEMPLATE_BY_PAGE[seg] ? seg : 'default'
}

export function ConfigureWithAI({ variant = 'sidebar', className }: Props) {
  const [busy, setBusy] = useState(false)
  const [dataDir, setDataDir] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const location = useLocation()
  const pageContext = pageContextFromPath(location.pathname, location.search)

  useEffect(() => {
    let cancelled = false
    getHealth()
      .then((h) => {
        if (!cancelled) setDataDir(h?.system?.data_dir ?? null)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [])

  const copyPath = useCallback(async () => {
    if (!dataDir) return
    try {
      await navigator.clipboard.writeText(dataDir)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch (err) {
      toast.error('Could not copy path', {
        description: err instanceof Error ? err.message : String(err),
      })
    }
  }, [dataDir])

  const launch = useCallback(async () => {
    setBusy(true)
    try {
      await launchSystemTerminal('data_dir')
      const prompt = PROMPT_TEMPLATE_BY_PAGE[pageContext] ?? PROMPT_TEMPLATE_BY_PAGE.default
      toast.success('Terminal opened at ~/.mcplexer', {
        description: `Try: "${prompt}"`,
        duration: 8000,
      })
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Failed to open terminal'
      toast.error('Could not launch terminal', { description: msg })
    } finally {
      setBusy(false)
    }
  }, [pageContext])

  if (variant === 'hero') {
    return (
      <div
        className={cn(
          'flex items-start gap-3 border border-primary/20 bg-primary/5 px-4 py-3',
          className,
        )}
        data-testid="configure-with-ai-hero"
      >
        <div className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center bg-primary/10 text-primary">
          <Sparkles className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium text-foreground">Configure with AI</div>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Open a terminal at <span className="font-mono">~/.mcplexer</span> and ask claude / opencode / codex / mimo to do this for you. The agent will drive
            mcplexer&apos;s <span className="font-mono">mcpx__*</span> tools — no UI hunting required.
          </p>
        </div>
        <div className="flex shrink-0 flex-col gap-1.5">
          <button
            type="button"
            onClick={launch}
            disabled={busy}
            data-testid="configure-with-ai-launch"
            className="inline-flex items-center justify-center gap-1.5 bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-50"
          >
            {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Terminal className="h-3.5 w-3.5" />}
            Open terminal
          </button>
          <button
            type="button"
            onClick={copyPath}
            disabled={!dataDir}
            title={dataDir ? `Copy ${dataDir}` : 'Loading config path…'}
            aria-label="Copy mcplexer config directory path"
            data-testid="configure-with-ai-hero-copy-path"
            className="inline-flex items-center justify-center gap-1.5 border border-dashed border-primary/30 bg-transparent px-3 py-1 text-[11px] text-muted-foreground transition-colors hover:border-primary/60 hover:text-foreground disabled:opacity-50"
          >
            {copied ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
            {copied ? 'Path copied' : 'Copy path'}
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      <button
        type="button"
        onClick={launch}
        disabled={busy}
        data-testid="configure-with-ai-button"
        className="inline-flex w-full items-center justify-center gap-2 bg-primary/10 px-3 py-1.5 text-xs font-medium text-primary transition-colors hover:bg-primary/15 disabled:opacity-50"
      >
        {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Sparkles className="h-3.5 w-3.5" />}
        Configure with AI
      </button>
      <button
        type="button"
        onClick={copyPath}
        disabled={!dataDir}
        title={dataDir ? `Copy ${dataDir}` : 'Loading config path…'}
        aria-label="Copy mcplexer config directory path"
        data-testid="configure-with-ai-copy-path"
        className="inline-flex w-full items-center justify-center gap-1.5 border border-dashed border-border/60 bg-transparent px-3 py-1 text-[11px] text-muted-foreground transition-colors hover:border-border hover:text-foreground disabled:opacity-50"
      >
        {copied ? <Check className="h-3 w-3 text-emerald-500" /> : <Copy className="h-3 w-3" />}
        {copied ? 'Path copied' : 'Copy config path'}
      </button>
    </div>
  )
}
