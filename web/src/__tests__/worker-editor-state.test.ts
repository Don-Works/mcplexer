import { describe, expect, it } from 'vitest'

import type { Worker } from '@/api/workers'
import { stateFromWorker, toUpdateInput } from '@/pages/workers/worker-editor-state'

function baseWorker(overrides: Partial<Worker> = {}): Worker {
  return {
    id: 'wkr-test',
    name: 'telegram-responder',
    description: '',
    model_provider: 'opencode_cli',
    model_id: 'minimax/MiniMax-M2.7-highspeed',
    model_endpoint_url: '',
    secret_scope_id: 'scope-test',
    skill_refs: [],
    prompt_template: 'reply',
    parameters_json: '{}',
    schedule_spec: 'manual',
    tool_allowlist_json: '[]',
    output_channels_json:
      '[{"type":"mesh","priority":"high","tags":"telegram","notify_user":true,"reply_to_trigger":true}]',
    exec_mode: 'autonomous',
    concurrency_policy: 'skip',
    max_input_tokens: 0,
    max_output_tokens: 0,
    max_tool_calls: 0,
    max_wall_clock_seconds: 300,
    max_monthly_cost_usd: 0,
    max_consecutive_failures: 0,
    enabled: true,
    workspace_id: 'ws-test',
    workspace_access: [{ workspace_id: 'ws-test', access: 'write' }],
    created_at: '2026-06-12T00:00:00Z',
    updated_at: '2026-06-12T00:00:00Z',
    ...overrides,
  }
}

describe('worker editor output channels', () => {
  it('preserves mesh notify and reply fields across edit saves', () => {
    const state = stateFromWorker(baseWorker())
    const update = toUpdateInput(state)
    const channels = JSON.parse(update.output_channels_json ?? '[]')

    expect(channels).toEqual([
      {
        type: 'mesh',
        priority: 'high',
        tags: 'telegram',
        notify_user: true,
        reply_to_trigger: true,
      },
    ])
  })
})
