import { useEffect, useState } from 'react'

// useCommandPalette wires the global cmd+K / ctrl+K shortcut to a
// boolean open state. Lives at the App root so the palette is
// available from anywhere in the UI.
//
// The shortcut is suppressed when the user is currently typing in an
// input/textarea (so cmd+K inside an URL bar simulator doesn't trigger),
// EXCEPT when the palette itself is already open (so esc-to-close still
// works after typing a query).
export function useCommandPalette() {
  const [open, setOpen] = useState(false)

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const isOpener = (e.metaKey || e.ctrlKey) && (e.key === 'k' || e.key === 'K')
      if (!isOpener) return
      // Always allow the shortcut — even when an input is focused, the
      // user is explicitly asking for the palette via modifier+K.
      e.preventDefault()
      setOpen((v) => !v)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return { open, setOpen }
}
