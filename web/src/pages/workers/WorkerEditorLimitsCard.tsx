// WorkerEditorLimitsCard (M1) — per-worker safety caps. Two groups:
//   • Per-run limits: input/output tokens, tool calls, wall-clock secs.
//   • Budget + auto-pause: monthly spend cap, consecutive-failure cap.
//
// Every field shows 0 / empty as "default" so an operator who hasn't
// thought about it gets the runner's package defaults rather than an
// accidental zero-cap that fails closed on every run.

import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import type { EditorState } from './worker-editor-state'
import { Row } from './WorkerEditorFormBits'

type Setter = <K extends keyof EditorState>(key: K, value: EditorState[K]) => void

export function LimitsCard({ state, set }: { state: EditorState; set: Setter }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Safety limits</CardTitle>
        <p className="text-xs text-muted-foreground">
          Leave any field blank to use the runner default. 0 = "no cap"
          for monthly budget + consecutive-failure auto-pause.
        </p>
      </CardHeader>
      <CardContent className="space-y-5">
        <section className="space-y-3">
          <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground/70">
            Per-run limits
          </h3>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <CapInput
              label="Max input tokens"
              id="cap-input"
              value={state.maxInputTokens}
              onChange={(v) => set('maxInputTokens', v)}
              placeholder="default: no cap"
            />
            <CapInput
              label="Max output tokens"
              id="cap-output"
              value={state.maxOutputTokens}
              onChange={(v) => set('maxOutputTokens', v)}
              placeholder="default: 4096"
            />
            <CapInput
              label="Max tool calls"
              id="cap-tools"
              value={state.maxToolCalls}
              onChange={(v) => set('maxToolCalls', v)}
              placeholder="default: 50"
            />
            <CapInput
              label="Max wall-clock seconds"
              id="cap-wall"
              value={state.maxWallClockSeconds}
              onChange={(v) => set('maxWallClockSeconds', v)}
              placeholder="default: 300"
            />
          </div>
        </section>
        <section className="space-y-3">
          <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground/70">
            Budget + auto-pause
          </h3>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <CapInput
              label="Max monthly cost (USD)"
              id="cap-cost"
              value={state.maxMonthlyCostUSD}
              onChange={(v) => set('maxMonthlyCostUSD', v)}
              placeholder="0 = no cap"
              step="0.01"
            />
            <CapInput
              label="Max consecutive failures"
              id="cap-streak"
              value={state.maxConsecutiveFailures}
              onChange={(v) => set('maxConsecutiveFailures', v)}
              placeholder="0 = no auto-pause"
            />
          </div>
          <p className="text-[10px] text-muted-foreground/70">
            When either cap fires, the worker is paused automatically
            and a mesh alert lands in the Signal tray.
          </p>
        </section>
      </CardContent>
    </Card>
  )
}

interface CapInputProps {
  label: string
  id: string
  value: string
  onChange: (v: string) => void
  placeholder?: string
  step?: string
}

function CapInput({ label, id, value, onChange, placeholder, step }: CapInputProps) {
  return (
    <Row label={label} htmlFor={id}>
      <Input
        id={id}
        type="number"
        inputMode="decimal"
        min="0"
        step={step ?? '1'}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
      />
    </Row>
  )
}
