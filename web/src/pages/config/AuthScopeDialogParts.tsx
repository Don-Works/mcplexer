import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Trash2 } from 'lucide-react'
import type { AuthScopeFormData } from './AuthScopeDialog'

export type HeaderMode = 'bearer' | 'apiKey' | 'custom'

export const credentialTypeOptions: Array<{
  value: AuthScopeFormData['type']
  label: string
  description: string
}> = [
  {
    value: 'env',
    label: 'Environment Variables',
    description: 'Inject encrypted values into stdio servers as env vars.',
  },
  {
    value: 'header',
    label: 'HTTP Headers',
    description: 'Attach encrypted headers like Authorization or X-API-Key.',
  },
  {
    value: 'hawk',
    label: 'Hawk',
    description: 'Sign HTTP requests with HAWK_ID and HAWK_KEY.',
  },
  {
    value: 'oauth2',
    label: 'OAuth 2.0',
    description: 'Store a provider and authenticate with an OAuth flow.',
  },
]

export const headerModeOptions: Array<{
  value: HeaderMode
  label: string
  description: string
}> = [
  {
    value: 'bearer',
    label: 'Authorization Bearer',
    description: 'The common Authorization: Bearer <token> pattern.',
  },
  {
    value: 'apiKey',
    label: 'API Key Header',
    description: 'A named header such as X-API-Key: <token>.',
  },
  {
    value: 'custom',
    label: 'Custom Header',
    description: 'Any header name, with an optional value prefix.',
  },
]

export function maskSecret(rawValue: string): string {
  if (!rawValue.trim()) return '••••••'
  return '•'.repeat(Math.max(6, Math.min(rawValue.trim().length, 12)))
}

export function buildHeaderSecret(
  mode: HeaderMode,
  headerName: string,
  rawValue: string,
  prefix: string,
) {
  const trimmedValue = rawValue.trim()
  const resolvedKey = mode === 'bearer' ? 'Authorization' : headerName.trim()
  const resolvedPrefix = mode === 'bearer' ? 'Bearer ' : mode === 'custom' ? prefix : ''
  const resolvedValue = trimmedValue ? `${resolvedPrefix}${trimmedValue}` : ''
  const previewValue = trimmedValue ? `${resolvedPrefix}${maskSecret(trimmedValue)}` : ''

  return {
    key: resolvedKey,
    value: resolvedValue,
    preview: resolvedKey && previewValue ? `${resolvedKey}: ${previewValue}` : '',
  }
}

export function SectionHeading({
  step,
  title,
  description,
}: {
  step: string
  title: string
  description: string
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-2">
        <Badge variant="outline" className="text-[10px] font-mono">
          step {step}
        </Badge>
        <h3 className="text-sm font-semibold">{title}</h3>
      </div>
      <p className="text-xs text-muted-foreground">{description}</p>
    </div>
  )
}

export function StoredSecretList({
  keys,
  onDelete,
}: {
  keys: string[]
  onDelete: (key: string) => void
}) {
  if (keys.length === 0) {
    return (
      <div className="rounded-md border border-dashed border-border/60 bg-muted/20 px-4 py-3 text-sm text-muted-foreground">
        Nothing stored yet.
      </div>
    )
  }

  return (
    <div className="space-y-1.5">
      {keys.map((key) => (
        <div
          key={key}
          className="flex items-center justify-between rounded-md border border-border/50 px-3 py-2"
        >
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate font-mono text-sm">{key}</span>
            <Badge
              variant="outline"
              className="shrink-0 text-[10px] text-emerald-600 border-emerald-500/30"
            >
              stored
            </Badge>
          </div>
          <Button
            variant="ghost"
            size="sm"
            className="h-6 w-6 p-0 hover:bg-destructive/10 hover:text-destructive"
            onClick={() => onDelete(key)}
          >
            <Trash2 className="h-3 w-3" />
          </Button>
        </div>
      ))}
    </div>
  )
}
