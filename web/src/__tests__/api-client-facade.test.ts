import { describe, expect, it } from 'vitest'

import * as client from '@/api/client'
import * as addons from '@/api/addons'
import * as approvals from '@/api/approvals'
import * as audit from '@/api/audit'
import * as auth from '@/api/auth'
import * as backups from '@/api/backups'
import * as connections from '@/api/connections'
import * as dashboard from '@/api/dashboard'
import * as descriptions from '@/api/descriptions'
import * as guards from '@/api/guards'
import * as hammerspoon from '@/api/hammerspoon'
import * as harnessSetup from '@/api/harness-setup'
import * as mesh from '@/api/mesh'
import * as modelProfiles from '@/api/model-profiles'
import * as p2p from '@/api/p2p'
import * as skillRegistry from '@/api/skill-registry'
import * as system from '@/api/system'
import * as transport from '@/api/transport'
import * as workspaces from '@/api/workspaces'

describe('API client compatibility facade', () => {
  it('re-exports every runtime domain binding by identity', () => {
    const domains = [
      workspaces,
      auth,
      connections,
      audit,
      dashboard,
      approvals,
      system,
      descriptions,
      mesh,
      addons,
      p2p,
      backups,
      skillRegistry,
      guards,
      modelProfiles,
      hammerspoon,
      harnessSetup,
    ]

    for (const domain of domains) {
      for (const [name, value] of Object.entries(domain)) {
        expect(client[name as keyof typeof client], name).toBe(value)
      }
    }
  })

  it('keeps transport internals private while preserving the public core', () => {
    expect(client.request).toBe(transport.request)
    expect(client.apiURL).toBe(transport.apiURL)
    expect(client.ApiClientError).toBe(transport.ApiClientError)
    expect(client).not.toHaveProperty('DEFAULT_TIMEOUT_MS')
  })

  it('keeps direct download and desktop endpoint URLs on the shared base', () => {
    expect(client.backupDownloadURL('snapshot/example')).toBe(
      '/api/v1/backups/snapshot%2Fexample/download',
    )
    expect(client.hammerspoonSnippetURL()).toBe('/api/v1/hammerspoon/snippet')
  })
})
