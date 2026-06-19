// audit-semantics — one source of truth for how an audit row's *meaning*
// is presented in the dashboard. Two concerns live here:
//
//  1. Status normalization. Most of the gateway writes status="success",
//     but the secrets resolver (and a few built-in emitters) write
//     status="ok". The UI used to render anything that wasn't the literal
//     string "success" with the destructive (red) badge — so every benign
//     secret.read / secret.list showed up looking like a failure. Normalize
//     once, here, and every surface agrees.
//
//  2. Secret-event classification. secret.list is *enumeration* (it reads
//     the KEY NAMES in a scope — no value is ever decrypted) while
//     secret.read is *decryption* (a stored value was unsealed and spliced
//     into a downstream call). Those are very different in blast radius and
//     must never look the same. The classifier gives each a plain-English
//     label, a calm-vs-attention tone, and a one-line explanation.

/** The three visual outcomes the dashboard styles audit rows by. */
export type StatusTone = 'success' | 'error' | 'blocked'

/**
 * normalizeStatus folds the raw audit status string into one of three
 * tones. "ok" (secrets resolver + some built-ins) is success, full stop.
 * Anything unrecognised is treated as an error so genuine failures are
 * never silently styled green.
 */
export function normalizeStatus(status: string | undefined | null): StatusTone {
  if (status === 'success' || status === 'ok') return 'success'
  if (status === 'blocked') return 'blocked'
  return 'error'
}

/** Convenience: true when the row succeeded (covers both "success" and "ok"). */
export function isSuccessStatus(status: string | undefined | null): boolean {
  return normalizeStatus(status) === 'success'
}

/** The four secret operations, ordered roughly by sensitivity. */
export type SecretOp = 'enumerate' | 'decrypt' | 'store' | 'delete'

export interface SecretSemantics {
  op: SecretOp
  /** Short chip text, e.g. "Enumeration". */
  label: string
  /**
   * info   — calm/blue: no secret value involved (enumeration).
   * notice — amber/attention: a value was unsealed or written.
   * neutral— slate: structural, low-signal.
   */
  tone: 'info' | 'notice' | 'neutral'
  /** One sentence a non-engineer can read and relax (or pay attention) to. */
  blurb: string
}

/**
 * classifySecretEvent maps a secrets-resolver tool_name to its semantics,
 * or null when the row isn't a secret event. Keyed on the stable
 * auditEventSecret* constants emitted by internal/secrets/audit.go.
 */
export function classifySecretEvent(toolName: string): SecretSemantics | null {
  switch (toolName) {
    case 'secret.list':
      return {
        op: 'enumerate',
        label: 'Enumeration',
        tone: 'info',
        blurb:
          'Listed the key names in this scope. No secret value was decrypted, read, or returned.',
      }
    case 'secret.read':
      return {
        op: 'decrypt',
        label: 'Decryption',
        tone: 'notice',
        blurb:
          'A stored secret was decrypted and substituted into a downstream call. The plaintext never enters an agent context or the audit log.',
      }
    case 'secret.write':
      return {
        op: 'store',
        label: 'Stored secret',
        tone: 'notice',
        blurb: 'A secret value was written to this scope.',
      }
    case 'secret.delete':
      return {
        op: 'delete',
        label: 'Deleted secret',
        tone: 'neutral',
        blurb: 'A secret key was removed from this scope.',
      }
    default:
      return null
  }
}

/**
 * isSecretsActor is true for rows emitted by the gateway's secret resolver
 * (client_type / actor_kind = "secrets"). Such rows are attributed to the
 * auth *scope* they touched, not to the agent that triggered them — the
 * triggering agent shares the row's correlation_id when one was recorded.
 */
export function isSecretsActor(record: {
  actor_kind?: string
  client_type?: string
}): boolean {
  return record.actor_kind === 'secrets' || record.client_type === 'secrets'
}
