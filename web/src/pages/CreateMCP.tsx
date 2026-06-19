import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { ArrowLeft, ArrowRight, CheckCircle2, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { StepIndicator, type StepDef } from '@/components/ui/step-indicator'
import { useApi } from '@/hooks/use-api'
import {
  createAddon,
  listDownstreams,
  previewAddon,
  type AddonAuthKind,
  type AddonAuthSpec,
  type AddonEndpointSpec,
  type AddonOAuthWizardResponse,
  type AddonSpec,
} from '@/api/client'
import { toast } from 'sonner'
import { OAuthStep } from '@/components/create-mcp/OAuthStep'
import { TestStep } from '@/components/create-mcp/TestStep'
import {
  AuthStep,
  BasicsStep,
  EndpointsStep,
  ReviewStep,
  emptyEndpoint,
} from '@/components/create-mcp/Steps'

type Step = 'basics' | 'auth' | 'oauth' | 'endpoints' | 'test' | 'review'

const ALL_STEPS: StepDef[] = [
  { id: 'basics', label: 'Basics' },
  { id: 'auth', label: 'Auth' },
  { id: 'oauth', label: 'OAuth' },
  { id: 'endpoints', label: 'Endpoints' },
  { id: 'test', label: 'Test' },
  { id: 'review', label: 'Review' },
]

// visibleSteps filters out the OAuth step unless the user picked an OAuth2 auth kind.
function visibleSteps(kind: AddonAuthKind): StepDef[] {
  if (kind === 'oauth2' || kind === 'oauth2_pending') return ALL_STEPS
  return ALL_STEPS.filter((s) => s.id !== 'oauth')
}

export function CreateMCPPage() {
  const [step, setStep] = useState<Step>('basics')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [parentServer, setParentServer] = useState('')
  const [authKind, setAuthKind] = useState<AddonAuthKind>('bearer')
  const [headerName, setHeaderName] = useState('Authorization')
  const [queryName, setQueryName] = useState('api_key')
  const [endpoints, setEndpoints] = useState<AddonEndpointSpec[]>([emptyEndpoint()])
  const [yamlPreview, setYamlPreview] = useState<string>('')
  const [submitting, setSubmitting] = useState(false)

  // OAuth wizard state — only relevant when authKind is oauth2 / oauth2_pending.
  const [oauthSpec, setOAuthSpec] = useState<AddonAuthSpec>({
    kind: 'oauth2',
    grant_type: 'authorization_code',
    use_pkce: true,
    scopes: [],
  })
  const [oauthScopeName, setOAuthScopeName] = useState('')
  const [oauthResult, setOAuthResult] = useState<AddonOAuthWizardResponse | null>(null)

  const dsFetcher = useCallback(() => listDownstreams(), [])
  const { data: downstreams } = useApi(dsFetcher)

  const steps = useMemo(() => visibleSteps(authKind), [authKind])

  const completedSteps = useMemo(() => {
    const currentIdx = steps.findIndex((s) => s.id === step)
    return steps.slice(0, currentIdx).map((s) => s.id)
  }, [steps, step])

  const buildSpec = useCallback((): AddonSpec => {
    const baseAuth: AddonAuthSpec = (() => {
      if (authKind === 'oauth2' || authKind === 'oauth2_pending') {
        return { ...oauthSpec, kind: authKind }
      }
      return {
        kind: authKind,
        ...(authKind === 'api_key_header' ? { header_name: headerName } : {}),
        ...(authKind === 'api_key_query' ? { query_name: queryName } : {}),
      }
    })()
    return {
      name: name.trim(),
      description: description.trim(),
      base_url: baseURL.trim(),
      parent_server: parentServer.trim(),
      auth: baseAuth,
      ...(oauthScopeName ? { auth_scope: oauthScopeName } : {}),
      endpoints,
    }
  }, [
    name, description, baseURL, parentServer, authKind, headerName, queryName,
    endpoints, oauthSpec, oauthScopeName,
  ])

  // Refresh preview whenever the user lands on the review step.
  useEffect(() => {
    if (step !== 'review') return
    let cancelled = false
    previewAddon(buildSpec())
      .then((res) => { if (!cancelled) setYamlPreview(res.yaml) })
      .catch((err: Error) => { if (!cancelled) setYamlPreview(`# preview failed: ${err.message}`) })
    return () => { cancelled = true }
  }, [step, buildSpec])

  function nextStep() {
    const idx = steps.findIndex((s) => s.id === step)
    if (idx < steps.length - 1) setStep(steps[idx + 1].id as Step)
  }
  function prevStep() {
    const idx = steps.findIndex((s) => s.id === step)
    if (idx > 0) setStep(steps[idx - 1].id as Step)
  }

  async function handleCreate() {
    setSubmitting(true)
    try {
      const res = await createAddon(buildSpec())
      toast.success(`Created ${res.name} with ${res.tools.length} tool(s).`)
      // Reset to a fresh form.
      setStep('basics')
      setName(''); setDescription(''); setBaseURL(''); setParentServer('')
      setAuthKind('bearer'); setEndpoints([emptyEndpoint()])
      setYamlPreview('')
      setOAuthSpec({ kind: 'oauth2', grant_type: 'authorization_code', use_pkce: true, scopes: [] })
      setOAuthScopeName(''); setOAuthResult(null)
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'create failed'
      toast.error(msg)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="space-y-5 max-w-3xl">
      <header className="flex items-center justify-between">
        <div className="space-y-1">
          <h1 className="text-xl font-semibold">Create Custom MCP</h1>
          <p className="text-sm text-muted-foreground">
            Build an HTTP-based MCP addon with custom endpoints and auth.
          </p>
        </div>
        <Button variant="ghost" asChild>
          <Link to="/"><ArrowLeft className="mr-1.5 h-4 w-4" /> Back</Link>
        </Button>
      </header>

      <StepIndicator steps={steps} currentStep={step} completedSteps={completedSteps} />

      {step === 'basics' && (
        <BasicsStep
          name={name} setName={setName}
          description={description} setDescription={setDescription}
          baseURL={baseURL} setBaseURL={setBaseURL}
          parentServer={parentServer} setParentServer={setParentServer}
          downstreams={downstreams ?? []}
        />
      )}

      {step === 'auth' && (
        <AuthStep
          authKind={authKind} setAuthKind={setAuthKind}
          headerName={headerName} setHeaderName={setHeaderName}
          queryName={queryName} setQueryName={setQueryName}
        />
      )}

      {step === 'oauth' && (
        <OAuthStep
          authSpec={oauthSpec} setAuthSpec={setOAuthSpec}
          authScopeName={oauthScopeName} setAuthScopeName={setOAuthScopeName}
          parentServer={parentServer}
          redirectURL={`${window.location.origin}/api/v1/oauth/callback`}
          oauthResult={oauthResult} setOAuthResult={setOAuthResult}
        />
      )}

      {step === 'endpoints' && (
        <EndpointsStep endpoints={endpoints} setEndpoints={setEndpoints} />
      )}

      {step === 'test' && (
        <TestStep spec={buildSpec()} authScopeID={oauthResult?.auth_scope?.id} />
      )}

      {step === 'review' && (
        <ReviewStep yaml={yamlPreview} />
      )}

      <div className="flex justify-between border-t border-border pt-4">
        <Button variant="ghost" onClick={prevStep} disabled={step === steps[0]?.id}>
          <ArrowLeft className="mr-1 h-4 w-4" /> Back
        </Button>
        {step !== 'review' ? (
          <Button onClick={nextStep}>
            Next <ArrowRight className="ml-1 h-4 w-4" />
          </Button>
        ) : (
          <Button onClick={handleCreate} disabled={submitting}>
            {submitting ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : <CheckCircle2 className="mr-1 h-4 w-4" />}
            Create
          </Button>
        )}
      </div>
    </div>
  )
}

