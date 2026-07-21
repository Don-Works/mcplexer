import { useState, type KeyboardEvent } from 'react'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { X } from 'lucide-react'

interface Props {
  tags: string[]
  onChange: (tags: string[]) => void
}

// TagInput is a minimal chip-style tag editor: type a tag, press Enter or
// comma to commit, click the x to remove. Used by the brain record editor
// for the task/memory `tags` frontmatter list.
export function TagInput({ tags, onChange }: Props) {
  const [draft, setDraft] = useState('')

  function commit() {
    const t = draft.trim().replace(/,$/, '').trim()
    if (t && !tags.includes(t)) {
      onChange([...tags, t])
    }
    setDraft('')
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault()
      commit()
    } else if (e.key === 'Backspace' && draft === '' && tags.length > 0) {
      onChange(tags.slice(0, -1))
    }
  }

  return (
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
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={onKeyDown}
        onBlur={commit}
        placeholder={tags.length === 0 ? 'add tags…' : ''}
        className="h-6 flex-1 border-0 px-1 shadow-none focus-visible:ring-0"
      />
    </div>
  )
}
