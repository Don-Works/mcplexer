/**
 * Calculates the next SSE reconnection delay using exponential backoff.
 * Used by use-audit-stream and use-approval-stream hooks.
 *
 * @param retryCount - Number of consecutive retry attempts (0-based)
 * @param initialDelayMs - Base delay in milliseconds (default: 1000)
 * @param maxDelayMs - Maximum delay cap in milliseconds (default: 30000)
 * @returns Delay in milliseconds before next reconnection attempt
 */
export function getBackoffDelay(
  retryCount: number,
  initialDelayMs = 1000,
  maxDelayMs = 30000,
): number {
  return Math.min(initialDelayMs * 2 ** retryCount, maxDelayMs)
}
