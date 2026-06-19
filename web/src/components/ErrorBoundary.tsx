import { Component, type ErrorInfo, type ReactNode } from 'react'
import { AlertTriangle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

interface ErrorBoundaryProps {
  children: ReactNode
  /** Optional override for the fallback UI. */
  fallback?: (error: Error, reset: () => void) => ReactNode
}

interface ErrorBoundaryState {
  error: Error | null
}

/**
 * Top-level error boundary. Catches render-time errors anywhere in the
 * subtree, logs them to `console.error` (so the Electron main process can
 * pick them up via the renderer log stream), and shows a clean fallback
 * with a "Reload" button.
 *
 * Intentionally does NOT auto-recover: if a render error happens once it
 * almost always happens again, so we ask the user to reload.
 */
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // Log to console so the error surfaces in devtools and is captured by
    // the Electron renderer log stream.
    console.error('[ErrorBoundary] render error:', error, info.componentStack)
  }

  private handleReload = (): void => {
    window.location.reload()
  }

  render(): ReactNode {
    const { error } = this.state
    if (!error) return this.props.children

    if (this.props.fallback) {
      return this.props.fallback(error, this.handleReload)
    }

    return (
      <div
        role="alert"
        className="flex min-h-screen w-full items-center justify-center p-6"
      >
        <Card className="w-full max-w-lg">
          <CardHeader>
            <div className="flex items-center gap-2 text-destructive">
              <AlertTriangle className="size-5" aria-hidden="true" />
              <CardTitle>Something went wrong</CardTitle>
            </div>
            <CardDescription>
              The app hit an unexpected error and can&apos;t continue. Reloading
              usually fixes it.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <pre className="bg-muted text-muted-foreground max-h-48 overflow-auto rounded-md p-3 text-xs whitespace-pre-wrap break-words">
              {error.message || String(error)}
            </pre>
          </CardContent>
          <CardFooter>
            <Button onClick={this.handleReload}>Reload</Button>
          </CardFooter>
        </Card>
      </div>
    )
  }
}
