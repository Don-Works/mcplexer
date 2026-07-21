import type { TimeSeriesPoint } from '@/api/types'
import {
  Area,
  AreaChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

export type TimeRange = '1h' | '6h' | '24h' | '7d'

export function formatTime(ts: string): string {
  return new Date(ts).toLocaleTimeString()
}

export function formatHHMM(ts: string, range: TimeRange): string {
  const d = new Date(ts)
  if (range === '7d') {
    return `${String(d.getMonth() + 1).padStart(2, '0')}/${String(d.getDate()).padStart(2, '0')} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  }
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

export function formatDuration(connectedAt: string): string {
  const ms = Date.now() - new Date(connectedAt).getTime()
  const minutes = Math.floor(ms / 60000)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  const remainMin = minutes % 60
  if (hours < 24) return `${hours}h ${remainMin}m`
  const days = Math.floor(hours / 24)
  return `${days}d ${hours % 24}h`
}

export interface ChartPoint {
  time: string
  value: number
}

export function prepareChartData(
  points: TimeSeriesPoint[],
  accessor: (p: TimeSeriesPoint) => number,
): ChartPoint[] {
  return points.map((p) => ({
    time: p.bucket,
    value: accessor(p),
  }))
}

function ChartTooltip({
  active,
  payload,
  label,
  suffix,
  range,
}: {
  active?: boolean
  payload?: { value: number }[]
  label?: string
  suffix?: string
  range: TimeRange
}) {
  if (!active || !payload?.length || !label) return null
  return (
    <div className="border border-border bg-card px-3 py-1.5 font-mono text-xs shadow-lg">
      <div className="text-muted-foreground">{formatHHMM(label, range)}</div>
      <div className="text-foreground">
        {payload[0].value}
        {suffix}
      </div>
    </div>
  )
}

const chartColors = {
  primary: { stroke: 'hsl(205, 95%, 55%)', fill: 'hsl(205, 95%, 55%)' },
  green: { stroke: 'hsl(160, 70%, 45%)', fill: 'hsl(160, 70%, 45%)' },
  red: { stroke: 'hsl(0, 72%, 51%)', fill: 'hsl(0, 72%, 51%)' },
  amber: { stroke: 'hsl(38, 92%, 50%)', fill: 'hsl(38, 92%, 50%)' },
} as const

export function MetricChart({
  label,
  value,
  subtitle,
  icon,
  data,
  colorKey,
  suffix,
  range,
  onClick,
  testId,
}: {
  label: string
  value: React.ReactNode
  subtitle?: React.ReactNode
  icon: React.ReactNode
  /** Optional time-series data. Tiles without a meaningful sparkline (e.g. point-in-time peer count) can omit. */
  data?: ChartPoint[]
  colorKey: keyof typeof chartColors
  suffix?: string
  range: TimeRange
  onClick?: () => void
  testId?: string
}) {
  const color = chartColors[colorKey]
  const gradientId = `gradient-${colorKey}`
  const colorClass =
    colorKey === 'primary' ? 'text-primary'
    : colorKey === 'green' ? 'text-chart-2'
    : colorKey === 'amber' ? 'text-amber-400'
    : 'text-chart-5'

  const wrapperClass = `relative overflow-hidden rounded-lg border border-border/50 bg-card pb-24 ${
    onClick ? 'cursor-pointer text-left transition-colors hover:bg-card/80 hover:border-border focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring' : ''
  }`

  const inner = (
    <>
      <div className="p-5">
        <div className="flex items-center gap-2 text-muted-foreground">
          <span className={colorClass}>{icon}</span>
          <span className="text-[11px] uppercase tracking-widest">{label}</span>
        </div>
        <div className={`mt-3 text-3xl font-bold tracking-tight md:text-4xl ${colorClass}`}>
          {value}
        </div>
        {subtitle && <div className="mt-1">{subtitle}</div>}
      </div>
    </>
  )

  const area = !data || data.length === 0 ? null : (
    <div className="absolute bottom-0 left-0 right-0 h-24 select-none [&_svg]:outline-none [&_svg]:!cursor-default [&_.recharts-surface]:!outline-none">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={data} margin={{ top: 0, right: 0, left: 0, bottom: 0 }}>
          <defs>
            <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={color.fill} stopOpacity={0.2} />
              <stop offset="100%" stopColor={color.fill} stopOpacity={0} />
            </linearGradient>
          </defs>
          <YAxis domain={[0, (max: number) => Math.max(max, 100)]} hide />
          <XAxis
            dataKey="time"
            tickFormatter={(ts: string) => formatHHMM(ts, range)}
            tick={{ fontSize: 9, fontFamily: 'monospace', fill: '#6b7280' }}
            axisLine={false}
            tickLine={false}
            interval="preserveStartEnd"
            minTickGap={40}
          />
          <Tooltip
            content={<ChartTooltip suffix={suffix} range={range} />}
            cursor={{ stroke: '#374151', strokeDasharray: '3 3' }}
          />
          <Area
            type="monotone"
            dataKey="value"
            stroke={color.stroke}
            strokeWidth={1.5}
            fill={`url(#${gradientId})`}
            dot={false}
            activeDot={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )

  if (onClick) {
    return (
      <button type="button" onClick={onClick} className={`block w-full ${wrapperClass}`} data-testid={testId}>
        {inner}
        {area}
      </button>
    )
  }

  return (
    <div className={wrapperClass} data-testid={testId}>
      {inner}
      {area}
    </div>
  )
}
