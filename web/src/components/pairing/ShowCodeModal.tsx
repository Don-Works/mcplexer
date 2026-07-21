import { useCallback, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Check, Copy, Loader2, Plug } from 'lucide-react'
import { toast } from 'sonner'
import { formatExpiry, p2pFetch, type PairStartResponse } from './api'

// ShowCodeModal renders the pairing payload + 6-digit code. The user
// copies the payload, pastes it on the other device, types the 6-digit
// code there. No QR — desktop-to-desktop only, copy-paste is plenty.
export function ShowCodeModal({ onComplete }: { onComplete: () => void }) {
  const [open, setOpen] = useState(false)
  const [data, setData] = useState<PairStartResponse | null>(null)
  const [busy, setBusy] = useState(false)
  const [copied, setCopied] = useState(false)
  const [, setNow] = useState(Date.now())

  useEffect(() => {
    if (!open) return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [open])

  const start = useCallback(async () => {
    setBusy(true)
    try {
      const res = await p2pFetch<PairStartResponse>('/pair/start', { method: 'POST' })
      setData(res)
    } catch (e) {
      toast.error(`Pair start failed: ${(e as Error).message}`)
      setOpen(false)
    } finally {
      setBusy(false)
    }
  }, [])

  useEffect(() => {
    if (open && !data) void start()
    if (!open) {
      setData(null)
      setCopied(false)
    }
  }, [open, data, start])

  const copy = useCallback(async () => {
    if (!data) return
    try {
      await navigator.clipboard.writeText(data.qr_payload)
      setCopied(true)
      toast.success('Pairing payload copied')
      setTimeout(() => setCopied(false), 2000)
    } catch {
      toast.error('Copy failed — select and copy manually')
    }
  }, [data])

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button data-testid="pair-show-code-btn">
          <Plug className="mr-2 h-4 w-4" />
          Pair this device
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Pair a new device</DialogTitle>
          <DialogDescription>
            Copy the payload, paste it on the other device, then type the
            6-digit code there. The code expires in 5 minutes.
          </DialogDescription>
        </DialogHeader>
        {busy && (
          <div className="flex items-center justify-center py-12">
            <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
          </div>
        )}
        {data && (
          <div className="flex flex-col gap-5 py-2">
            <div className="rounded-lg border bg-muted/40 px-6 py-5 text-center">
              <div
                data-testid="pair-code"
                className="font-mono text-5xl font-semibold tracking-[0.4em] pl-[0.4em] text-foreground tabular-nums"
              >
                {data.code}
              </div>
              <div className="mt-2 text-xs text-muted-foreground">
                expires in {formatExpiry(data.expires_at)}
              </div>
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <div className="text-xs font-medium text-muted-foreground">Pairing payload</div>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={copy}
                  data-testid="pair-payload-copy"
                  className="h-7 gap-1.5 text-xs"
                >
                  {copied ? (
                    <>
                      <Check className="h-3.5 w-3.5" /> Copied
                    </>
                  ) : (
                    <>
                      <Copy className="h-3.5 w-3.5" /> Copy
                    </>
                  )}
                </Button>
              </div>
              <code
                data-testid="pair-payload-text"
                className="block w-full select-all rounded border bg-muted px-3 py-2 font-mono text-[11px] leading-relaxed break-all"
              >
                {data.qr_payload}
              </code>
              <p className="text-[11px] text-muted-foreground">
                Paste this into the other device's "Enter code" dialog.
              </p>
            </div>
          </div>
        )}
        <DialogFooter>
          <Button
            variant="outline"
            data-testid="pair-show-done"
            onClick={() => { setOpen(false); onComplete() }}
          >
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
