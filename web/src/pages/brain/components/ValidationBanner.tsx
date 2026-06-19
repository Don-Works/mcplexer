import { AlertTriangle } from 'lucide-react'
import { Button } from '@/components/ui/button'

// ValidationBanner is the standing index-rejection banner (DESIGN §3.7): the
// honest consequence in the agent's terms ("your agent cannot see this record
// yet"), the offending field named in human language, and a one-click fix that
// snaps the field to the first allowed vocab value. It is rendered at the top
// of the editor (not a toast), and the matching inline error renders again at
// the offending control via the FrontmatterForm Field wrapper.
//
// `field` + `allowed` come from the structured 422 payload the Go handler
// returns; `onFix` applies the suggested vocab value to the live draft.
interface Props {
  // message is the human-readable reason (never a raw YAML parse trace).
  message: string
  // field names the offending frontmatter field (e.g. "status"), if known.
  field?: string
  // allowed is the workspace vocab the field must be one of, if applicable.
  allowed?: string[]
  // onFix snaps the offending field to `value` (the first allowed vocab value).
  onFix?: (field: string, value: string) => void
}

export function ValidationBanner({ message, field, allowed, onFix }: Props) {
  const fixValue = allowed && allowed.length > 0 ? allowed[0] : undefined
  const canFix = Boolean(field && fixValue && onFix)

  return (
    <div className="flex items-start gap-2 border border-red-500/40 bg-red-500/10 p-2 text-sm">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-300" aria-hidden />
      <div className="min-w-0 flex-1">
        <div className="font-medium text-red-300">
          not indexed: your agent cannot see this record yet
        </div>
        <div className="text-xs text-muted-foreground">{message}</div>
        {allowed && allowed.length > 0 && (
          <div className="mt-0.5 font-mono text-[11px] text-muted-foreground/80">
            allowed: {allowed.join(' · ')}
          </div>
        )}
      </div>
      {canFix && (
        <Button
          size="sm"
          variant="outline"
          className="h-7 shrink-0 rounded-none font-mono text-xs"
          onClick={() => onFix!(field!, fixValue!)}
        >
          fix &rarr; set "{fixValue}"
        </Button>
      )}
    </div>
  )
}
