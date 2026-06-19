// MemoryGetStartedPanel — replaces the passive "Nothing learned yet"
// activity card when the gateway has zero memories. Three concrete
// steps: (1) pull existing memories into your harness via @import, (2)
// write your first memory with a copy-paste curl, (3) trigger the
// consolidator. Active, not passive.

import { Link } from 'react-router-dom'
import { Brain, PlayCircle, Sparkles, Terminal } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { CopyButton } from '@/components/ui/copy-button'
import { Badge } from '@/components/ui/badge'

const CLAUDE_IMPORT_LINE = '@~/.mcplexer/memory-exports/global.md'

const CURL_BODY = `{
  "kind": "fact",
  "name": "Project deploy gate",
  "content": "Never deploy a dirty working tree; HEAD must equal origin/main."
}`

export function MemoryGetStartedPanel() {
  // Derive the API origin from where the dashboard is actually served so
  // the operator's first copy-paste step hits the running gateway instead
  // of a hardcoded port that may be wrong.
  const apiOrigin =
    typeof window !== 'undefined'
      ? window.location.origin
      : 'http://localhost:3333'
  const curlCommand = `curl -X POST ${apiOrigin}/api/v1/memory \\
  -H 'Content-Type: application/json' \\
  -d '${CURL_BODY.replace(/\n/g, ' ')}'`
  return (
    <Card>
      <CardContent className="space-y-5 p-4">
        <div className="flex items-center gap-2">
          <Sparkles className="h-4 w-4 text-primary" />
          <h2 className="text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
            Getting started
          </h2>
          <span className="ml-auto font-mono text-[10px] uppercase tracking-wider text-muted-foreground/60">
            3 steps
          </span>
        </div>

        <Step
          number={1}
          icon={<Brain className="h-3.5 w-3.5" />}
          title="Wire it into your harness"
          body="Add this @import line to your CLAUDE.md or equivalent harness config so every agent session sees your gateway's memories on every turn."
          snippet={CLAUDE_IMPORT_LINE}
          language="markdown"
        />

        <Step
          number={2}
          icon={<Terminal className="h-3.5 w-3.5" />}
          title="Write your first memory"
          body={
            <>
              Run this from any terminal. The same shape ships through{' '}
              <code className="font-mono text-foreground/80">
                memory__write_memory
              </code>{' '}
              when an agent learns something worth keeping.
            </>
          }
          snippet={curlCommand}
          language="bash"
        />

        <Step
          number={3}
          icon={<PlayCircle className="h-3.5 w-3.5" />}
          title="Run the consolidator"
          body={
            <>
              The consolidator worker rolls up duplicates, summarises long
              notes, and earmarks pin candidates. Configure + enable it from{' '}
              <Link
                to="/memory/consolidation"
                className="text-primary hover:underline"
              >
                Consolidation
              </Link>{' '}
              once you have a handful of memories.
            </>
          }
        />
      </CardContent>
    </Card>
  )
}

function Step({
  number,
  icon,
  title,
  body,
  snippet,
  language,
}: {
  number: number
  icon: React.ReactNode
  title: string
  body: React.ReactNode
  snippet?: string
  language?: string
}) {
  return (
    <div className="flex gap-3">
      <span className="mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center border border-border bg-card/60 font-mono text-[10px] tabular-nums text-muted-foreground">
        {number}
      </span>
      <div className="min-w-0 flex-1 space-y-2">
        <div className="flex items-center gap-2">
          <span className="text-primary/70">{icon}</span>
          <h3 className="text-[13px] font-semibold text-foreground">{title}</h3>
          {language && (
            <Badge variant="outline" tone="mono" className="font-mono text-[9px]">
              {language}
            </Badge>
          )}
        </div>
        <p className="text-[11.5px] leading-relaxed text-muted-foreground">{body}</p>
        {snippet && (
          <div className="group/snippet flex items-stretch border border-border bg-background/60">
            <pre className="flex-1 overflow-x-auto whitespace-pre px-3 py-2 font-mono text-[11.5px] text-emerald-300">
              {snippet}
            </pre>
            <CopyButton
              value={snippet}
              className="self-stretch px-2 text-muted-foreground"
            />
          </div>
        )}
      </div>
    </div>
  )
}
