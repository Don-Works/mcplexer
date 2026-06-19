import { Check } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface StepDef {
  id: string
  label: string
}

interface StepIndicatorProps {
  steps: StepDef[]
  currentStep: string
  completedSteps: string[]
}

export function StepIndicator({ steps, currentStep, completedSteps }: StepIndicatorProps) {
  const currentIndex = steps.findIndex((s) => s.id === currentStep)

  return (
    <div className="flex items-center gap-1">
      {steps.map((step, i) => {
        const isCompleted = completedSteps.includes(step.id)
        const isCurrent = step.id === currentStep
        const isPast = i < currentIndex

        return (
          <div key={step.id} className="flex items-center gap-1">
            {i > 0 && (
              <div
                className={cn(
                  'h-px w-6 transition-colors',
                  isPast || isCompleted ? 'bg-primary' : 'bg-border',
                )}
              />
            )}
            <div className="flex items-center gap-1.5">
              <div
                className={cn(
                  'flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-[10px] font-medium transition-colors',
                  isCompleted
                    ? 'bg-primary text-primary-foreground'
                    : isCurrent
                      ? 'border-2 border-primary bg-primary/10 text-primary'
                      : 'border border-border text-muted-foreground/50',
                )}
              >
                {isCompleted ? <Check className="h-3 w-3" /> : i + 1}
              </div>
              <span
                className={cn(
                  'hidden text-[11px] tracking-wide sm:inline',
                  isCurrent
                    ? 'font-medium text-foreground'
                    : isCompleted
                      ? 'text-muted-foreground'
                      : 'text-muted-foreground/50',
                )}
              >
                {step.label}
              </span>
            </div>
          </div>
        )
      })}
    </div>
  )
}
