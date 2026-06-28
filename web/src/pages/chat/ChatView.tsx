import { useCallback, useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { getMeshStatus } from '@/api/client'
import { request } from '@/api/client'
import type { MeshMessage } from '@/api/types'
import { formatAgo } from '@/components/mesh/AgentRow'
import { Markdown } from '@/lib/markdown'
import { cn } from '@/lib/utils'
import { Send } from 'lucide-react'

export function ChatView() {
  const [messages, setMessages] = useState<MeshMessage[]>([])
  const [input, setInput] = useState('')
  const [sending, setSending] = useState(false)
  const bottomRef = useRef<HTMLDivElement>(null)

  const scrollToBottom = useCallback(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [])

  const fetchMessages = useCallback(async () => {
    try {
      const data = await getMeshStatus({ includeTaskEvents: false })
      setMessages(data.messages ?? [])
    } catch {
      // best-effort
    }
  }, [])

  useEffect(() => {
    fetchMessages()
    const interval = setInterval(fetchMessages, 4000)
    return () => clearInterval(interval)
  }, [fetchMessages])

  useEffect(() => {
    scrollToBottom()
  }, [messages, scrollToBottom])

  const handleSend = useCallback(async () => {
    const text = input.trim()
    if (!text || sending) return
    setSending(true)
    try {
      await request('/chat/send', {
        method: 'POST',
        body: JSON.stringify({ message: text }),
      })
      setInput('')
      await fetchMessages()
    } catch {
      // best-effort
    } finally {
      setSending(false)
    }
  }, [input, sending, fetchMessages])

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Enter' && !e.shiftKey) {
        e.preventDefault()
        handleSend()
      }
    },
    [handleSend],
  )

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center gap-2 border-b px-4 py-3">
        <h1 className="text-sm font-semibold">Chat</h1>
        <span className="text-xs text-muted-foreground">Mesh broadcast</span>
      </div>

      <div className="flex-1 overflow-y-auto px-4 py-3 space-y-3">
        {messages.length === 0 && (
          <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
            No messages yet
          </div>
        )}
        {messages.map((msg) => (
          <div key={msg.id} className="group">
            <div className="flex items-baseline gap-2">
              <span className="text-xs font-medium text-foreground">
                {msg.sender_display_name || msg.agent_name}
              </span>
              <span className="text-[10px] text-muted-foreground">
                {formatAgo(msg.created_at)}
              </span>
              {msg.kind && msg.kind !== 'chat' && (
                <span className="rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                  {msg.kind}
                </span>
              )}
            </div>
            <div
              className={cn(
                'mt-0.5 text-sm leading-relaxed',
                'prose prose-sm dark:prose-invert max-w-none',
              )}
            >
              <Markdown source={msg.content} />
            </div>
          </div>
        ))}
        <div ref={bottomRef} />
      </div>

      <div className="border-t px-4 py-3">
        <div className="flex gap-2">
          <Input
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Type a message..."
            disabled={sending}
            className="flex-1"
          />
          <Button
            size="icon"
            onClick={handleSend}
            disabled={!input.trim() || sending}
          >
            <Send className="h-4 w-4" />
          </Button>
        </div>
      </div>
    </div>
  )
}
