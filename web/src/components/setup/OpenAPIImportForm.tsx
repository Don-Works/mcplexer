/**
 * OpenAPIImportForm — inline wizard step that accepts a URL or raw YAML/JSON
 * OpenAPI spec and creates a Custom MCP addon via importAddonOpenAPI +
 * createAddon.  Used inside QuickSetupPage.
 */
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Loader2, ArrowRight, FileCode } from 'lucide-react'
import { importAddonOpenAPI, createAddon } from '@/api/client'
import type { AddonSpec } from '@/api/client'

interface Props {
  /** Called with the server name once the addon has been created server-side. */
  onCreated: (serverName: string) => void
}

export function OpenAPIImportForm({ onCreated }: Props) {
  const [specUrl, setSpecUrl] = useState('')
  const [specInline, setSpecInline] = useState('')
  const [useInline, setUseInline] = useState(false)
  const [importing, setImporting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [preview, setPreview] = useState<AddonSpec | null>(null)
  const [creating, setCreating] = useState(false)

  async function handleImport() {
    setImporting(true)
    setError(null)
    setPreview(null)
    try {
      const spec = await importAddonOpenAPI({
        spec_url: useInline ? undefined : specUrl.trim() || undefined,
        spec_inline: useInline ? specInline.trim() || undefined : undefined,
      })
      setPreview(spec)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Import failed')
    } finally {
      setImporting(false)
    }
  }

  async function handleCreate() {
    if (!preview) return
    setCreating(true)
    setError(null)
    try {
      const res = await createAddon(preview)
      onCreated(res.name)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Create failed')
    } finally {
      setCreating(false)
    }
  }

  const hasInput = useInline ? specInline.trim().length > 0 : specUrl.trim().length > 0

  return (
    <div className="mx-auto max-w-md space-y-4">
      <div className="flex items-center gap-4">
        <button
          type="button"
          className={`text-xs ${!useInline ? 'font-semibold text-primary' : 'text-muted-foreground hover:text-foreground'}`}
          onClick={() => setUseInline(false)}
        >
          From URL
        </button>
        <button
          type="button"
          className={`text-xs ${useInline ? 'font-semibold text-primary' : 'text-muted-foreground hover:text-foreground'}`}
          onClick={() => setUseInline(true)}
        >
          Paste spec
        </button>
      </div>

      {!useInline ? (
        <div className="space-y-1.5">
          <Label className="text-xs text-muted-foreground">OpenAPI Spec URL</Label>
          <Input
            value={specUrl}
            onChange={(e) => setSpecUrl(e.target.value)}
            placeholder="https://api.example.com/openapi.json"
            data-testid="openapi-url"
          />
        </div>
      ) : (
        <div className="space-y-1.5">
          <Label className="text-xs text-muted-foreground">OpenAPI YAML / JSON</Label>
          <textarea
            className="min-h-[140px] w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-[11px] leading-relaxed placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring"
            value={specInline}
            onChange={(e) => setSpecInline(e.target.value)}
            placeholder="openapi: 3.0.0&#10;info:&#10;  title: My API&#10;  ..."
            data-testid="openapi-inline"
          />
        </div>
      )}

      {error && (
        <p className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
          {error}
        </p>
      )}

      {preview && (
        <div className="rounded-md border border-border bg-muted/30 p-3 space-y-1">
          <div className="flex items-center gap-2">
            <FileCode className="h-4 w-4 text-muted-foreground" />
            <span className="text-sm font-medium">{preview.name || 'Unnamed'}</span>
          </div>
          {preview.description && (
            <p className="text-xs text-muted-foreground">{preview.description}</p>
          )}
          <p className="text-xs text-muted-foreground/70">
            {preview.endpoints?.length ?? 0} endpoint(s) detected
          </p>
        </div>
      )}

      <div className="flex justify-end gap-2">
        {!preview ? (
          <Button
            onClick={handleImport}
            disabled={importing || !hasInput}
            data-testid="openapi-import"
          >
            {importing ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : null}
            Import
          </Button>
        ) : (
          <Button
            onClick={handleCreate}
            disabled={creating}
            data-testid="openapi-create"
          >
            {creating ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <ArrowRight className="mr-2 h-4 w-4" />
            )}
            Create &amp; Continue
          </Button>
        )}
      </div>
    </div>
  )
}
