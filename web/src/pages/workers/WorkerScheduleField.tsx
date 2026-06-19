// WorkerScheduleField — friendly schedule builder with three modes:
// (1) preset chips, (2) visual builder (Every X "Y"), (3) advanced cron.
// All three modes write the same `schedule_spec` string the backend
// already accepts. The component keeps the active mode in local state
// so switching back to the advanced view preserves what the builder
// produced.

import { useEffect, useState } from 'react'
import { Calendar, Clock, Code2, Zap } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { humanizeScheduleOrNull } from './worker-utils'

interface Preset {
  label: string
  spec: string
  hint: string
}

const PRESETS: Preset[] = [
  { label: 'Every 5m', spec: '5m', hint: 'Every 5 minutes' },
  { label: 'Hourly', spec: '0 * * * *', hint: 'Top of every hour' },
  { label: 'Every 6h', spec: '0 */6 * * *', hint: '4 times a day' },
  { label: 'Daily 9am', spec: '0 9 * * *', hint: 'Morning kickoff' },
  { label: 'Weekdays 9am', spec: '0 9 * * 1-5', hint: 'Mon–Fri, 9am' },
  { label: 'Mon 9am', spec: '0 9 * * 1', hint: 'Weekly digest' },
]

type Mode = 'preset' | 'builder' | 'advanced'

interface BuilderState {
  unit: 'minutes' | 'hours' | 'days'
  every: number
  hour: number
  minute: number
}

interface Props {
  value: string
  onChange: (next: string) => void
}

export function WorkerScheduleField({ value, onChange }: Props) {
  const [mode, setMode] = useState<Mode>(() => detectMode(value))
  const [builder, setBuilder] = useState<BuilderState>(() => parseBuilderState(value))
  const humanPreview = humanizeScheduleOrNull(value)
  const parseError = value.trim().length > 0 && humanPreview === null

  useEffect(() => {
    if (mode === 'builder') {
      const next = builderToSpec(builder)
      if (next !== value) onChange(next)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [builder, mode])

  return (
    <div className="space-y-3">
      <ModeTabs mode={mode} onChange={setMode} />

      {mode === 'preset' && (
        <PresetGrid
          activeSpec={value}
          onPick={(spec) => onChange(spec)}
        />
      )}

      {mode === 'builder' && (
        <Builder state={builder} onChange={setBuilder} />
      )}

      {mode === 'advanced' && (
        <div className="space-y-1.5">
          <Input
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder="0 * * * *  or  5m"
            className="font-mono text-xs"
            data-testid="worker-schedule"
          />
          <p className="text-[10px] text-muted-foreground/70">
            5-field cron (<code>min hr dom mon dow</code>) or Go duration (<code>5m</code>, <code>1h30m</code>).
          </p>
        </div>
      )}

      <SchedulePreview spec={value} humanPreview={humanPreview} parseError={parseError} />
    </div>
  )
}

interface ModeTabsProps {
  mode: Mode
  onChange: (next: Mode) => void
}

function ModeTabs({ mode, onChange }: ModeTabsProps) {
  return (
    <div className="inline-flex rounded-md border border-border/60 bg-muted/30 p-0.5 text-xs">
      <ModeTab active={mode === 'preset'} onClick={() => onChange('preset')} icon={<Zap className="h-3 w-3" />} label="Presets" />
      <ModeTab active={mode === 'builder'} onClick={() => onChange('builder')} icon={<Clock className="h-3 w-3" />} label="Builder" />
      <ModeTab active={mode === 'advanced'} onClick={() => onChange('advanced')} icon={<Code2 className="h-3 w-3" />} label="Cron" />
    </div>
  )
}

interface ModeTabProps {
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  label: string
}

function ModeTab({ active, onClick, icon, label }: ModeTabProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        'flex items-center gap-1.5 rounded px-2.5 py-1 transition-colors ' +
        (active ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground')
      }
    >
      {icon}
      {label}
    </button>
  )
}

interface PresetGridProps {
  activeSpec: string
  onPick: (spec: string) => void
}

function PresetGrid({ activeSpec, onPick }: PresetGridProps) {
  return (
    <div className="grid grid-cols-2 gap-1.5 sm:grid-cols-3">
      {PRESETS.map((p) => {
        const active = p.spec === activeSpec
        return (
          <button
            key={p.spec}
            type="button"
            onClick={() => onPick(p.spec)}
            className={
              'rounded border px-2.5 py-1.5 text-left text-xs transition-colors ' +
              (active
                ? 'border-primary bg-primary/10 text-foreground'
                : 'border-border/60 bg-card/40 text-foreground hover:bg-card')
            }
          >
            <div className="font-medium">{p.label}</div>
            <div className="mt-0.5 text-[10px] text-muted-foreground">{p.hint}</div>
          </button>
        )
      })}
    </div>
  )
}

interface BuilderProps {
  state: BuilderState
  onChange: (next: BuilderState) => void
}

function Builder({ state, onChange }: BuilderProps) {
  const everyAtTime = state.unit === 'days'
  return (
    <div className="space-y-3 rounded border border-border/40 bg-muted/20 p-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="text-muted-foreground">Every</span>
        <Input
          type="number"
          min={1}
          max={state.unit === 'minutes' ? 59 : state.unit === 'hours' ? 23 : 365}
          value={state.every}
          onChange={(e) => onChange({ ...state, every: clamp(parseInt(e.target.value, 10) || 1, 1, 999) })}
          className="h-7 w-16 text-xs"
        />
        <select
          value={state.unit}
          onChange={(e) => onChange({ ...state, unit: e.target.value as BuilderState['unit'] })}
          className="h-7 rounded border border-border/60 bg-background px-1.5 text-xs"
        >
          <option value="minutes">minute(s)</option>
          <option value="hours">hour(s)</option>
          <option value="days">day(s)</option>
        </select>
      </div>

      {everyAtTime && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Label className="text-muted-foreground text-xs">at</Label>
          <TimePicker hour={state.hour} minute={state.minute} onChange={(h, m) => onChange({ ...state, hour: h, minute: m })} />
        </div>
      )}

      <p className="text-[10px] text-muted-foreground/70">
        {everyAtTime ? <Calendar className="inline h-3 w-3 mr-1" /> : <Clock className="inline h-3 w-3 mr-1" />}
        Generates: <code className="font-mono">{builderToSpec(state)}</code>
      </p>
    </div>
  )
}

interface TimePickerProps {
  hour: number
  minute: number
  onChange: (hour: number, minute: number) => void
}

function TimePicker({ hour, minute, onChange }: TimePickerProps) {
  return (
    <div className="inline-flex items-center gap-1 rounded border border-border/60 bg-background px-2 py-1 font-mono text-xs">
      <input
        type="number"
        min={0}
        max={23}
        value={String(hour).padStart(2, '0')}
        onChange={(e) => onChange(clamp(parseInt(e.target.value, 10) || 0, 0, 23), minute)}
        className="w-7 bg-transparent text-center outline-none"
      />
      <span>:</span>
      <input
        type="number"
        min={0}
        max={59}
        value={String(minute).padStart(2, '0')}
        onChange={(e) => onChange(hour, clamp(parseInt(e.target.value, 10) || 0, 0, 59))}
        className="w-7 bg-transparent text-center outline-none"
      />
    </div>
  )
}

interface PreviewProps {
  spec: string
  humanPreview: string | null
  parseError: boolean
}

function SchedulePreview({ spec, humanPreview, parseError }: PreviewProps) {
  if (!spec.trim()) {
    return (
      <p className="text-[11px] text-muted-foreground/70">
        Pick a preset above or switch to Builder for custom timing.
      </p>
    )
  }
  if (parseError) {
    return (
      <p className="rounded bg-amber-500/10 px-2 py-1 text-[11px] text-amber-700 dark:text-amber-300">
        ⚠ Couldn't parse <code className="font-mono">{spec}</code>. The
        backend will reject this at save time.
      </p>
    )
  }
  return (
    <p className="text-[11px] text-muted-foreground">
      Will fire: <span className="font-medium text-foreground">{humanPreview}</span>
    </p>
  )
}

function detectMode(spec: string): Mode {
  if (!spec.trim()) return 'preset'
  if (PRESETS.some((p) => p.spec === spec)) return 'preset'
  if (matchBuilder(spec)) return 'builder'
  return 'advanced'
}

function matchBuilder(spec: string): boolean {
  // Builder produces only these shapes:
  //   N + ('m'|'h')      → every N minutes/hours
  //   '0 HH * * *'       → daily at HH:00 (single hour, simple)
  //   '*/N * * * *'      → every N minutes
  //   '0 */N * * *'      → every N hours
  return /^(\d+m|\d+h|0 \d{1,2} \* \* \*|\*\/\d+ \* \* \* \*|0 \*\/\d+ \* \* \*|M \d+ \d+ \* \* \*)$/.test(spec)
}

function parseBuilderState(spec: string): BuilderState {
  const fallback: BuilderState = { unit: 'hours', every: 1, hour: 9, minute: 0 }
  if (!spec.trim()) return fallback
  const mDur = /^(\d+)(m|h)$/.exec(spec)
  if (mDur) {
    return { ...fallback, unit: mDur[2] === 'm' ? 'minutes' : 'hours', every: parseInt(mDur[1], 10) }
  }
  const mEveryMin = /^\*\/(\d+) \* \* \* \*$/.exec(spec)
  if (mEveryMin) return { ...fallback, unit: 'minutes', every: parseInt(mEveryMin[1], 10) }
  const mEveryHour = /^0 \*\/(\d+) \* \* \*$/.exec(spec)
  if (mEveryHour) return { ...fallback, unit: 'hours', every: parseInt(mEveryHour[1], 10) }
  const mDaily = /^(\d+) (\d+) \* \* \*$/.exec(spec)
  if (mDaily) return { unit: 'days', every: 1, hour: parseInt(mDaily[2], 10), minute: parseInt(mDaily[1], 10) }
  return fallback
}

function builderToSpec(state: BuilderState): string {
  if (state.unit === 'minutes') return `${state.every}m`
  if (state.unit === 'hours') return `${state.every}h`
  // days
  if (state.every === 1) return `${state.minute} ${state.hour} * * *`
  return `${state.minute} ${state.hour} */${state.every} * *`
}

function clamp(n: number, lo: number, hi: number): number {
  return Math.min(Math.max(n, lo), hi)
}
