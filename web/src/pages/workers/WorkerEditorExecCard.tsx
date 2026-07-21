// Execution + Output cards for the WorkerEditor. Split off from
// WorkerEditorCards.tsx so each file fits the 300-line budget.

import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import type { ConcurrencyPolicy, ExecMode } from '@/api/workers'
import type { EditorState } from './worker-editor-state'
import { OutputChannelEditor } from './WorkerOutputChannelEditor'
import { RadioRow } from './WorkerEditorFormBits'

type Setter = <K extends keyof EditorState>(key: K, value: EditorState[K]) => void

export function OutputCard({ state, set }: { state: EditorState; set: Setter }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Output channels</CardTitle>
      </CardHeader>
      <CardContent>
        <OutputChannelEditor
          channels={state.outputChannels}
          onChange={(next) => set('outputChannels', next)}
        />
      </CardContent>
    </Card>
  )
}

export function ExecutionCard({ state, set }: { state: EditorState; set: Setter }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Execution</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        <fieldset className="space-y-2">
          <legend className="text-xs uppercase tracking-wider text-muted-foreground/70">
            Mode
          </legend>
          <RadioRow
            name="execMode"
            value="propose"
            current={state.execMode}
            onChange={(v) => set('execMode', v as ExecMode)}
            label="Propose-first (default)"
            hint="Drafts output and posts to mesh; you approve before any side effect."
          />
          <RadioRow
            name="execMode"
            value="autonomous"
            current={state.execMode}
            onChange={(v) => set('execMode', v as ExecMode)}
            label="Autonomous"
            hint="Executes tool calls directly, bounded by the tool allowlist."
          />
        </fieldset>
        <fieldset className="space-y-2">
          <legend className="text-xs uppercase tracking-wider text-muted-foreground/70">
            Concurrency policy
          </legend>
          <RadioRow
            name="conc"
            value="skip"
            current={state.concurrencyPolicy}
            onChange={(v) => set('concurrencyPolicy', v as ConcurrencyPolicy)}
            label="Skip (default)"
            hint="Drop the tick when a run is already in flight."
          />
          <RadioRow
            name="conc"
            value="queue"
            current={state.concurrencyPolicy}
            onChange={(v) => set('concurrencyPolicy', v as ConcurrencyPolicy)}
            label="Queue"
            hint="Run in parallel — audit-only, no serialisation guarantee."
          />
        </fieldset>
      </CardContent>
    </Card>
  )
}
