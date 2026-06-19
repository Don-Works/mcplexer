// Output channel editor — multi-add UI for OutputChannel rows. Each
// row is one of the six channel kinds the runner supports. Type-
// specific fields appear conditionally so the form stays compact for
// the simple mesh+file cases.

import {
  CheckSquare,
  FileText,
  Github,
  MessageSquare,
  Plus,
  Radio,
  Trash2,
  Webhook,
} from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { OutputChannel, OutputChannelType } from './worker-editor-state'

// channelIcon picks the lucide icon best suited to each channel kind.
// Surfaced next to the type label inside the Select trigger so the
// editor "speaks" what each row routes to at a glance.
function channelIcon(type: OutputChannelType) {
  switch (type) {
    case 'mesh':
      return <Radio className="h-3.5 w-3.5" />
    case 'file':
      return <FileText className="h-3.5 w-3.5" />
    case 'webhook':
      return <Webhook className="h-3.5 w-3.5" />
    case 'slack_webhook':
      return <MessageSquare className="h-3.5 w-3.5" />
    case 'clickup_task':
      return <CheckSquare className="h-3.5 w-3.5" />
    case 'github_issue':
      return <Github className="h-3.5 w-3.5" />
  }
}

interface Props {
  channels: OutputChannel[]
  onChange: (next: OutputChannel[]) => void
}

const CHANNEL_OPTIONS: ReadonlyArray<{ value: OutputChannelType; label: string }> = [
  { value: 'mesh', label: 'mesh' },
  { value: 'file', label: 'file' },
  { value: 'webhook', label: 'webhook' },
  { value: 'slack_webhook', label: 'slack' },
  { value: 'clickup_task', label: 'clickup' },
  { value: 'github_issue', label: 'github' },
]

export function OutputChannelEditor({ channels, onChange }: Props) {
  function update(idx: number, patch: Partial<OutputChannel>) {
    const next = channels.map((c, i) => (i === idx ? { ...c, ...patch } : c))
    onChange(next)
  }
  function remove(idx: number) {
    const next = channels.filter((_, i) => i !== idx)
    onChange(next.length === 0 ? [{ type: 'mesh', priority: 'normal' }] : next)
  }
  function add() {
    onChange([...channels, { type: 'mesh', priority: 'normal' }])
  }
  function changeType(idx: number, v: OutputChannelType) {
    onChange(channels.map((c, i) => (i === idx ? defaultsForType(v, c) : c)))
  }

  return (
    <div className="space-y-2">
      {channels.map((c, idx) => (
        <div
          key={idx}
          className="space-y-2 rounded-md border border-border/60 bg-background/40 p-2"
        >
          <div className="flex flex-wrap items-center gap-2">
            <span className="inline-flex h-9 w-9 items-center justify-center border border-border/60 bg-card/40 text-muted-foreground">
              {channelIcon(c.type)}
            </span>
            <Select value={c.type} onValueChange={(v) => changeType(idx, v as OutputChannelType)}>
              <SelectTrigger className="w-36">
                <SelectValue placeholder="Type" />
              </SelectTrigger>
              <SelectContent>
                {CHANNEL_OPTIONS.map((o) => (
                  <SelectItem key={o.value} value={o.value}>
                    <span className="inline-flex items-center gap-2">
                      <span className="text-muted-foreground">{channelIcon(o.value)}</span>
                      {o.label}
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="ml-auto text-muted-foreground"
              onClick={() => remove(idx)}
              data-testid={`output-channel-remove-${idx}`}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>
          <ChannelFields channel={c} onPatch={(p) => update(idx, p)} />
        </div>
      ))}

      <Button type="button" size="sm" variant="outline" onClick={add}>
        <Plus className="mr-1.5 h-3.5 w-3.5" /> Add channel
      </Button>
    </div>
  )
}

// ChannelFields renders the per-type field set. Pulled out so the
// parent stays a thin list-management loop. Each branch is intentionally
// flat and small — easier to scan than a single-component switch.
function ChannelFields({
  channel,
  onPatch,
}: {
  channel: OutputChannel
  onPatch: (patch: Partial<OutputChannel>) => void
}) {
  switch (channel.type) {
    case 'mesh':
      return <MeshFields channel={channel} onPatch={onPatch} />
    case 'file':
      return <FileFields channel={channel} onPatch={onPatch} />
    case 'webhook':
      return <WebhookFields channel={channel} onPatch={onPatch} />
    case 'slack_webhook':
      return <SlackFields channel={channel} onPatch={onPatch} />
    case 'clickup_task':
      return <ClickUpFields channel={channel} onPatch={onPatch} />
    case 'github_issue':
      return <GitHubFields channel={channel} onPatch={onPatch} />
  }
}

type FieldProps = {
  channel: OutputChannel
  onPatch: (patch: Partial<OutputChannel>) => void
}

function MeshFields({ channel, onPatch }: FieldProps) {
  return (
    <div className="space-y-2">
      <div className="grid gap-2 sm:grid-cols-2">
        <div>
          <Label className="mb-1 text-xs">Priority</Label>
          <PrioritySelect
            value={channel.priority ?? 'normal'}
            onChange={(priority) => onPatch({ priority })}
          />
        </div>
        <div>
          <Label className="mb-1 text-xs">Priority on failure</Label>
          <PrioritySelect
            value={channel.priority_on_fail ?? ''}
            allowEmpty
            onChange={(priority_on_fail) => onPatch({ priority_on_fail })}
          />
        </div>
        <div>
          <Label className="mb-1 text-xs">Tags</Label>
          <Input
            value={channel.tags ?? ''}
            onChange={(e) => onPatch({ tags: e.target.value })}
            placeholder="worker,telegram"
          />
        </div>
        <div>
          <Label className="mb-1 text-xs">Peer target</Label>
          <Input
            value={channel.to_peer ?? ''}
            onChange={(e) => onPatch({ to_peer: e.target.value })}
            placeholder="device-name"
          />
        </div>
      </div>
      <div className="flex flex-wrap gap-3 text-xs text-muted-foreground">
        <CheckboxField
          label="Notify user"
          checked={Boolean(channel.notify_user)}
          onCheckedChange={(notify_user) => onPatch({ notify_user })}
        />
        <CheckboxField
          label="Reply to trigger"
          checked={Boolean(channel.reply_to_trigger)}
          onCheckedChange={(reply_to_trigger) => onPatch({ reply_to_trigger })}
        />
        <CheckboxField
          label="Broadcast peers"
          checked={Boolean(channel.broadcast_peers)}
          onCheckedChange={(broadcast_peers) => onPatch({ broadcast_peers })}
        />
      </div>
    </div>
  )
}

function PrioritySelect({
  value,
  allowEmpty = false,
  onChange,
}: {
  value: OutputChannel['priority'] | ''
  allowEmpty?: boolean
  onChange: (value: OutputChannel['priority'] | undefined) => void
}) {
  return (
    <Select
      value={value || 'none'}
      onValueChange={(v) => onChange(v === 'none' ? undefined : (v as OutputChannel['priority']))}
    >
      <SelectTrigger>
        <SelectValue placeholder="Priority" />
      </SelectTrigger>
      <SelectContent>
        {allowEmpty && <SelectItem value="none">default</SelectItem>}
        <SelectItem value="low">low</SelectItem>
        <SelectItem value="normal">normal</SelectItem>
        <SelectItem value="high">high</SelectItem>
        <SelectItem value="critical">critical</SelectItem>
      </SelectContent>
    </Select>
  )
}

function CheckboxField({
  label,
  checked,
  onCheckedChange,
}: {
  label: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <label className="inline-flex items-center gap-2">
      <Checkbox checked={checked} onCheckedChange={(v) => onCheckedChange(v === true)} />
      {label}
    </label>
  )
}

function FileFields({ channel, onPatch }: FieldProps) {
  return (
    <Input
      value={channel.path ?? ''}
      onChange={(e) => onPatch({ path: e.target.value })}
      placeholder="/path/to/output.md"
    />
  )
}

function WebhookFields({ channel, onPatch }: FieldProps) {
  return (
    <div className="space-y-2">
      <Input
        value={channel.url ?? ''}
        onChange={(e) => onPatch({ url: e.target.value })}
        placeholder="https://example.com/hook"
      />
      <label className="flex items-center gap-2 text-xs text-muted-foreground">
        <input
          type="checkbox"
          checked={Boolean(channel.include_metadata)}
          onChange={(e) => onPatch({ include_metadata: e.target.checked })}
        />
        Include full run metadata in body
      </label>
    </div>
  )
}

function SlackFields({ channel, onPatch }: FieldProps) {
  return (
    <div className="grid grid-cols-2 gap-2">
      <div className="col-span-2">
        <Label className="mb-1 text-xs">Incoming webhook URL</Label>
        <Input
          value={channel.url ?? ''}
          onChange={(e) => onPatch({ url: e.target.value })}
          placeholder="https://hooks.slack.com/services/..."
        />
      </div>
      <div>
        <Label className="mb-1 text-xs">Channel override</Label>
        <Input
          value={channel.channel ?? ''}
          onChange={(e) => onPatch({ channel: e.target.value })}
          placeholder="#alerts"
        />
      </div>
      <div>
        <Label className="mb-1 text-xs">Prefix</Label>
        <Input
          value={channel.prefix ?? ''}
          onChange={(e) => onPatch({ prefix: e.target.value })}
          placeholder="[worker]"
        />
      </div>
    </div>
  )
}

function ClickUpFields({ channel, onPatch }: FieldProps) {
  return (
    <div className="grid grid-cols-2 gap-2">
      <div>
        <Label className="mb-1 text-xs">List ID</Label>
        <Input
          value={channel.list_id ?? ''}
          onChange={(e) => onPatch({ list_id: e.target.value })}
          placeholder="9001..."
        />
      </div>
      <div>
        <Label className="mb-1 text-xs">Secret scope (api_key)</Label>
        <Input
          value={channel.secret_scope_id ?? ''}
          onChange={(e) => onPatch({ secret_scope_id: e.target.value })}
          placeholder="scope-id"
        />
      </div>
      <div className="col-span-2">
        <Label className="mb-1 text-xs">Task name prefix</Label>
        <Input
          value={channel.name_prefix ?? ''}
          onChange={(e) => onPatch({ name_prefix: e.target.value })}
          placeholder="[worker]"
        />
      </div>
    </div>
  )
}

function GitHubFields({ channel, onPatch }: FieldProps) {
  return (
    <div className="grid grid-cols-2 gap-2">
      <div>
        <Label className="mb-1 text-xs">Repo (owner/name)</Label>
        <Input
          value={channel.repo ?? ''}
          onChange={(e) => onPatch({ repo: e.target.value })}
          placeholder="acme/widgets"
        />
      </div>
      <div>
        <Label className="mb-1 text-xs">Secret scope (api_key)</Label>
        <Input
          value={channel.secret_scope_id ?? ''}
          onChange={(e) => onPatch({ secret_scope_id: e.target.value })}
          placeholder="scope-id"
        />
      </div>
      <div className="col-span-2">
        <Label className="mb-1 text-xs">Issue title prefix</Label>
        <Input
          value={channel.title_prefix ?? ''}
          onChange={(e) => onPatch({ title_prefix: e.target.value })}
          placeholder="[worker]"
        />
      </div>
    </div>
  )
}

// defaultsForType resets the channel to the minimum viable shape for a
// new type while preserving any cross-cutting fields (none today, but
// the signature keeps the door open if we ever add tags / labels).
function defaultsForType(type: OutputChannelType, prev: OutputChannel): OutputChannel {
  void prev
  switch (type) {
    case 'mesh':
      return { type, priority: 'normal' }
    case 'file':
      return { type, path: '' }
    case 'webhook':
      return { type, url: '', include_metadata: true }
    case 'slack_webhook':
      return { type, url: '' }
    case 'clickup_task':
      return { type, list_id: '', secret_scope_id: '' }
    case 'github_issue':
      return { type, repo: '', secret_scope_id: '' }
  }
}
