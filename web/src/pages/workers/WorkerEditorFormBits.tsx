// Small form primitives shared across the WorkerEditor cards. Pulled
// out of WorkerEditorCards.tsx so each file fits the 300-line budget.

import { Label } from '@/components/ui/label'

interface RowProps {
  label: string
  htmlFor: string
  required?: boolean
  children: React.ReactNode
}

export function Row({ label, htmlFor, required, children }: RowProps) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={htmlFor} className="text-xs">
        {label}
        {required && <span className="ml-0.5 text-destructive">*</span>}
      </Label>
      {children}
    </div>
  )
}

interface RadioRowProps {
  name: string
  value: string
  current: string
  onChange: (v: string) => void
  label: string
  hint: string
}

export function RadioRow({ name, value, current, onChange, label, hint }: RadioRowProps) {
  const id = `${name}-${value}`
  return (
    <label
      htmlFor={id}
      className="flex cursor-pointer items-start gap-2 rounded-md border border-border/50 p-2 hover:bg-muted/30"
    >
      <input
        id={id}
        type="radio"
        name={name}
        value={value}
        checked={current === value}
        onChange={() => onChange(value)}
        className="mt-1"
      />
      <div className="text-xs">
        <div className="font-medium text-foreground">{label}</div>
        <div className="text-muted-foreground/70">{hint}</div>
      </div>
    </label>
  )
}
