import { useState } from 'react'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { FileText, Info } from 'lucide-react'

// FileTruthDisclosure is the calm footer line that never hides the fact that a
// record is also a file the agent reads verbatim (DESIGN §3.3). Collapsed it is
// a single mono line reading as reassurance, not a code panel. The "open as
// file" affordance reveals the literal serialized .md in a Sheet — the
// technical escape hatch where git implicitly lives but is never named for the
// non-technical user.
interface Props {
  // path is the canonical .md file path (mono, the agent's address for it).
  path?: string
  // raw is the verbatim serialized .md (populated by the detail read).
  raw?: string
  // savedHint is the trailing micro-status (e.g. "saved · indexed"); optional.
  savedHint?: string
}

export function FileTruthDisclosure({ path, raw, savedHint }: Props) {
  const [open, setOpen] = useState(false)
  if (!path) return null

  return (
    <>
      <div className="flex items-center gap-2 border-t border-border pt-2 font-mono text-xs text-muted-foreground">
        <Info className="h-3 w-3 shrink-0" aria-hidden />
        <span className="truncate">this is exactly what your agent reads</span>
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="text-primary hover:underline"
        >
          open as file
        </button>
        {savedHint && <span className="ml-auto text-muted-foreground/70">{savedHint}</span>}
      </div>

      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="right" className="w-[640px] max-w-[90vw] rounded-none sm:max-w-[640px]">
          <SheetHeader>
            <SheetTitle className="flex items-center gap-2 font-mono text-xs">
              <FileText className="h-3.5 w-3.5" aria-hidden /> {path}
            </SheetTitle>
          </SheetHeader>
          <p className="px-1 pt-2 text-xs text-muted-foreground">
            this is exactly what your agent reads
          </p>
          <pre className="mt-2 h-[calc(100%-4rem)] overflow-auto whitespace-pre-wrap border border-border bg-muted/30 p-2 font-mono text-xs">
            {raw || '(raw .md not loaded; open from the record list)'}
          </pre>
        </SheetContent>
      </Sheet>
    </>
  )
}
