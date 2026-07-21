// RecordSkeleton renders geometry-matched loading placeholders (DESIGN §3.8):
// muted bars at the exact geometry of the real RecordList rows + the real
// FrontmatterForm fields, so there is no layout shift on load. No spinner
// anywhere — the only motion is the calm `animate-pulse-slow` sheen the rest
// of the app uses for "waiting". Bars use the muted skeleton fill.

const BAR = 'bg-muted/60 animate-pulse-slow'

// RecordListSkeleton matches the divide-y RowShell geometry: a title bar, a
// row of status/priority/tag chip bars, and the mono id line beneath.
export function RecordListSkeleton({ rows = 6 }: { rows?: number }) {
  return (
    <ul className="divide-y divide-border" aria-hidden>
      {Array.from({ length: rows }).map((_, i) => (
        <li key={i} className="px-3 py-2">
          <div className={`h-4 w-[60%] ${BAR}`} />
          <div className="mt-1.5 flex items-center gap-1.5">
            <div className={`h-3 w-12 ${BAR}`} />
            <div className={`h-3 w-10 ${BAR}`} />
            <div className={`h-3 w-14 ${BAR}`} />
          </div>
          <div className={`mt-1.5 h-2.5 w-[40%] ${BAR}`} />
        </li>
      ))}
    </ul>
  )
}

// RecordEditorSkeleton matches the FrontmatterForm field stack: title input,
// segmented status/priority rows, a two-up due/tags row, and the body editor.
export function RecordEditorSkeleton() {
  return (
    <div className="space-y-4" aria-hidden>
      <div className="flex items-center justify-between">
        <div className={`h-6 w-28 ${BAR}`} />
        <div className={`h-5 w-40 ${BAR}`} />
      </div>
      <FieldSkeleton barClass="h-9 w-full" />
      <FieldSkeleton barClass="h-9 w-56" />
      <FieldSkeleton barClass="h-9 w-48" />
      <div className="grid grid-cols-2 gap-3">
        <FieldSkeleton barClass="h-9 w-full" />
        <FieldSkeleton barClass="h-9 w-full" />
      </div>
      <FieldSkeleton barClass="h-[220px] w-full" />
    </div>
  )
}

function FieldSkeleton({ barClass }: { barClass: string }) {
  return (
    <div className="space-y-1.5">
      <div className={`h-3 w-20 ${BAR}`} />
      <div className={`${barClass} ${BAR}`} />
    </div>
  )
}

// ScopePickerSkeleton matches the header-strip geometry (client / workspace /
// source selects) so the picker does not jump when the index loads.
export function ScopePickerSkeleton() {
  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2" aria-hidden>
      <div className={`h-3 w-10 ${BAR}`} />
      <div className={`h-8 w-[180px] ${BAR}`} />
      <div className={`h-3 w-16 ${BAR}`} />
      <div className={`h-8 w-[200px] ${BAR}`} />
      <div className={`h-3 w-12 ${BAR}`} />
      <div className={`h-8 w-32 ${BAR}`} />
    </div>
  )
}
