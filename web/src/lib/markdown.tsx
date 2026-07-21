// Unified Markdown renderer for every surface in the dashboard that
// holds operator-authored prose: task descriptions + notes, skill
// READMEs, memory entries, mesh signal bodies, worker tail output that
// happens to be markdown.
//
// Built on react-markdown + remark-gfm + rehype-sanitize so we get
// CommonMark + GFM (tables, strikethrough, task lists, autolinks)
// without trusting embedded HTML. Element renderers are overridden so
// the output reads native to the dark zero-radius dashboard.
//
// Bonus over a stock renderer: text nodes are post-processed through
// linkifyTaskRefs so any `task:<id>` mention anywhere in the doc lights
// up as a clickable TaskRef chip. Same affordance the rest of the app
// uses, no special syntax to remember.
//
// Backward-compat: same {source} prop the old custom parser took, so
// existing call sites (MemoryDetailDrawer, SkillDetailPane) keep
// working with no edits.

import { Children, isValidElement, useMemo, type ReactNode } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeSanitize from 'rehype-sanitize'

import { cn } from '@/lib/utils'
import { linkifyTaskRefs } from '@/pages/tasks/task-utils'

// eslint-disable-next-line react-refresh/only-export-components
export function stripFrontmatter(body: string): string {
  const trimmed = body.replace(/^[\s\r\n]+/, '')
  if (!trimmed.startsWith('---')) return body
  const end = trimmed.indexOf('\n---', 3)
  if (end < 0) return body
  return trimmed.slice(end + 4).replace(/^[\r\n]+/, '')
}

export interface MarkdownProps {
  source: string
  // workspaceId, when set, scopes `task:<id>` autolinks so the chip
  // navigates straight to that workspace's task detail. Without it,
  // links fall back to the global /tasks?focus=<id> redirect.
  workspaceId?: string
  // className lets the caller adjust the outer container's spacing or
  // typography baseline. Defaults to the dashboard's standard prose
  // density.
  className?: string
}

export function Markdown({ source, workspaceId, className }: MarkdownProps) {
  const components = useMemo(
    () => buildComponents(workspaceId),
    [workspaceId],
  )
  // Empty-string short-circuit: react-markdown still renders an empty
  // <div> wrapper which steals layout space. The caller-side null check
  // pairs naturally with the surrounding "no description." fallback.
  // Memo must be called above this early-return to satisfy the
  // rules-of-hooks lint.
  if (!source) return null

  return (
    <div className={cn('space-y-3 text-[13px] leading-relaxed text-foreground/90', className)}>
      <ReactMarkdown
        // skipHtml + sanitize — defense in depth. We render markdown
        // that may have originated from mesh-untrusted peers; raw HTML
        // would otherwise become an XSS path.
        skipHtml
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeSanitize]}
        components={components}
      >
        {source}
      </ReactMarkdown>
    </div>
  )
}

function buildComponents(workspaceId?: string) {
  // Wrap a children array so every text node passes through the task-
  // ref autolinker. We don't traverse non-string children — those are
  // already React elements (code spans, strong/em, etc.) that the
  // parent renderer is responsible for.
  const linkify = (children: ReactNode): ReactNode => {
    return Children.map(children, (child) => {
      if (typeof child === 'string') {
        return linkifyTaskRefs(child, { workspaceId })
      }
      return child
    })
  }

  return {
    h1: ({ children }: { children?: ReactNode }) => (
      <h2 className="text-base font-semibold tracking-tight text-foreground">{linkify(children)}</h2>
    ),
    h2: ({ children }: { children?: ReactNode }) => (
      <h3 className="mt-2 text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
        {linkify(children)}
      </h3>
    ),
    h3: ({ children }: { children?: ReactNode }) => (
      <h4 className="text-[13px] font-semibold text-foreground">{linkify(children)}</h4>
    ),
    h4: ({ children }: { children?: ReactNode }) => (
      <h5 className="text-[12px] font-semibold text-foreground/90">{linkify(children)}</h5>
    ),
    p: ({ children }: { children?: ReactNode }) => (
      <p className="text-foreground/85">{linkify(children)}</p>
    ),
    a: ({ children, href }: { children?: ReactNode; href?: string }) => (
      <a
        href={href}
        target="_blank"
        rel="noreferrer noopener"
        className="text-primary underline-offset-2 hover:underline"
      >
        {children}
      </a>
    ),
    strong: ({ children }: { children?: ReactNode }) => (
      <strong className="font-semibold text-foreground">{linkify(children)}</strong>
    ),
    em: ({ children }: { children?: ReactNode }) => (
      <em className="italic text-foreground/90">{linkify(children)}</em>
    ),
    del: ({ children }: { children?: ReactNode }) => (
      <del className="text-muted-foreground/70 line-through">{linkify(children)}</del>
    ),
    code: ({
      inline,
      className,
      children,
    }: {
      inline?: boolean
      className?: string
      children?: ReactNode
    }) => {
      // react-markdown distinguishes inline vs block by passing
      // inline=true only for spans. The block path is owned by the pre
      // renderer below; here we render inline mono chips only.
      const isInline = inline ?? !className
      if (isInline) {
        return (
          <code className="border border-border/60 bg-muted/50 px-1 py-px font-mono text-[12px] text-foreground">
            {children}
          </code>
        )
      }
      // Block code falls through to the pre renderer; preserve the
      // language class for the header chip.
      return (
        <code className={cn('font-mono text-[12.5px] leading-relaxed text-foreground/90', className)}>
          {children}
        </code>
      )
    },
    pre: ({ children }: { children?: ReactNode }) => {
      // Reach into the code child to lift its language class onto the
      // pre header. Keeps the language label visible without a syntax
      // highlighter dep.
      let lang = ''
      Children.forEach(children, (child) => {
        if (!isValidElement(child)) return
        const childClass =
          typeof (child.props as Record<string, unknown>).className === 'string'
            ? ((child.props as Record<string, unknown>).className as string)
            : ''
        const match = /language-([\w-]+)/.exec(childClass ?? '')
        if (match) lang = match[1]
      })
      return (
        <pre className="overflow-x-auto border border-border bg-muted/40 px-3 py-2 font-mono text-[12px] leading-relaxed text-foreground/90">
          {lang ? (
            <div className="mb-2 text-[10px] uppercase tracking-wider text-muted-foreground">{lang}</div>
          ) : null}
          {children}
        </pre>
      )
    },
    ul: ({ children }: { children?: ReactNode }) => (
      <ul className="list-disc space-y-1 pl-5 marker:text-muted-foreground/50">{children}</ul>
    ),
    ol: ({ children }: { children?: ReactNode }) => (
      <ol className="list-decimal space-y-1 pl-5 marker:text-muted-foreground/50">{children}</ol>
    ),
    li: ({ children }: { children?: ReactNode }) => (
      <li className="text-foreground/90">{linkify(children)}</li>
    ),
    blockquote: ({ children }: { children?: ReactNode }) => (
      // Full border, not the banned side-stripe. Slight tint pulls it
      // off the surrounding surface without competing with status chips.
      <blockquote className="border border-border bg-muted/30 px-3 py-2 text-foreground/80">
        {children}
      </blockquote>
    ),
    hr: () => <hr className="border-border" />,
    table: ({ children }: { children?: ReactNode }) => (
      <div className="overflow-x-auto border border-border">
        <table className="w-full text-[12.5px]">{children}</table>
      </div>
    ),
    thead: ({ children }: { children?: ReactNode }) => (
      <thead className="border-b border-border bg-card/40 text-left text-muted-foreground">{children}</thead>
    ),
    th: ({ children }: { children?: ReactNode }) => (
      <th className="px-3 py-2 text-[11px] font-semibold uppercase tracking-wider">{children}</th>
    ),
    td: ({ children }: { children?: ReactNode }) => (
      <td className="border-t border-border/60 px-3 py-1.5 text-foreground/85">{linkify(children)}</td>
    ),
    input: ({ checked, disabled, type }: { checked?: boolean; disabled?: boolean; type?: string }) => {
      // GFM task-list checkboxes — keep them read-only chips, not
      // interactive form inputs (the source markdown is what carries
      // task state in our world, not transient UI state).
      if (type !== 'checkbox') return null
      return (
        <span
          aria-disabled={disabled || undefined}
          className={cn(
            'mr-1 inline-flex h-3 w-3 shrink-0 -translate-y-px items-center justify-center border border-border align-middle',
            checked ? 'bg-primary/70' : 'bg-transparent',
          )}
        >
          {checked ? <span className="block h-1.5 w-1.5 bg-background" /> : null}
        </span>
      )
    },
  }
}
