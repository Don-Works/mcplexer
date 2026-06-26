import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import type { ComponentProps } from 'react'
import { describe, expect, it, vi } from 'vitest'
import type { Task } from '@/api/tasks'
import type { Workspace } from '@/api/types'

const apiMocks = vi.hoisted(() => ({
  createTask: vi.fn(),
  updateTask: vi.fn(),
}))

vi.mock('@/api/tasks', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/tasks')>()
  return {
    ...actual,
    createTask: apiMocks.createTask,
    updateTask: apiMocks.updateTask,
  }
})

vi.mock('@/api/client', () => ({
  listUsers: vi.fn().mockResolvedValue({ users: [] }),
}))

vi.mock('sonner', () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}))

import { TaskEditDialog } from '@/pages/tasks/TaskEditDialog'

const workspace: Workspace = {
  id: 'ws-1',
  name: 'Workspace one',
  root_path: '/tmp/ws-1',
  tags: {},
  default_policy: 'allow',
  created_at: '2026-06-12T10:00:00Z',
  updated_at: '2026-06-12T10:00:00Z',
}

function createdTask(overrides: Partial<Task> = {}): Task {
  return {
    id: 'task-1',
    workspace_id: 'ws-1',
    title: 'Draft task',
    description: '',
    status: 'open',
    priority: 'normal',
    assignee_origin_kind: 'local',
    source_kind: 'user',
    created_at: '2026-06-12T10:00:00Z',
    updated_at: '2026-06-12T10:00:00Z',
    ...overrides,
  }
}

type CreateDialogProps = Extract<ComponentProps<typeof TaskEditDialog>, { mode: 'create' }>
type EditDialogProps = Extract<ComponentProps<typeof TaskEditDialog>, { mode: 'edit' }>

function renderCreateDialog(overrides: Partial<CreateDialogProps> = {}) {
  const props = {
    mode: 'create' as const,
    open: true,
    onOpenChange: vi.fn(),
    workspaces: [workspace],
    defaultWorkspaceId: workspace.id,
    onSaved: vi.fn(),
    ...overrides,
  }
  render(<TaskEditDialog {...props} />)
  return props
}

function renderEditDialog(task: Task, overrides: Partial<EditDialogProps> = {}) {
  const props = {
    mode: 'edit' as const,
    open: true,
    onOpenChange: vi.fn(),
    workspaces: [workspace],
    onSaved: vi.fn(),
    task,
    ...overrides,
  }
  return {
    props,
    view: render(<TaskEditDialog {...props} />),
  }
}

describe('TaskEditDialog', () => {
  it('does not create a task from an implicit form submit while typing', () => {
    renderCreateDialog()
    fireEvent.change(screen.getByPlaceholderText('Patch the audit redaction for peer IDs'), {
      target: { value: 'Draft task' },
    })

    const form = document.querySelector('form')
    expect(form).not.toBeNull()
    fireEvent.submit(form as HTMLFormElement)

    expect(apiMocks.createTask).not.toHaveBeenCalled()
  })

  it('creates only from the explicit Create task button', async () => {
    apiMocks.createTask.mockResolvedValueOnce(createdTask())
    const props = renderCreateDialog()
    fireEvent.change(screen.getByPlaceholderText('Patch the audit redaction for peer IDs'), {
      target: { value: 'Draft task' },
    })

    fireEvent.click(screen.getByRole('button', { name: /create task/i }))

    await waitFor(() => {
      expect(apiMocks.createTask).toHaveBeenCalledWith(expect.objectContaining({
        workspace_id: workspace.id,
        title: 'Draft task',
      }))
    })
    await waitFor(() => {
      expect(props.onSaved).toHaveBeenCalledWith(expect.objectContaining({ id: 'task-1' }))
    })
  })

  it('keeps an edit draft when the same task refreshes while the dialog is open', () => {
    const original = createdTask({
      id: 'task-edit',
      title: 'Original task',
      description: 'Original description',
      updated_at: '2026-06-12T10:00:00Z',
    })
    const { props, view } = renderEditDialog(original)

    fireEvent.change(screen.getByPlaceholderText('Patch the audit redaction for peer IDs'), {
      target: { value: 'Typing still in progress' },
    })

    view.rerender(
      <TaskEditDialog
        {...props}
        task={{
          ...original,
          updated_at: '2026-06-12T10:01:00Z',
        }}
      />,
    )

    expect(screen.getByPlaceholderText('Patch the audit redaction for peer IDs')).toHaveValue('Typing still in progress')
  })
})
