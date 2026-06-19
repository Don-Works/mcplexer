// WorkerSkillRefsField (M0.7) — multi-skill picker for the Worker
// editor. Replaces the single skill_name autocomplete; the runner now
// loads each ref's body in order and joins them with a markdown
// separator before sending as the system prompt.
//
// The picker is intentionally simple — one row per ref, autocomplete
// the name against the skill registry, free-text version, plus
// up/down reorder + remove buttons. Drag-to-reorder was considered but
// punted for now (the typical worker carries 1–3 skills so the
// up/down buttons are fine).
//
// Empty refs render an explanatory placeholder so the operator knows
// the worker will run against the model alone.

import { useId, useMemo } from 'react'
import { ArrowDown, ArrowUp, Plus, X } from 'lucide-react'

import type { SkillRef } from '@/api/workers'
import type { SkillRegistryEntry } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

interface WorkerSkillRefsFieldProps {
  refs: SkillRef[]
  onChange: (next: SkillRef[]) => void
  skills: SkillRegistryEntry[] | null
}

export function WorkerSkillRefsField({
  refs,
  onChange,
  skills,
}: WorkerSkillRefsFieldProps) {
  const datalistId = useId()
  const uniqueSkills = useMemo(() => dedupSkillsByName(skills), [skills])

  function update(idx: number, patch: Partial<SkillRef>): void {
    const next = refs.map((r, i) => (i === idx ? { ...r, ...patch } : r))
    onChange(next)
  }

  function remove(idx: number): void {
    onChange(refs.filter((_, i) => i !== idx))
  }

  function move(idx: number, delta: -1 | 1): void {
    const target = idx + delta
    if (target < 0 || target >= refs.length) return
    const next = refs.slice()
    ;[next[idx], next[target]] = [next[target], next[idx]]
    onChange(next)
  }

  function add(): void {
    onChange([...refs, { name: '', version: '' }])
  }

  if (refs.length === 0) {
    return (
      <div className="space-y-2">
        <p className="text-xs text-muted-foreground">
          No skills selected — the worker will run against the model alone.
        </p>
        <Button type="button" variant="secondary" size="sm" onClick={add}>
          <Plus className="mr-1 h-3.5 w-3.5" /> Add skill
        </Button>
        <datalist id={datalistId}>
          {uniqueSkills.map((s) => (
            <option key={s.name} value={s.name}>
              {s.description ? `${s.name} — ${s.description}` : s.name}
            </option>
          ))}
        </datalist>
      </div>
    )
  }

  return (
    <div className="space-y-2">
      <p className="text-xs text-muted-foreground">
        Skills are joined in order (top → bottom) and prepended to the rendered
        prompt with a markdown separator.
      </p>
      <ol className="space-y-2">
        {refs.map((ref, idx) => (
          <li
            key={idx}
            className="flex items-center gap-2 rounded-md border bg-card/40 px-2 py-1.5"
          >
            <span className="w-5 text-center font-mono text-[10px] text-muted-foreground">
              {idx + 1}
            </span>
            <Input
              value={ref.name}
              onChange={(e) => update(idx, { name: e.target.value })}
              placeholder="skill-name"
              list={datalistId}
              autoComplete="off"
              className="flex-1 font-mono text-xs"
              aria-label={`Skill ${idx + 1} name`}
            />
            <Input
              value={ref.version ?? ''}
              onChange={(e) => update(idx, { version: e.target.value })}
              placeholder="latest"
              className="w-24 font-mono text-xs"
              aria-label={`Skill ${idx + 1} version`}
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => move(idx, -1)}
              disabled={idx === 0}
              aria-label="Move up"
            >
              <ArrowUp className="h-3.5 w-3.5" />
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => move(idx, 1)}
              disabled={idx === refs.length - 1}
              aria-label="Move down"
            >
              <ArrowDown className="h-3.5 w-3.5" />
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => remove(idx)}
              aria-label="Remove"
            >
              <X className="h-3.5 w-3.5" />
            </Button>
          </li>
        ))}
      </ol>
      <Button type="button" variant="secondary" size="sm" onClick={add}>
        <Plus className="mr-1 h-3.5 w-3.5" /> Add skill
      </Button>
      <datalist id={datalistId}>
        {uniqueSkills.map((s) => (
          <option key={s.name} value={s.name}>
            {s.description ? `${s.name} — ${s.description}` : s.name}
          </option>
        ))}
      </datalist>
    </div>
  )
}

// dedupSkillsByName collapses the registry's per-version rows into one
// suggestion per name. The autocomplete dropdown should show each skill
// once; the user picks a version in the adjacent input.
function dedupSkillsByName(
  skills: SkillRegistryEntry[] | null,
): SkillRegistryEntry[] {
  if (!skills) return []
  const seen = new Set<string>()
  const out: SkillRegistryEntry[] = []
  for (const s of skills) {
    if (seen.has(s.name)) continue
    seen.add(s.name)
    out.push(s)
  }
  return out
}
