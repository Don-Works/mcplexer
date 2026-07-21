import { useMemo, useState } from 'react'
import { Loader2, Play } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  previewAddonCall,
  type AddonPreviewCallResponse,
  type AddonSpec,
} from '@/api/client'

export interface TestStepProps {
  spec: AddonSpec
  authScopeID?: string
}

// TestStep is the test/preview pane: pick an endpoint, fill params, fire one
// HTTP call, and view the redacted request + response side by side. Nothing
// is persisted — this is purely a UI-side validation step.
export function TestStep({ spec, authScopeID }: TestStepProps) {
  const [endpointName, setEndpointName] = useState<string>(spec.endpoints[0]?.name ?? '')
  const endpoint = useMemo(
    () => spec.endpoints.find((e) => e.name === endpointName),
    [spec.endpoints, endpointName],
  )
  const [args, setArgs] = useState<Record<string, string>>({})
  const [result, setResult] = useState<AddonPreviewCallResponse | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  function setArg(name: string, value: string) {
    setArgs((prev) => ({ ...prev, [name]: value }))
  }

  async function runCall() {
    if (!endpoint) return
    setBusy(true); setErr(null)
    try {
      const typed: Record<string, unknown> = {}
      for (const p of endpoint.params ?? []) {
        const raw = args[p.name]
        if (raw === undefined || raw === '') continue
        typed[p.name] = coerceParam(raw, p.type)
      }
      const res = await previewAddonCall({
        spec, endpoint: endpointName, args: typed,
        auth_scope_id: authScopeID,
      })
      setResult(res)
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'preview call failed')
    } finally {
      setBusy(false)
    }
  }

  if (spec.endpoints.length === 0) {
    return <p className="text-sm text-muted-foreground">Add at least one endpoint to test.</p>
  }

  return (
    <div className="space-y-4">
      <div className="rounded border border-amber-500/40 bg-amber-500/10 p-3 text-xs">
        Test responses are not saved. Sensitive headers (Authorization, Cookie, X-API-Key) and JSON
        keys containing token/secret/password are redacted before display.
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Endpoint">
          <Select value={endpointName} onValueChange={setEndpointName}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              {spec.endpoints.map((e) => (
                <SelectItem key={e.name} value={e.name}>
                  {e.method} {e.name} ({e.path})
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <div className="flex items-end justify-end">
          <Button onClick={runCall} disabled={busy || !endpoint}>
            {busy ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : <Play className="mr-1 h-4 w-4" />}
            Try it
          </Button>
        </div>
      </div>

      {endpoint && (
        <div className="space-y-2 rounded border p-3">
          <div className="text-xs text-muted-foreground">
            {endpoint.method} {spec.base_url}{endpoint.path}
          </div>
          <div className="space-y-2">
            {(endpoint.params ?? []).map((p) => (
              <div key={p.name} className="grid grid-cols-12 items-center gap-2 text-xs">
                <Label className="col-span-3">{p.name}{p.required && <span className="text-red-500">*</span>}</Label>
                <span className="col-span-2 text-muted-foreground">{p.type} / {p.in}</span>
                <Input
                  className="col-span-7"
                  value={args[p.name] ?? ''}
                  onChange={(e) => setArg(p.name, e.target.value)}
                  placeholder={p.description}
                />
              </div>
            ))}
            {(endpoint.params ?? []).length === 0 && (
              <p className="text-xs text-muted-foreground">This endpoint takes no parameters.</p>
            )}
          </div>
        </div>
      )}

      {err && <p className="text-sm text-red-500">{err}</p>}
      {result && <PreviewView result={result} />}
    </div>
  )
}

function PreviewView({ result }: { result: AddonPreviewCallResponse }) {
  return (
    <div className="grid grid-cols-2 gap-3 text-xs">
      <div className="space-y-1">
        <div className="font-semibold">Request</div>
        <pre className="max-h-72 overflow-auto rounded border bg-muted p-2">
{result.request.method} {result.request.url}
{formatHeaders(result.request.headers)}
{result.request.body ? '\n\n' + result.request.body : ''}
        </pre>
      </div>
      <div className="space-y-1">
        <div className="font-semibold">Response — HTTP {result.status}</div>
        <pre className="max-h-72 overflow-auto rounded border bg-muted p-2">
{formatHeaders(result.response.headers)}
{result.response.body ? '\n\n' + result.response.body : ''}
        </pre>
      </div>
    </div>
  )
}

function formatHeaders(h: Record<string, string>): string {
  return Object.entries(h)
    .map(([k, v]) => `${k}: ${v}`)
    .join('\n')
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      {children}
    </div>
  )
}

// coerceParam turns the raw string from an Input into the typed value the
// API expects. Falls back to the string when parsing fails (the backend
// will surface a typed error).
function coerceParam(raw: string, type: string): unknown {
  switch (type) {
    case 'integer': {
      const n = parseInt(raw, 10)
      return Number.isNaN(n) ? raw : n
    }
    case 'number': {
      const n = parseFloat(raw)
      return Number.isNaN(n) ? raw : n
    }
    case 'boolean':
      return raw === 'true' || raw === '1'
    default:
      return raw
  }
}
