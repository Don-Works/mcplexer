import { Plus, Trash2 } from 'lucide-react'
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
import type { AddonAuthKind, AddonEndpointSpec } from '@/api/client'

const HTTP_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'] as const

interface BasicsProps {
  name: string; setName: (v: string) => void
  description: string; setDescription: (v: string) => void
  baseURL: string; setBaseURL: (v: string) => void
  parentServer: string; setParentServer: (v: string) => void
  downstreams: { id: string; name: string }[]
  /**
   * When true, hide the parent-server Select entirely if no downstreams exist.
   * Defaults to false (legacy behaviour — show an empty Select). The compact
   * QuickSetup wizard variant passes `true` to keep the form tidy.
   */
  hideParentServerWhenEmpty?: boolean
  /**
   * Optional data-testid hooks for the name + base-URL inputs. Used by the
   * QuickSetup wizard to retain its e2e selectors after the dedupe.
   */
  testIds?: { name?: string; baseURL?: string }
}

export function BasicsStep(p: BasicsProps) {
  const showParentServer = !p.hideParentServerWhenEmpty || p.downstreams.length > 0
  return (
    <div className="space-y-4">
      <Field label="Name (namespace)">
        <Input
          value={p.name}
          onChange={(e) => p.setName(e.target.value)}
          placeholder="weatherco"
          data-testid={p.testIds?.name}
        />
      </Field>
      <Field label="Description">
        <Input value={p.description} onChange={(e) => p.setDescription(e.target.value)} placeholder="Public weather API" />
      </Field>
      <Field label="Base URL">
        <Input
          value={p.baseURL}
          onChange={(e) => p.setBaseURL(e.target.value)}
          placeholder="https://api.weather.co/v1"
          data-testid={p.testIds?.baseURL}
        />
      </Field>
      {showParentServer && (
        <Field label="Parent server (existing downstream)">
          <Select value={p.parentServer} onValueChange={p.setParentServer}>
            <SelectTrigger><SelectValue placeholder="Select existing server for auth" /></SelectTrigger>
            <SelectContent>
              {p.downstreams.map((ds) => (
                <SelectItem key={ds.id} value={ds.id}>{ds.name} ({ds.id})</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
    </div>
  )
}

interface AuthProps {
  authKind: AddonAuthKind; setAuthKind: (v: AddonAuthKind) => void
  headerName: string; setHeaderName: (v: string) => void
  queryName: string; setQueryName: (v: string) => void
  /**
   * Restrict which auth kinds are offered. Defaults to the full set used by
   * the standalone CreateMCP wizard. The QuickSetup compact form excludes
   * `oauth2` (configure now) since the inline form has no follow-up OAuth step.
   */
  availableAuthKinds?: ReadonlyArray<AddonAuthKind>
  /** Render the secret-handling explainer paragraph. Defaults to true. */
  showSecretNote?: boolean
}

const AUTH_KIND_LABELS: Record<AddonAuthKind, string> = {
  bearer: 'Bearer token',
  api_key_header: 'API key in header',
  api_key_query: 'API key in query',
  hawk: 'Hawk',
  oauth2: 'OAuth2 (configure now)',
  oauth2_pending: 'OAuth2 (configure later)',
  none: 'None',
}

const DEFAULT_AUTH_KINDS: ReadonlyArray<AddonAuthKind> = [
  'bearer', 'api_key_header', 'api_key_query', 'hawk', 'oauth2', 'oauth2_pending', 'none',
]

export function AuthStep(p: AuthProps) {
  const kinds = p.availableAuthKinds ?? DEFAULT_AUTH_KINDS
  const showNote = p.showSecretNote ?? true
  return (
    <div className="space-y-4">
      <Field label="Auth kind">
        <Select value={p.authKind} onValueChange={(v) => p.setAuthKind(v as AddonAuthKind)}>
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            {kinds.map((k) => (
              <SelectItem key={k} value={k}>{AUTH_KIND_LABELS[k]}</SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      {p.authKind === 'api_key_header' && (
        <Field label="Header name">
          <Input value={p.headerName} onChange={(e) => p.setHeaderName(e.target.value)} placeholder="X-Api-Key" />
        </Field>
      )}
      {p.authKind === 'api_key_query' && (
        <Field label="Query parameter name">
          <Input value={p.queryName} onChange={(e) => p.setQueryName(e.target.value)} placeholder="api_key" />
        </Field>
      )}
      {(p.authKind === 'oauth2' || p.authKind === 'oauth2_pending') && (
        <p className="text-xs text-muted-foreground">
          The next step (OAuth) will let you configure the auth/token URLs, scopes, and client credentials.
        </p>
      )}
      {showNote && (
        <p className="text-xs text-muted-foreground">
          The actual secret value comes from the parent server&apos;s auth scope at request time. Configure it under Credentials before using the addon.
        </p>
      )}
    </div>
  )
}

interface EndpointsProps {
  endpoints: AddonEndpointSpec[]
  setEndpoints: (v: AddonEndpointSpec[]) => void
}

// emptyEndpoint is exported separately so the page can seed initial state
// without re-importing every step component.
// eslint-disable-next-line react-refresh/only-export-components
export function emptyEndpoint(): AddonEndpointSpec {
  return { name: '', description: '', method: 'GET', path: '/', params: [] }
}

export function EndpointsStep({ endpoints, setEndpoints }: EndpointsProps) {
  function update(idx: number, patch: Partial<AddonEndpointSpec>) {
    setEndpoints(endpoints.map((ep, i) => i === idx ? { ...ep, ...patch } : ep))
  }
  return (
    <div className="space-y-4">
      {endpoints.map((ep, idx) => (
        <div key={idx} className="space-y-2 rounded border p-3">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium">Endpoint {idx + 1}</span>
            {endpoints.length > 1 && (
              <Button size="sm" variant="ghost" onClick={() => setEndpoints(endpoints.filter((_, i) => i !== idx))}>
                <Trash2 className="h-4 w-4" />
              </Button>
            )}
          </div>
          <div className="grid grid-cols-2 gap-2">
            <Input value={ep.name} onChange={(e) => update(idx, { name: e.target.value })} placeholder="get_forecast" />
            <Select value={ep.method} onValueChange={(v) => update(idx, { method: v as AddonEndpointSpec['method'] })}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>{HTTP_METHODS.map((m) => <SelectItem key={m} value={m}>{m}</SelectItem>)}</SelectContent>
            </Select>
          </div>
          <Input value={ep.path} onChange={(e) => update(idx, { path: e.target.value })} placeholder="/forecast/{{city}}" />
          <Input value={ep.description} onChange={(e) => update(idx, { description: e.target.value })} placeholder="Description" />
          <ParamsEditor params={ep.params ?? []} onChange={(params) => update(idx, { params })} />
        </div>
      ))}
      <Button size="sm" variant="outline" onClick={() => setEndpoints([...endpoints, emptyEndpoint()])}>
        <Plus className="mr-1 h-4 w-4" /> Add endpoint
      </Button>
    </div>
  )
}

interface ParamsEditorProps {
  params: NonNullable<AddonEndpointSpec['params']>
  onChange: (p: NonNullable<AddonEndpointSpec['params']>) => void
}

function ParamsEditor({ params, onChange }: ParamsEditorProps) {
  function update(idx: number, patch: Partial<NonNullable<AddonEndpointSpec['params']>[number]>) {
    onChange(params.map((p, i) => i === idx ? { ...p, ...patch } : p))
  }
  return (
    <div className="space-y-1 pl-2 text-xs">
      <div className="font-medium text-muted-foreground">Parameters</div>
      {params.map((p, idx) => (
        <div key={idx} className="grid grid-cols-12 items-center gap-1">
          <Input className="col-span-3" value={p.name} onChange={(e) => update(idx, { name: e.target.value })} placeholder="name" />
          <Select value={p.type} onValueChange={(v) => update(idx, { type: v as 'string' | 'integer' | 'number' | 'boolean' })}>
            <SelectTrigger className="col-span-3"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="string">string</SelectItem>
              <SelectItem value="integer">integer</SelectItem>
              <SelectItem value="number">number</SelectItem>
              <SelectItem value="boolean">boolean</SelectItem>
            </SelectContent>
          </Select>
          <Select value={p.in} onValueChange={(v) => update(idx, { in: v as 'path' | 'query' | 'body' })}>
            <SelectTrigger className="col-span-2"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="path">path</SelectItem>
              <SelectItem value="query">query</SelectItem>
              <SelectItem value="body">body</SelectItem>
            </SelectContent>
          </Select>
          <label className="col-span-3 flex items-center gap-1">
            <input type="checkbox" checked={!!p.required} onChange={(e) => update(idx, { required: e.target.checked })} />
            required
          </label>
          <Button size="sm" variant="ghost" className="col-span-1" onClick={() => onChange(params.filter((_, i) => i !== idx))}>
            <Trash2 className="h-3 w-3" />
          </Button>
        </div>
      ))}
      <Button size="sm" variant="outline" onClick={() => onChange([...params, { name: '', type: 'string', in: 'query' }])}>
        <Plus className="mr-1 h-3 w-3" /> Param
      </Button>
    </div>
  )
}

export function ReviewStep({ yaml }: { yaml: string }) {
  return (
    <div className="space-y-2">
      <Label>Generated addon YAML</Label>
      <pre className="max-h-96 overflow-auto rounded border bg-muted p-3 text-xs">{yaml || 'building preview...'}</pre>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      {children}
    </div>
  )
}
