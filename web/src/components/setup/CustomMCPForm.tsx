/**
 * CustomMCPForm — lightweight inline wizard step for creating a Custom MCP
 * addon (HTTP-based, hand-defined endpoints) from inside the QuickSetupPage
 * wizard.  Collects the same basics + auth fields as the full CreateMCPPage,
 * but in a single compact form so the user stays in the wizard flow.
 *
 * The field definitions are NOT duplicated here — both surfaces consume the
 * shared `BasicsStep` + `AuthStep` from `@/components/create-mcp/Steps`. This
 * form is just the thin compact shell + a submit handler.
 *
 * On success it calls `onCreated(serverName)` with the new addon's name so
 * the parent can advance to the workspace step.
 */
import { useCallback, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Loader2, ArrowRight } from 'lucide-react'
import { createAddon, listDownstreams } from '@/api/client'
import type { AddonAuthKind, AddonAuthSpec } from '@/api/client'
import { useApi } from '@/hooks/use-api'
import { toast } from 'sonner'
import { AuthStep, BasicsStep } from '@/components/create-mcp/Steps'

interface Props {
  onCreated: (serverName: string) => void
}

// The compact form omits `oauth2` (configure now) because there is no
// follow-up OAuth wizard step here — picking it would lead nowhere. Users
// who need real OAuth go through the standalone /create-mcp wizard.
const COMPACT_AUTH_KINDS: ReadonlyArray<AddonAuthKind> = [
  'bearer', 'api_key_header', 'api_key_query', 'hawk', 'oauth2_pending', 'none',
]

export function CustomMCPForm({ onCreated }: Props) {
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [parentServer, setParentServer] = useState('')
  const [authKind, setAuthKind] = useState<AddonAuthKind>('bearer')
  const [headerName, setHeaderName] = useState('Authorization')
  const [queryName, setQueryName] = useState('api_key')
  const [submitting, setSubmitting] = useState(false)

  const dsFetcher = useCallback(() => listDownstreams(), [])
  const { data: downstreams } = useApi(dsFetcher)

  async function handleCreate() {
    setSubmitting(true)
    try {
      const authSpec: AddonAuthSpec = (() => {
        switch (authKind) {
          case 'api_key_header': return { kind: authKind, header_name: headerName }
          case 'api_key_query': return { kind: authKind, query_name: queryName }
          default: return { kind: authKind }
        }
      })()
      const res = await createAddon({
        name: name.trim(),
        description: description.trim(),
        base_url: baseURL.trim(),
        parent_server: parentServer.trim(),
        auth: authSpec,
        endpoints: [],
      })
      toast.success(`Created ${res.name}`)
      onCreated(res.name)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Create failed')
    } finally {
      setSubmitting(false)
    }
  }

  const valid = name.trim().length > 0 && baseURL.trim().length > 0

  return (
    <div className="mx-auto max-w-md space-y-4">
      <BasicsStep
        name={name} setName={setName}
        description={description} setDescription={setDescription}
        baseURL={baseURL} setBaseURL={setBaseURL}
        parentServer={parentServer} setParentServer={setParentServer}
        downstreams={downstreams ?? []}
        hideParentServerWhenEmpty
        testIds={{ name: 'custom-mcp-name', baseURL: 'custom-mcp-url' }}
      />
      <AuthStep
        authKind={authKind} setAuthKind={setAuthKind}
        headerName={headerName} setHeaderName={setHeaderName}
        queryName={queryName} setQueryName={setQueryName}
        availableAuthKinds={COMPACT_AUTH_KINDS}
        showSecretNote={false}
      />
      <p className="text-xs text-muted-foreground/60">
        You can add endpoints and configure credentials after creation.
      </p>
      <div className="flex justify-end">
        <Button
          onClick={handleCreate}
          disabled={!valid || submitting}
          data-testid="custom-mcp-create"
        >
          {submitting ? (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          ) : (
            <ArrowRight className="mr-2 h-4 w-4" />
          )}
          Create &amp; Continue
        </Button>
      </div>
    </div>
  )
}
