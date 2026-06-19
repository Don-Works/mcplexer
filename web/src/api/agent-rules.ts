import { request } from './client'

// AgentRulesStatus mirrors agentRulesStatusResponse on the Go side.
// `current_version` is the BEGIN-marker version installed in the file
// (0 when no block is installed, -1 when the marker is malformed).
// `latest_version` is what `mcplexer rules sync` would write today.
export interface AgentRulesStatus {
  present: boolean
  current_version: number
  latest_version: number
  up_to_date: boolean
  path: string
}

export interface AgentRulesSyncResult {
  changed: boolean
  version: number
  path: string
}

export function getAgentRulesStatus(): Promise<AgentRulesStatus> {
  return request('/agent-rules/status')
}

export function syncAgentRules(): Promise<AgentRulesSyncResult> {
  return request('/agent-rules/sync', { method: 'POST' })
}
