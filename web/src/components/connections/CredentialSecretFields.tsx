import { Eye, EyeOff } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { OAuthProvider } from '@/api/types'

export function FreeEnvForm({
  envKey,
  setEnvKey,
  envValue,
  setEnvValue,
  showValue,
  setShowValue,
}: {
  envKey: string
  setEnvKey: (value: string) => void
  envValue: string
  setEnvValue: (value: string) => void
  showValue: boolean
  setShowValue: (value: boolean) => void
}) {
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <div className="space-y-1">
        <Label className="text-xs text-muted-foreground">Variable name</Label>
        <Input
          className="font-mono text-sm"
          value={envKey}
          onChange={(event) => setEnvKey(event.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, ''))}
          placeholder="API_KEY"
          autoComplete="off"
        />
      </div>
      <div className="space-y-1">
        <Label className="text-xs text-muted-foreground">Value</Label>
        <div className="relative">
          <Input
            className="pr-8 font-mono text-sm"
            type={showValue ? 'text' : 'password'}
            value={envValue}
            onChange={(event) => setEnvValue(event.target.value)}
            placeholder="sk-..."
            autoComplete="off"
          />
          <button
            type="button"
            className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            aria-label={showValue ? 'Hide secret value' : 'Show secret value'}
            onClick={() => setShowValue(!showValue)}
          >
            {showValue ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
          </button>
        </div>
      </div>
    </div>
  )
}

export function HeaderForm({
  token,
  setToken,
  showToken,
  setShowToken,
}: {
  token: string
  setToken: (value: string) => void
  showToken: boolean
  setShowToken: (value: boolean) => void
}) {
  return (
    <div className="space-y-1">
      <Label className="text-xs text-muted-foreground">Bearer token / API key</Label>
      <div className="relative">
        <Input
          className="pr-8 font-mono text-sm"
          type={showToken ? 'text' : 'password'}
          value={token}
          onChange={(event) => setToken(event.target.value)}
          placeholder="sk-..."
          autoComplete="off"
        />
        <button
          type="button"
          className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
          aria-label={showToken ? 'Hide secret value' : 'Show secret value'}
          onClick={() => setShowToken(!showToken)}
        >
          {showToken ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
        </button>
      </div>
      <p className="text-[11px] text-muted-foreground">
        Stored as <code className="bg-muted px-1 py-0.5 font-mono">Authorization: Bearer &lt;token&gt;</code>
      </p>
    </div>
  )
}

export function OAuthProviderField({
  providers,
  providerId,
  onProviderChange,
}: {
  providers: OAuthProvider[]
  providerId: string
  onProviderChange: (value: string) => void
}) {
  return (
    <div className="space-y-1">
      <Label className="text-xs text-muted-foreground">OAuth provider</Label>
      {providers.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          No OAuth providers configured. Set one up via Quick Setup first.
        </p>
      ) : (
        <Select value={providerId} onValueChange={onProviderChange}>
          <SelectTrigger data-testid="inline-credential-oauth-provider">
            <SelectValue placeholder="Select a provider..." />
          </SelectTrigger>
          <SelectContent>
            {providers.map((provider) => (
              <SelectItem key={provider.id} value={provider.id}>
                {provider.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
      <p className="text-[11px] text-muted-foreground">
        After creating, use the Authenticate button to complete the OAuth flow.
      </p>
    </div>
  )
}
