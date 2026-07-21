import type {
  HarnessKey,
  HarnessSetupRow,
  HarnessSetupStatusResponse,
} from './types'
import { request } from './transport'

export function getHarnessSetupStatus(): Promise<HarnessSetupStatusResponse> {
  return request('/setup/status')
}

export function installHarness(harness: HarnessKey): Promise<HarnessSetupRow> {
  return request('/setup/install', {
    method: 'POST',
    body: JSON.stringify({ harness }),
  })
}

export function recheckHarness(harness: HarnessKey): Promise<HarnessSetupRow> {
  return request('/setup/recheck', {
    method: 'POST',
    body: JSON.stringify({ harness }),
  })
}
