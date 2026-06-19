import { useCallback, useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Loader2, Plug } from 'lucide-react'
import { toast } from 'sonner'
import { p2pFetch } from './api'
import { parsePairingInput } from './parse'

// EnterCodeModal is the OTHER side of pairing: the user pastes the
// payload (JSON or `mcplexer://pair/...` URL) copied from device A and
// types the 6-digit code displayed there. The daemon resolves A's
// current addresses via the DHT and runs the libp2p handshake. The
// user never sees ports/IPs/peer-IDs.
interface EnterCodeModalProps {
  onComplete: () => void
  // initialPayload pre-fills the textarea with a pasted URL or QR JSON.
  // Used by the Electron mcplexer:// deeplink path — see PairingPage.
  initialPayload?: string
  // autoOpen pops the dialog without the user clicking the trigger
  // button. Pairs with initialPayload for a single-tap deeplink flow.
  autoOpen?: boolean
}

export function EnterCodeModal({
  onComplete,
  initialPayload = '',
  autoOpen = false,
}: EnterCodeModalProps) {
  const [open, setOpen] = useState(autoOpen)
  const [code, setCode] = useState('')
  const [payload, setPayload] = useState(initialPayload)
  const [busy, setBusy] = useState(false)

  // Mirror the props back into local state when the parent supplies a new
  // deeplink URL after mount (e.g. when the user activates a second
  // mcplexer:// URL while the modal is closed).
  useEffect(() => {
    if (!initialPayload) return
    setPayload(initialPayload)
    setOpen(true)
    // Also try to pre-fill the code from the URL so the user can submit
    // immediately when the URL embeds the code (it always does for the
    // mcplexer:// scheme).
    const parsed = parsePairingInput(initialPayload)
    if (parsed?.code) setCode(parsed.code)
  }, [initialPayload])

  const submit = useCallback(async () => {
    const parsed = parsePairingInput(payload)
    if (!parsed) {
      toast.error('Paste the pairing payload or a mcplexer:// URL from the other device')
      return
    }
    const finalCode = (code || parsed.code || '').trim()
    if (!parsed.peer_id || finalCode.length !== 6) {
      toast.error('Need a 6-digit code and a peer ID')
      return
    }
    setBusy(true)
    try {
      await p2pFetch<void>('/pair/complete', {
        method: 'POST',
        body: JSON.stringify({
          code: finalCode,
          peer_id: parsed.peer_id,
          // display_name is forwarded from the QR payload (set by the
          // far-side StartPair when its DisplayNameProvider is wired).
          // Server falls back to the peer-prefix label when absent.
          display_name: parsed.display_name ?? '',
          ...(parsed.user_id ? { user_id: parsed.user_id } : {}),
        }),
      })
      toast.success('Device paired')
      setOpen(false)
      setCode('')
      setPayload('')
      onComplete()
    } catch (e) {
      toast.error(`Pair failed: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }, [code, payload, onComplete])

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" data-testid="pair-enter-code-btn">
          <Plug className="mr-2 h-4 w-4" />
          Enter code
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Pair with another device</DialogTitle>
          <DialogDescription>
            Paste the payload from the other device's pairing dialog, then
            type its 6-digit code below.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="space-y-1.5">
            <Label htmlFor="qr-payload" className="text-xs font-medium text-muted-foreground">Pairing payload</Label>
            <Textarea
              id="qr-payload"
              data-testid="pair-payload-input"
              rows={4}
              className="font-mono text-[11px] leading-relaxed"
              placeholder={'{"code":"…","peer_id":"…"}'}
              value={payload}
              onChange={(e) => setPayload(e.target.value)}
              autoFocus
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="pair-code" className="text-xs font-medium text-muted-foreground">6-digit code</Label>
            <Input
              id="pair-code"
              inputMode="numeric"
              maxLength={6}
              placeholder="123456"
              className="mx-auto block w-full max-w-[14rem] text-center font-mono text-2xl tracking-[0.25em] pl-[0.25em] tabular-nums h-14"
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)} data-testid="pair-enter-cancel">Cancel</Button>
          <Button onClick={submit} disabled={busy} data-testid="pair-submit-btn">
            {busy && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            Pair
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
