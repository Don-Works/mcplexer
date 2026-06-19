import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Loader2 } from 'lucide-react'
import { toast } from 'sonner'
import { publishSkillRegistry } from '@/api/client'

interface Props {
  open: boolean
  onOpenChange: (b: boolean) => void
  onPublished: () => void
  serverMode?: boolean
}

export function PublishDialog({ open, onOpenChange, onPublished, serverMode = false }: Props) {
  const [name, setName] = useState('')
  const [body, setBody] = useState(
    '---\nname: my-skill\ndescription: Use when the user wants ...\n---\n\n# My skill\n',
  )
  const [busy, setBusy] = useState(false)

  async function submit() {
    if (!name.trim() || !body.trim()) return
    setBusy(true)
    try {
      const res = await publishSkillRegistry({ name: name.trim(), body })
      if (res.action === 'deduped') {
        toast.info(`No change — content matched existing v${res.version}.`)
      } else {
        toast.success(`Published ${res.name}@${res.version}`)
      }
      onPublished()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Publish failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{serverMode ? 'New global skill' : 'New skill'}</DialogTitle>
        </DialogHeader>
        <div className="space-y-3">
          <Field label="Name">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-skill (must match the frontmatter name)"
            />
          </Field>
          <Field label="SKILL.md">
            <Textarea
              value={body}
              onChange={(e) => setBody(e.target.value)}
              className="min-h-[280px] text-[12.5px] leading-relaxed"
            />
          </Field>
          <p className="text-[11px] text-muted-foreground">
            {serverMode
              ? 'This writes to the central repository only after you press Publish.'
              : (
                <>
                  Lead the description with <span className="text-foreground/80">"Use when…"</span>. That
                  phrase is the retrieval key when agents ask the registry by intent.
                </>
              )}
          </p>
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={busy}>
            {busy ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
            Publish
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <label className="text-[11px] font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </label>
      {children}
    </div>
  )
}
