import { type ClassValue, clsx } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function formatRelativeTime(isoDate: string): string {
  const diff = new Date(isoDate).getTime() - Date.now()
  const absDiff = Math.abs(diff)
  const isPast = diff < 0

  const days = Math.floor(absDiff / (1000 * 60 * 60 * 24))
  if (days > 1) return isPast ? `${days}d ago` : `${days}d`
  const hours = Math.floor(absDiff / (1000 * 60 * 60))
  if (hours > 0) return isPast ? `${hours}h ago` : `${hours}h`
  const minutes = Math.floor(absDiff / (1000 * 60))
  if (minutes > 0) return isPast ? `${minutes}m ago` : `${minutes}m`
  return isPast ? 'just now' : 'soon'
}
