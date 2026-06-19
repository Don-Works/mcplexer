import { useId, useRef, useState, type KeyboardEvent } from 'react'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { X } from 'lucide-react'
import { IndexTypeahead } from './IndexTypeahead'
import type { TypeaheadOption } from './typeaheadRank'

// TagTokenInput is the #tag token field (DESIGN §3.2 + §4.1) wired to the
// shared IndexTypeahead: typing opens the same dropdown grammar as [[refs]],
// mining existing tags out of the live index so the operator reuses tags
// instead of forking near-duplicates. The always-last create-on-miss row
// commits a brand-new tag (no server write — tags are inline strings, not
// records). Mono #tag chips, zero radius — the tag is machine output.
interface Props {
  tags: string[]
  onChange: (tags: string[]) => void
  workspace?: string
  // ariaLabelledBy points the combobox input at the owning Field's <Label> id
  // so a screen reader announces the field name on focus (DESIGN §7).
  ariaLabelledBy?: string
}

export function TagTokenInput({ tags, onChange, workspace, ariaLabelledBy }: Props) {
  const [draft, setDraft] = useState('')
  const [open, setOpen] = useState(false)
  const [activeDesc, setActiveDesc] = useState('')
  const listboxId = useId()
  const inputRef = useRef<HTMLInputElement | null>(null)

  function add(raw: string) {
    const t = raw.trim().replace(/^#/, '').replace(/,$/, '').trim()
    if (t && !tags.includes(t)) onChange([...tags, t])
    setDraft('')
    setOpen(false)
  }

  function select(opt: TypeaheadOption) {
    // Both a real tag hit and the create-on-miss row commit a bare tag string.
    add(opt.id)
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === ',') {
      e.preventDefault()
      add(draft)
    } else if (e.key === 'Backspace' && draft === '' && tags.length > 0) {
      onChange(tags.slice(0, -1))
    }
  }

  return (
    <div className="relative">
      <div className="flex flex-wrap items-center gap-1.5 border border-input p-1.5">
        {tags.map((t) => (
          <Badge key={t} tone="mono" className="gap-1 text-[10px] lowercase tracking-normal">
            #{t}
            <button
              type="button"
              onClick={() => onChange(tags.filter((x) => x !== t))}
              className="hover:text-destructive"
              aria-label={`Remove tag ${t}`}
            >
              <X className="h-3 w-3" />
            </button>
          </Badge>
        ))}
        <Input
          ref={inputRef}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value)
            setOpen(e.target.value.trim() !== '')
          }}
          onKeyDown={onKeyDown}
          onFocus={() => setOpen(draft.trim() !== '')}
          onBlur={() => window.setTimeout(() => setOpen(false), 120)}
          role="combobox"
          aria-expanded={open}
          aria-autocomplete="list"
          aria-controls={open ? listboxId : undefined}
          aria-activedescendant={open && activeDesc ? activeDesc : undefined}
          aria-labelledby={ariaLabelledBy}
          placeholder={tags.length === 0 ? 'add tags… #' : ''}
          className="h-6 flex-1 border-0 px-1 font-mono text-xs shadow-none focus-visible:ring-0"
        />
      </div>
      <IndexTypeahead
        open={open}
        query={draft.replace(/^#/, '')}
        mode="tag"
        workspace={workspace}
        listboxId={listboxId}
        onActiveDescendant={setActiveDesc}
        onSelect={select}
        onClose={() => setOpen(false)}
      />
    </div>
  )
}
