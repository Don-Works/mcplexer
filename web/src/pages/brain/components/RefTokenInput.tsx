import { useId, useRef, useState, type KeyboardEvent } from 'react'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { X } from 'lucide-react'
import { toast } from 'sonner'
import { createBrainStub } from '@/api/brainBrowser'
import { IndexTypeahead } from './IndexTypeahead'
import type { TypeaheadOption } from './typeaheadRank'

// RefTokenInput is the [[ref]] / composes token field (DESIGN §3.2 + §4.1):
// each child-record id renders as a mono [[id]] chip; typing opens the shared
// IndexTypeahead over the live index. Selecting a hit inserts its id; selecting
// the always-last create-on-miss row creates a REAL stub .md (generated ULID)
// and inserts the [[ref]] immediately, so referencing a not-yet-existing record
// never breaks the writing flow. Zero-radius, mono — the id is machine output.
interface Props {
  refs: string[]
  onChange: (refs: string[]) => void
  placeholder?: string
  // workspace scopes the typeahead + any create-on-miss stub to the active ws.
  workspace?: string
  // ariaLabelledBy points the combobox input at the owning Field's <Label> id
  // so a screen reader announces the field name on focus (DESIGN §7).
  ariaLabelledBy?: string
}

export function RefTokenInput({ refs, onChange, placeholder, workspace, ariaLabelledBy }: Props) {
  const [draft, setDraft] = useState('')
  const [open, setOpen] = useState(false)
  const [activeDesc, setActiveDesc] = useState('')
  const listboxId = useId()
  const inputRef = useRef<HTMLInputElement | null>(null)

  function add(id: string) {
    const t = id.trim().replace(/^\[\[/, '').replace(/\]\]$/, '').trim()
    if (t && !refs.includes(t)) onChange([...refs, t])
    setDraft('')
    setOpen(false)
  }

  async function select(opt: TypeaheadOption) {
    if (opt.create) {
      try {
        const stub = await createBrainStub('task', opt.id, workspace ?? '')
        if (stub.id) {
          add(stub.id)
          toast.success(`Created stub "${opt.id}" and linked it.`)
        }
      } catch {
        toast.error('Could not create the linked record.')
      }
      return
    }
    add(opt.id)
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    // The dropdown owns ArrowUp/Down/Enter/Escape while open; here we only
    // handle the no-dropdown commit (comma) + chip-delete backspace.
    if (e.key === ',' ) {
      e.preventDefault()
      add(draft)
    } else if (e.key === 'Backspace' && draft === '' && refs.length > 0) {
      onChange(refs.slice(0, -1))
    }
  }

  return (
    <div className="relative">
      <div className="flex flex-wrap items-center gap-1.5 border border-input p-1.5">
        {refs.map((r) => (
          <Badge key={r} tone="mono" className="gap-1 text-[10px] lowercase tracking-normal">
            [[{r}]]
            <button
              type="button"
              onClick={() => onChange(refs.filter((x) => x !== r))}
              className="hover:text-destructive"
              aria-label={`Remove ref ${r}`}
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
          onBlur={() => {
            // Defer so a dropdown mousedown-select wins the race.
            window.setTimeout(() => setOpen(false), 120)
          }}
          role="combobox"
          aria-expanded={open}
          aria-autocomplete="list"
          aria-controls={open ? listboxId : undefined}
          aria-activedescendant={open && activeDesc ? activeDesc : undefined}
          aria-labelledby={ariaLabelledBy}
          placeholder={refs.length === 0 ? (placeholder ?? 'link a record… [[') : ''}
          className="h-6 flex-1 border-0 px-1 font-mono text-xs shadow-none focus-visible:ring-0"
        />
      </div>
      <IndexTypeahead
        open={open}
        query={draft.replace(/^\[\[/, '')}
        mode="ref"
        workspace={workspace}
        listboxId={listboxId}
        onActiveDescendant={setActiveDesc}
        onSelect={select}
        onClose={() => setOpen(false)}
      />
    </div>
  )
}
