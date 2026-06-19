# ADR 0002 — Skill signing scheme

- Status: Accepted (spike outcome)
- Date: 2026-04-30
- Deciders: project maintainers
- Related: R0.2 — Spike: skill signing scheme decision (ClickUp 86c9k55k1)
- Supersedes: —

## Context

mcplexer will let users package skills as `.mcskill` tarballs and share them
with each other. Installing a skill is, in effect, agreeing to execute its code
inside the gateway. We therefore need a signing scheme so that an install is
a trust decision against an identifiable signer, not a blind `curl | sh`.

Hard requirements:

- Verification must work fully offline (the gateway runs on a laptop, often
  on flights).
- The signer's public key must be trivially copy-pasteable (Slack, README,
  business card). PGP fingerprints lost this fight in 1998 and have not won
  since.
- Smallest possible operator footprint. mcplexer is a desktop / launchd app —
  we cannot stand up Fulcio/Rekor, and we don't want users to need to.
- We should not add a heavy dep tree to a project whose entire stdlib-plus-
  age stack is currently lean.

## Decision

**Use minisign (Ed25519, raw keys) via `aead.dev/minisign` for `.mcskill`
signing. Public keys are presented as the canonical minisign single-line
base64 string, prefixed with `mcskill:` in our UI for clarity but
interoperable with the upstream minisign format.**

Skills ship as `<name>-<version>.mcskill` (a tarball) plus a sibling
`<name>-<version>.mcskill.minisig`. mcplexer maintains a local trust store
(SQLite, in `internal/store`) of `(pubkey, label, first_seen_at, revoked_at)`.

## Comparison

Numbers below are from the spikes in `spike/signing/{signify,minisign,cosign}`.
"LoC" is the entire main.go for keygen + sign + verify + tamper test + pubkey
print.

| Dimension                         | signify-style (stdlib) | **minisign (aead.dev/minisign)** | cosign / sigstore |
|-----------------------------------|------------------------|-----------------------------------|-------------------|
| LoC for keygen+sign+verify        | 70                     | **59**                            | 73                |
| New direct deps                   | 0                      | **1** (`aead.dev/minisign`)       | 1 (`sigstore/sigstore`) |
| Modules pulled (transitive)       | 0                      | **3**                             | 15                |
| Binary size delta                 | 0 (baseline)           | **+0.1 MB**                       | +4.8 MB           |
| Trust model                       | raw key                | **raw key**                       | raw key OR keyless via OIDC + Fulcio + Rekor |
| Requires external infra           | no                     | **no**                            | no for `--key`; yes for full keyless value-prop |
| Key algorithm                     | Ed25519                | **Ed25519**                       | Ed25519/ECDSA/RSA |
| Pubkey wire format                | rolled by us           | **standard, single line**         | PEM (5 lines)     |
| Format compat with existing tools | none                   | **`minisign` CLI everywhere**     | `cosign` CLI      |
| Comment binding (skill id/ver)    | rolled by us           | **trusted comment, signed**       | annotations/tlog  |
| Revocation                        | local trust list       | **local trust list**              | Rekor or local trust list |
| First-time install UX             | "trust this 47-char string?" | **"trust `RWRZPIO…` (key id `C47DCBFC9E833C59`)?"** | "trust this 5-line PEM block?" or "verify against Sigstore Rekor" |

The signify-style stdlib option is genuinely tempting at zero deps, but
inventing our own armor format and key id scheme has a cost we will pay
forever. minisign already solved both, costs us 3 transitive modules
(only `aead.dev/minisign` + `golang.org/x/crypto` + `golang.org/x/sys`,
which `golang.org/x/crypto` we already have transitively from `filippo.io/age`),
and gives us a key id (truncated BLAKE2b) we can show in the UI for free.

cosign/sigstore is overweight on every axis that matters here: 5x the binary
delta, a protobuf+containerregistry transitive cone, and the actual selling
point — keyless via OIDC + Fulcio + Rekor — is exactly the infra dependency
we are trying to avoid. With `--key` (no Fulcio/Rekor) cosign gives us less
than minisign at higher cost.

PGP was rejected on UX grounds without a spike. WoT, web-of-keyservers,
fingerprint formats, key expiry vs revocation cert, and gpg(1) are all things
no human enjoys. Not relitigating it here.

## Pubkey representation

Canonical: minisign's own single-line format, exactly as the `minisign` CLI
emits it.

```
RWRZPIOe/Mt9xMRgnzdbJPLApp5bi6f24BZvJV9y0qOD29/k7fYZeZ1m
```

That is: `RWR` (signature algorithm marker for Ed25519) + 8-byte key id +
32-byte public key, all base64-encoded — 56 chars on one line.

In mcplexer UI / docs we present it as:

```
mcskill:RWRZPIOe/Mt9xMRgnzdbJPLApp5bi6f24BZvJV9y0qOD29/k7fYZeZ1m
```

The `mcskill:` prefix is purely a hint for humans pasting it into the gateway;
the verifier strips it before handing the bytes to `minisign.PublicKey.UnmarshalText`.
This means a key generated by upstream `minisign -G` is also a valid mcplexer
signer key — we get interop for free without taking on any new format design.

For ultra-short references (commit messages, release notes, support
conversations) we use the 8-byte key id as 16 hex chars: `C47DCBFC9E833C59`.
That is what gets shown in `mcplexer skills info` and in the "Trust this
signer?" install dialog.

Skill metadata signed via the trusted comment slot:

```
trusted comment: skill=acme/hello version=0.1.0 sha256=<digest>
```

Trusted comments are part of the signed payload in minisign, so this binds
the signature to a specific skill name + version and prevents replay across
skills.

## Revocation

We deliberately do not run a transparency log. Revocation is a local trust
store decision, augmented by an optional opt-in revocation feed.

1. **Local trust store** (`internal/store` table `skill_signers`): rows are
   `(pubkey, label, first_seen_at, revoked_at, revoke_reason)`. On install,
   if the signer is unknown, prompt: "Trust `mcskill:RWR…` (`C47DCBFC9E833C59`)
   for skill `acme/hello`?". If known and not revoked, install silently.
   If known and revoked, refuse and surface the revoke reason.

2. **Compromise procedure for a signer**: the signer publishes a signed
   "revocation note" (just another minisign signature, but over a sentinel
   string `mcplexer-revoke-self:<pubkey>:<utc-rfc3339>`). Anyone who has
   that pubkey trusted runs `mcplexer skills revoke --note <file>`, which
   verifies the note against the stored pubkey and flips `revoked_at`.
   Skills already installed are not auto-uninstalled — we surface a
   warning banner and let the operator choose. (Auto-uninstall is too
   destructive given the offline-first posture.)

3. **Optional revocation feed** (post-MVP): a single static URL
   (`https://mcplexer.dev/revocations.minisig`-style file) that mcplexer
   polls daily, signed by the project's own minisign key, listing revoked
   pubkeys. Opt-in; off by default; failure to fetch is non-fatal. This
   is the smallest "global" revocation primitive we can ship without
   running infra of our own beyond a static file.

4. **What this explicitly does not do**: no transparency log, no Rekor,
   no "the world agrees this signature existed at time T". For skill
   sharing among small numbers of trusting parties, that complexity is
   not justified.

## Consequences

Positive:

- One tiny direct dep (`aead.dev/minisign`).
- Verification path is ~50 lines of Go we own end-to-end.
- Users can sign mcplexer skills with the upstream `minisign` CLI if they
  prefer a familiar tool — no lock-in to mcplexer-only tooling.
- Pubkey is a single short string suitable for chat / Slack / README.
- Trusted comment slot gives us signed skill name + version binding for free.

Negative / accepted:

- Single-key trust model — losing the private key means rotating to a new
  pubkey and asking everyone who trusts you to re-trust the new one. This
  is by design; the alternative (X.509 chains, key delegation) is more
  rope than mcplexer needs.
- No global "this was published at time T" attestation. If we later need it
  we can layer Rekor or a project-run transparency log on top without
  changing the core signature format.

Open follow-ups (not in this ADR):

- Where does the signer's private key live on a developer's laptop?
  (Suggest reusing `internal/secrets` age-encrypted store; spike separately.)
- CLI ergonomics: `mcplexer skills sign <tarball>` vs upstream `minisign -S`.
- Whether to ship a `mcplexer-publisher` minisign key for the built-in skill
  catalogue, separate from user-generated skills.
