// WorkerConfigView — read-only display of a Worker's configuration.
// Used on the detail page; the editor is a separate component. JSON
// fields are pretty-printed when valid, raw-displayed when not so the
// user can always see what's actually stored.

import type { Worker } from '@/api/workers'
import { humanizeSchedule, summariseModel } from './worker-utils'

export function ConfigView({ worker }: { worker: Worker }) {
  return (
    <dl className="grid grid-cols-1 gap-x-6 gap-y-3 text-xs sm:grid-cols-2">
      <Field label="Provider">{summariseModel(worker.model_provider, worker.model_id)}</Field>
      <Field label="Endpoint">{worker.model_endpoint_url || '—'}</Field>
      <Field label="Secret scope">{worker.secret_scope_id || '—'}</Field>
      <Field label="Skill">
        {worker.skill_name
          ? `${worker.skill_name}${worker.skill_version ? `@${worker.skill_version}` : ''}`
          : '—'}
      </Field>
      <Field label="Schedule">
        <div>{humanizeSchedule(worker.schedule_spec)}</div>
        <div className="mt-0.5 font-mono text-[10px] text-muted-foreground/70">
          {worker.schedule_spec}
        </div>
      </Field>
      <Field label="Exec mode">{worker.exec_mode}</Field>
      <Field label="Concurrency policy">{worker.concurrency_policy}</Field>
      <Field label="Workspace">{worker.workspace_id}</Field>
      <Field label="Prompt template" wide>
        <CodeBlock>{worker.prompt_template || '(empty)'}</CodeBlock>
      </Field>
      <Field label="Parameters" wide>
        <CodeBlock>{prettyJSON(worker.parameters_json)}</CodeBlock>
      </Field>
      <Field label="Tool allowlist" wide>
        <CodeBlock>{prettyJSON(worker.tool_allowlist_json)}</CodeBlock>
      </Field>
      {worker.pre_execute_script && worker.pre_execute_script.trim() !== '' && (
        <Field label="Pre-execute hook" wide>
          <CodeBlock>{worker.pre_execute_script}</CodeBlock>
        </Field>
      )}
      {worker.post_execute_script && worker.post_execute_script.trim() !== '' && (
        <Field label="Post-execute hook" wide>
          <CodeBlock>{worker.post_execute_script}</CodeBlock>
        </Field>
      )}
      <Field label="Output channels" wide>
        <CodeBlock>{prettyJSON(worker.output_channels_json)}</CodeBlock>
      </Field>
      <Field label="Max input tokens">{capLabel(worker.max_input_tokens, 'no cap')}</Field>
      <Field label="Max output tokens">{capLabel(worker.max_output_tokens, 'default (4096)')}</Field>
      <Field label="Max tool calls">{capLabel(worker.max_tool_calls, 'default (50)')}</Field>
      <Field label="Max wall-clock seconds">{capLabel(worker.max_wall_clock_seconds, 'default (300)')}</Field>
      <Field label="Max monthly cost (USD)">
        {worker.max_monthly_cost_usd > 0
          ? `$${worker.max_monthly_cost_usd.toFixed(2)}`
          : 'no cap'}
      </Field>
      <Field label="Max consecutive failures">
        {capLabel(worker.max_consecutive_failures, 'no auto-pause')}
      </Field>
      {worker.auto_paused_reason && worker.auto_paused_reason.trim() !== '' && (
        <Field label="Auto-paused reason" wide>
          <span className="text-destructive">{worker.auto_paused_reason}</span>
        </Field>
      )}
    </dl>
  )
}

function capLabel(v: number, fallback: string): string {
  if (!v) return fallback
  return String(v)
}

interface FieldProps {
  label: string
  children: React.ReactNode
  wide?: boolean
}

function Field({ label, children, wide }: FieldProps) {
  return (
    <div className={wide ? 'sm:col-span-2' : ''}>
      <dt className="text-[10px] uppercase tracking-wider text-muted-foreground/60">
        {label}
      </dt>
      <dd className="mt-0.5 text-foreground">{children}</dd>
    </div>
  )
}

function CodeBlock({ children }: { children: React.ReactNode }) {
  return (
    <pre className="max-h-56 overflow-auto rounded-md border border-border/60 bg-background/60 p-2.5 font-mono text-[11px] whitespace-pre-wrap">
      {children}
    </pre>
  )
}

function prettyJSON(raw: string): string {
  if (!raw) return '(empty)'
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}
