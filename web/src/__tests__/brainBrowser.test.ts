import { afterEach, describe, expect, it, vi } from 'vitest'

// Mock the shared request() helper so we assert the brain browser client
// hits the right path + method (POST=create, PUT=update) without touching
// fetch / the network.
const request = vi.fn()
vi.mock('@/api/client', () => ({
  request: (...args: unknown[]) => request(...args),
  // ApiClientError is re-exported from the client; the browser client only
  // imports request, so a minimal stub keeps the module graph happy.
  ApiClientError: class ApiClientError extends Error {},
}))

import {
  getBrainTree,
  listBrainTasks,
  listBrainMemories,
  listBrainTaskRecords,
  listBrainMemoryRecords,
  getBrainTask,
  getBrainMemory,
  saveBrainTask,
  saveBrainMemory,
  type BrainTaskRecord,
  type BrainMemoryRecord,
} from '@/api/brainBrowser'

const task = (over: Partial<BrainTaskRecord> = {}): BrainTaskRecord => ({
  id: '',
  workspace: 'ws',
  title: 'T',
  status: 'open',
  tags: [],
  pinned: false,
  description: '',
  ...over,
})

const memory = (over: Partial<BrainMemoryRecord> = {}): BrainMemoryRecord => ({
  id: '',
  kind: 'note',
  name: 'm',
  workspace: 'ws',
  tags: [],
  pinned: false,
  content: '',
  ...over,
})

describe('brain browser api client', () => {
  afterEach(() => request.mockReset())

  it('getBrainTree GETs /brain/tree', async () => {
    request.mockResolvedValueOnce([])
    await getBrainTree()
    expect(request).toHaveBeenCalledWith('/brain/tree')
  })

  it('list endpoints encode the workspace slug', async () => {
    request.mockResolvedValue([])
    await listBrainTasks('ws/one')
    expect(request).toHaveBeenCalledWith('/brain/workspaces/ws%2Fone/tasks')
    await listBrainMemories('ws/one')
    expect(request).toHaveBeenCalledWith('/brain/workspaces/ws%2Fone/memory')
  })

  it('listBrainTaskRecords hits /brain/records with kind+filters', async () => {
    request.mockResolvedValue([])
    await listBrainTaskRecords('ws', { status: 'doing', source: 'repo' })
    const call = request.mock.calls[0][0] as string
    expect(call.startsWith('/brain/records?')).toBe(true)
    expect(call).toContain('workspace=ws')
    expect(call).toContain('kind=task')
    expect(call).toContain('status=doing')
    expect(call).toContain('source=repo')
  })

  it('listBrainMemoryRecords hits /brain/records with kind=memory', async () => {
    request.mockResolvedValue([])
    await listBrainMemoryRecords('ws', { memoryKind: 'fact' })
    const call = request.mock.calls[0][0] as string
    expect(call.startsWith('/brain/records?')).toBe(true)
    expect(call).toContain('kind=memory')
    expect(call).toContain('memory_kind=fact')
  })

  it('get endpoints encode the id and route on kind', async () => {
    request.mockResolvedValue({})
    await getBrainTask('01ABC')
    expect(request).toHaveBeenCalledWith('/brain/record/task/01ABC')
    await getBrainMemory('01DEF')
    expect(request).toHaveBeenCalledWith('/brain/record/memory/01DEF')
  })

  it('saveBrainTask POSTs when id is empty (create)', async () => {
    request.mockResolvedValueOnce(task({ id: '01NEW' }))
    await saveBrainTask(task())
    expect(request).toHaveBeenCalledWith('/brain/record/task', {
      method: 'POST',
      body: expect.any(String),
    })
  })

  it('saveBrainTask PUTs to the id path when id is set (update)', async () => {
    request.mockResolvedValueOnce(task({ id: '01OLD' }))
    await saveBrainTask(task({ id: '01OLD', title: 'changed' }))
    expect(request).toHaveBeenCalledWith('/brain/record/task/01OLD', {
      method: 'PUT',
      body: expect.any(String),
    })
  })

  it('saveBrainMemory POSTs on create and PUTs on update', async () => {
    request.mockResolvedValue(memory({ id: '01M' }))
    await saveBrainMemory(memory())
    expect(request).toHaveBeenCalledWith('/brain/record/memory', {
      method: 'POST',
      body: expect.any(String),
    })
    await saveBrainMemory(memory({ id: '01M' }))
    expect(request).toHaveBeenCalledWith('/brain/record/memory/01M', {
      method: 'PUT',
      body: expect.any(String),
    })
  })
})
