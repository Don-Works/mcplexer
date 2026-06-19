import { useCallback, useEffect, useRef, useState } from 'react'
import { cn } from '@/lib/utils'
import { useGhostText, nextWordBoundary, type GhostState } from './useGhostText'

// GhostText renders a text field (textarea or input) with an inline Copilot-
// style ghost suggestion after the caret (DESIGN §3.4 / §4.2): muted 45%
// opacity, no popup. Graded accept: `→` (right-arrow at the caret boundary)
// accepts the next word; `Tab` accepts all; any other key / `esc` /
// click-away dismisses. The ghost text NEVER traps keys it doesn't own — `→`
// only consumes the event when a ghost is showing AND the caret is at the end
// of the value, else it's a normal cursor move.
//
// The ghost is painted by a mirror layer behind a transparent field: the
// mirror renders the (transparent) live value followed by the muted ghost
// span, so the suggestion lands exactly where the next character would. The
// hook only ever suggests at end-of-value, so the mirror geometry is exact.

interface BaseProps {
  value: string
  onChange: (v: string) => void
  field: string
  workspace?: string
  className?: string
  placeholder?: string
  // id is set on the underlying textarea/input so an owning Field's
  // <Label htmlFor> association resolves and a screen reader announces the
  // field name on focus (DESIGN §7).
  id?: string
  // onState surfaces the live ghost state (inFlight/profile) so the editor can
  // drive a single shared ModelPresenceLabel.
  onState?: (s: GhostState) => void
}

export function GhostTextarea(props: BaseProps) {
  return <GhostField {...props} multiline />
}

export function GhostInput(props: BaseProps) {
  return <GhostField {...props} multiline={false} />
}

function GhostField({
  value,
  onChange,
  field,
  workspace,
  className,
  placeholder,
  id,
  onState,
  multiline,
}: BaseProps & { multiline: boolean }) {
  const [caret, setCaret] = useState(value.length)
  const ref = useRef<HTMLTextAreaElement | HTMLInputElement | null>(null)
  const { ghost, inFlight, profile, degraded, clear } = useGhostText({
    field,
    workspace,
    value,
    caret,
  })

  // Surface state upward for the shared ModelPresenceLabel. Done in an effect
  // (not at render time) so it never set-states the parent during our render.
  useEffect(() => {
    onState?.({ ghost, inFlight, profile, degraded })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inFlight, profile, degraded])

  const showGhost = ghost !== '' && caret >= value.length

  const acceptAll = useCallback(() => {
    if (!ghost) return
    onChange(value + ghost)
    clear()
    // Move the caret to the new end after React commits.
    queueMicrotask(() => {
      const el = ref.current
      if (el) {
        const end = (value + ghost).length
        el.setSelectionRange(end, end)
        setCaret(end)
      }
    })
  }, [ghost, value, onChange, clear])

  const acceptWord = useCallback(() => {
    if (!ghost) return
    const n = nextWordBoundary(ghost)
    const slice = ghost.slice(0, n)
    onChange(value + slice)
    queueMicrotask(() => {
      const el = ref.current
      if (el) {
        const end = (value + slice).length
        el.setSelectionRange(end, end)
        setCaret(end)
      }
    })
  }, [ghost, value, onChange])

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (!showGhost) return
      if (e.key === 'Tab') {
        e.preventDefault()
        acceptAll()
        return
      }
      // `→` accepts the next word only when the caret is at the ghost boundary
      // (end of value); otherwise it's a normal cursor move.
      if (e.key === 'ArrowRight' && caret >= value.length) {
        e.preventDefault()
        acceptWord()
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        clear()
        return
      }
      // Any other key dismisses (the onChange/onSelect that follows will
      // re-trigger a fresh suggestion if still at end-of-value).
      if (e.key.length === 1 || e.key === 'Backspace' || e.key === 'Enter') {
        clear()
      }
    },
    [showGhost, caret, value, acceptAll, acceptWord, clear],
  )

  const syncCaret = useCallback(() => {
    const el = ref.current
    if (el) setCaret(el.selectionEnd ?? el.value.length)
  }, [])

  const fieldClasses = cn(
    'relative z-10 w-full resize-none border bg-transparent px-3 py-2 text-sm outline-none rounded-none',
    'border-input focus-visible:border-ring',
    multiline ? 'min-h-[220px] font-mono' : '',
    className,
  )

  return (
    <div className="relative">
      {/* Mirror layer: transparent live text + muted ghost, behind the field.
          Hidden from AT (the field carries the value). */}
      {showGhost && (
        <div
          aria-hidden
          className={cn(
            'pointer-events-none absolute inset-0 z-0 overflow-hidden whitespace-pre-wrap break-words border border-transparent px-3 py-2 text-sm',
            multiline ? 'font-mono' : '',
          )}
        >
          <span className="text-transparent">{value}</span>
          <span className="text-muted-foreground/45">{ghost}</span>
        </div>
      )}
      {multiline ? (
        <textarea
          ref={ref as React.Ref<HTMLTextAreaElement>}
          id={id}
          className={fieldClasses}
          value={value}
          placeholder={placeholder}
          onChange={(e) => {
            onChange(e.target.value)
            setCaret(e.target.selectionEnd ?? e.target.value.length)
          }}
          onKeyDown={onKeyDown}
          onKeyUp={syncCaret}
          onClick={syncCaret}
          onSelect={syncCaret}
          onBlur={clear}
        />
      ) : (
        <input
          ref={ref as React.Ref<HTMLInputElement>}
          id={id}
          className={cn(fieldClasses, 'h-9')}
          value={value}
          placeholder={placeholder}
          onChange={(e) => {
            onChange(e.target.value)
            setCaret(e.target.selectionEnd ?? e.target.value.length)
          }}
          onKeyDown={onKeyDown}
          onKeyUp={syncCaret}
          onClick={syncCaret}
          onSelect={syncCaret}
          onBlur={clear}
        />
      )}
    </div>
  )
}
