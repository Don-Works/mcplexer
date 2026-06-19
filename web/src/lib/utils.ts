import { type ClassValue, clsx } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function formatRelativeTime(isoDate: string): string {
  const diff = new Date(isoDate).getTime() - Date.now()
  if (diff < 0) return 'expired'
  const days = Math.floor(diff / (1000 * 60 * 60 * 24))
  if (days > 1) return `${days}d`
  const hours = Math.floor(diff / (1000 * 60 * 60))
  if (hours > 0) return `${hours}h`
  return 'soon'
}
