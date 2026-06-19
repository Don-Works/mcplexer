/**
 * StdioServerForm — inline wizard step that collects the details needed to
 * create a new stdio downstream server.  Used inside QuickSetupPage so the
 * user stays in the wizard rather than being redirected to a separate page.
 */
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ArrowRight, Plus, Trash2 } from 'lucide-react'

export interface StdioFormValues {
  name: string
  command: string
  args: string[]
}

interface Props {
  onSubmit: (values: StdioFormValues) => void
}

export function StdioServerForm({ onSubmit }: Props) {
  const [name, setName] = useState('')
  const [command, setCommand] = useState('')
  const [args, setArgs] = useState<string[]>([''])

  function updateArg(idx: number, v: string) {
    setArgs(args.map((a, i) => (i === idx ? v : a)))
  }
  function removeArg(idx: number) {
    setArgs(args.filter((_, i) => i !== idx))
  }

  function handleSubmit() {
    onSubmit({
      name: name.trim(),
      command: command.trim(),
      args: args.map((a) => a.trim()).filter(Boolean),
    })
  }

  const valid = name.trim().length > 0 && command.trim().length > 0

  return (
    <div className="mx-auto max-w-md space-y-4">
      <div className="space-y-1.5">
        <Label className="text-xs text-muted-foreground">Server Name</Label>
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="e.g. my-tools"
          data-testid="stdio-name"
        />
      </div>
      <div className="space-y-1.5">
        <Label className="text-xs text-muted-foreground">Command</Label>
        <Input
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          placeholder="e.g. npx or /usr/local/bin/my-mcp-server"
          data-testid="stdio-command"
        />
      </div>

      <div className="space-y-1.5">
        <Label className="text-xs text-muted-foreground">Arguments</Label>
        {args.map((arg, idx) => (
          <div key={idx} className="flex gap-2">
            <Input
              value={arg}
              onChange={(e) => updateArg(idx, e.target.value)}
              placeholder={`arg ${idx + 1}`}
              className="flex-1"
            />
            {args.length > 1 && (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-9 w-9 p-0"
                onClick={() => removeArg(idx)}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            )}
          </div>
        ))}
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="text-xs"
          onClick={() => setArgs([...args, ''])}
        >
          <Plus className="mr-1 h-3 w-3" /> Add Argument
        </Button>
      </div>

      <p className="text-xs text-muted-foreground/60">
        Environment variables can be configured via auth scopes after setup.
      </p>

      <div className="flex justify-end">
        <Button
          onClick={handleSubmit}
          disabled={!valid}
          data-testid="stdio-next"
        >
          Next <ArrowRight className="ml-2 h-4 w-4" />
        </Button>
      </div>
    </div>
  )
}
