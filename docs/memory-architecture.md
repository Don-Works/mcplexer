# Memory architecture: from a ranking bug to memory that behaves like a human's

Status: design doc / working roadmap. Companion to `docs/memory.md` (which is
the short-form reference for what exists). This doc records **why the ranking
defect happened**, **what the eval now guarantees**, and **the four capabilities
mcplexer's memory subsystem does not yet have**.

Every claim about the codebase carries a `file:line`. Where the research is
speculative, it says so. Where two research pillars contradict each other, the
tension is named rather than averaged away.

---

## 1. What was broken, and why it got worse as the corpus grew

### 1.1 The defect

`internal/memory/registry.go:997` `Recall()` has two rerank paths and production
ran the wrong one.

- `foldRecencyPin` (`internal/memory/rank.go:190`) applies recency/pin as a
  **bounded additive sub-position-step nudge**, sized by `foldRecencyEpsilon`
  (`rank.go:219`) so it can only break a near-tie. This is the correct design and
  it was already in the tree.
- `rerankHits` (`rank.go:58`) applied recency as an **unbounded multiplier**:
  `score = base * recencyFactor * pinnedBoost * (1 + recallBoostMax*signal)`.

`foldRecencyPin` only runs when a cross-encoder reranker is configured
(`registry.go:1089`, gated on `s.reranker.HasModel()`), which requires
`MCPLEXER_RERANK_BASE_URL` (read once, at `cmd/mcplexer/serve.go:1100`). That
variable is not set in the live launchd environment. **Production therefore ran
`rerankHits` — the unbounded path — on every recall.**

The arithmetic, measured against the live 787-row store:

```
base(i)   = rankBlendAlpha·pos(i) + (1−rankBlendAlpha)·pos(i)·norm(i)
pos(i)    = 1/(rerankBaseK + i + 1),  rerankBaseK = 60,  rankBlendAlpha = 0.85
                                          (internal/memory/rank_tuning.go:29,109)

relevance dynamic range over a 40-deep pool
   position component : (1/61)/(1/100)            = 1.64×
   signal-blend       : 1/0.85                    = 1.18×
   total              :                             1.93×

recency dynamic range (oldest live memory ≈ 50 days)
   recencyFactor      = recencyFloor + (1−recencyFloor)·0.5^(age/30d)
                                          (rank_tuning.go:17,27,153)
   0.5^(50/30) = 0.315 → factor 0.349    → 2.88× over the observed corpus
   theoretical span 1.0 → 0.05           → 20×  over an unbounded corpus
```

So the score a hit could earn from *being fresh* had between 2.9× and 20× the
dynamic range of the score it could earn from *being relevant*. Concretely: a
maximally-fresh, **least**-relevant document at pool rank 39 scored
`0.010 · 0.85 · 1.0 = 0.0085`, which beats the **most** relevant document in the
store if that document is ~7 weeks old: `0.0164 · 0.348 = 0.0057`. Relevance was
decorative. The live symptom was reproducible: the query
`"why did we choose sqlite over postgres"` returned a blog voice-guide memory at
rank 1, and the first topically correct hit at rank 4.

### 1.2 Why it scaled badly

This is the important part, because it explains why the subsystem passed its
gate for a year and then visibly degraded.

The failure requires the candidate pool to be **saturated with fresh
distractors**. The FTS5 arm builds its query by OR-joining query terms
(`internal/store/sqlite/memory_query.go:652`), so any memory sharing a generic
connective reaches the pool. At N≈50 memories, a 40-deep pool is most of the
corpus and rarely contains a fresh, topically unrelated document that also
outranks the correct one on recency. At N=787 it *always* does — the pool is 5%
of the corpus and is filled by whatever was written this week.

Precision therefore decays roughly as **1/N**: each new fresh memory is another
lottery ticket in the "beat the correct answer on recency" draw, and the correct
answer's only defence is a 1.93× relevance spread it cannot grow. The fused path
is worse than the FTS path, not better, because `Recall` pools only `k*2 = 20`
candidates for the fused arm — a narrower position band (1/61…1/81 = 1.33×) and
therefore *less* relevance authority to defend with.

Restated as a rule worth keeping: **any ranking term whose dynamic range exceeds
the dynamic range of the relevance base is the primary sort key wearing a
disguise.** The disguise gets more effective as the corpus grows, because the
relevance base is bounded by pool depth and the corpus is not.

### 1.3 The fix that landed

`rank.go:58` `rerankHits` now computes:

```
score(i) = base(i) + rerankNudgeSteps · positionStep(i) · (normRecency + pinned + recallSignal)
positionStep(i) = base_pos(i) − base_pos(i+1)        (rank_tuning.go:144)
rerankNudgeSteps    = 1.6                            (rank_tuning.go:64)
rerankMaxNudgeSteps = 3 × 1.6 = 4.8                  (rank_tuning.go:73)
```

Each of the three terms is in `[0,1]`. Scaling by the **local adjacent step**
rather than a constant epsilon is the load-bearing choice: position gaps shrink
as ~1/i², so a fixed epsilon is a gentle nudge at the head of the pool and a
re-sort at its tail — exactly the regime a large store lives in. In step units,
every depth now gets identical authority: one term at full signal climbs ~1.55
positions and provably never 2. All three terms maxed cap at 4.8 steps against a
20-deep pool spread of 14.7 steps.

Collateral cleanups in the same change: `pinnedBoost` (a 1.5× multiplier with the
same unbounded problem) is gone, folded into the shared step budget;
`recencyFloor` is now inert for ordering because both paths consume
`recencyFactor` through the `(f−floor)/(1−floor)` rescale (`normRecency`,
`rank_tuning.go:165`); and `rrfFuse` (`registry.go:1734`) was made deterministic —
it previously built its sort input by ranging a Go map, so equal RRF scores
(common, since rank *r* of either arm scores exactly `1/(k+r+1)`) resolved
differently per call. That non-determinism is why "recall is flaky" reports were
real, and it would have silently poisoned any A/B of a ranking change.

### 1.4 Two related defects found during the work, not yet fixed

Both are recorded here because they are the same class of bug — a write clock
being used as a use clock — and both are live.

**(a) Re-embedding resets the corpus age distribution.**
`internal/store/sqlite/memory_query.go:174-177` — `UpsertMemoryEmbedding` runs
`UPDATE memories SET embed_model=?, embed_version=?, updated_at=?` with
`time.Now()`. `internal/memory/backfill.go:113` calls it for every unembedded row.
So **switching or backfilling an embedder model stamps `updated_at = now` across
the whole corpus**, flattening every age signal the ranker depends on. This was
first observed inside the eval harness (it silently un-backdated the fixture
corpus, `internal/memory/eval/harness.go:111`), and the harness works around it
by restoring the intended timestamp. Production has no such workaround. Fix:
drop `updated_at` from that UPDATE, or add a separate `embedded_at` column. This
is a one-line change and should land before any decay work.

**(b) Invalidation leaves a live vector row and eats KNN slots.**
`memory.go:367-378` `InvalidateMemory` stamps `t_valid_end` and does *not* delete
the `memories_vec` row — unlike `SoftDeleteMemory` (`memory.go:382-399`), which
does. `VectorSearchMemories` (`memory_query.go:126-132`) runs
`WHERE v.embedding MATCH ? AND v.k = ?` and applies `memoryWhere` (scope,
`deleted_at`, `t_valid_end`) **after** the join. vec0 returns exactly *k* rows, so
every invalidated row silently consumes one. **The more the consolidator works,
the fewer live vector hits recall gets** — consolidation currently degrades the
vector arm, silently. Fix both ends: `DELETE FROM memories_vec` inside
`InvalidateMemory`'s transaction, and over-fetch (`v.k = k*4`) then truncate after
the join.

---

## 2. The measurement contract

### 2.1 Why the eval is the load-bearing artifact

`internal/memory/eval/` was already a real gate — `TestRetrievalQualityGate`
asserted recall@k ≥ 0.90, nDCG ≥ 0.85, MRR ≥ 0.85 — and it was **structurally
blind** to the defect above, for three independent reasons:

1. **10 documents / 10 queries.** The pool never saturates, so the failure mode
   (fresh distractors filling the pool) cannot occur.
2. **FTS5-only** (`NoopEmbedder`), so `Recall` took the `ftsFallback` early return
   at `registry.go:1051` and never exercised the `rrfFuse` → `mmrPool` →
   `rerankHits` path production actually runs.
3. **Every fixture was seeded at test time**, so `UpdatedAt ≈ now` for all rows
   and `recencyFactor` was uniformly 1.0. **The recency multiplier was
   mathematically inert inside the gate.** A term that cannot vary cannot be
   tested.

This is the general lesson and it is worth stating as a rule: *a quality gate
that does not reproduce the production distribution measures the fixture, not the
system.* Three properties matter here — corpus size, retrieval path, and the age
distribution — and all three were wrong.

### 2.2 What the eval now models

| Property | Before | Now |
|--|--|--|
| Corpus size | 10 docs | 500 (FTS arm) / 190 (fused arm) |
| Retrieval path | FTS only, `ftsFallback` | both; fused arm runs `rrfFuse` + `mmrPool` + `rerankHits` |
| Age distribution | all `now` | gold backdated 45–90d; noise < 120h |
| Metrics | recall@k, nDCG@k, MRR | + precision@1, + per-query `TopKey` |
| Attribution | none | age-flattened control arm |

Mechanics:

- **Backdating seam, no production change.** `(*sqlite.DB).WriteMemory`
  (`internal/store/sqlite/memory.go:24`) only defaults `CreatedAt`/`UpdatedAt`/
  `TValidStart` when zero, so the harness writes backdated rows straight to the
  store. `FixtureMemory` gained `UpdatedAt` + `Pinned`; `Harness.seedOne`
  (`eval/harness.go:89`) branches — a zero `UpdatedAt` keeps the original
  `memory.Service.Write` path, so the pre-existing gate and the lifecycle tests
  are byte-for-byte unaffected.
- **Programmatic corpus** (`eval/corpus_scaled.go`, 179 lines — generated, not a
  hand-written literal, per the repo's file-size rules). `ScaledCorpus(now, n)`
  reuses the 10 hand-labelled `DefaultCorpus` queries and gold docs verbatim,
  backdates gold evenly across 45–90 days (RELEVANT-BUT-OLD), and generates *n*
  topically disjoint fresh docs under 120h old (IRRELEVANT-BUT-FRESH). All
  synthetic and neutral: voice guides, standup notes, recipe cards, gym logs.
- **Offline fusion.** `hashEmbedder` (`eval/embedder_test.go`) is a hashed
  bag-of-words projection into `EmbedDim=1536`, L2-normalised, deterministic,
  zero network. `NewHarnessWithEmbedder` / `EvaluateWith`
  (`eval/harness.go:48,228`) upsert vectors synchronously so the fused arm is
  really exercised.
- **precision@1** was added because P@1 *is* the production symptom ("#1 is an
  unrelated doc"), and it collapses where mean nDCG@5 merely sags.

### 2.3 The control arm — why this is evidence and not a tuned failure

`TestScaledRetrievalAgeFlattenedControl` runs the **same** corpus, pool and
queries with `FlattenAges()` applied: one variable removed, `recencyFactor`
uniform and therefore inert. It asserts (a) **attribution** — flattening moves the
metric — and (b) **solvability** — with recency inert the FTS arm scores the
corpus perfectly. Without (b) you cannot distinguish "recency broke it" from "the
corpus is too hard".

Baseline against unmodified `rank.go`:

```
fts-only  500 docs k=10 : recall@10 0.400  ndcg 0.363  mrr 0.350  p@1 0.300   FAIL
fused-rrf 190 docs k=10 : recall@10 0.000  ndcg 0.000  mrr 0.000  p@1 0.000   FAIL
control (ages flattened), fts-only : 1.000 / 1.000 / 1.000 / 1.000
attribution delta: fts-only MRR +0.650, P@1 +0.700; fused MRR +0.739, recall +1.000
```

After the fix:

```
fts-only  500 docs k=10 : recall@10 1.000  ndcg 1.000  mrr 1.000  p@1 1.000   PASS
fused-rrf 190 docs k=10 : recall@10 1.000  ndcg 0.792  mrr 0.735  p@1 0.700   PASS
```

Note what happened to the control assertion when the bug was fixed. The original
`assertAgeAttribution` ("flattening ages must *recover* ≥ 0.20 MRR") can only pass
while recency dominates — it encoded the bug as a spec. It was inverted into
`assertRecencyBounded`: the aged run must **converge** on its own recency-inert
control within 0.05 on MRR/nDCG/P@1/recall. Same evidence, read the other way,
and a strictly stronger regression test. Verified by temporarily restoring the
multiplicative formula: the new assertion fails loudly.

### 2.4 The honest caveat on the fused arm

The fused control tops out at nDCG 0.796 / P@1 0.700 even with recency made
completely inert. That is `hashEmbedder`'s ceiling — a hashed bag-of-words has no
synonymy, so it injects its own noise into the vector arm. The fused scenario is
therefore held to **recall@10 ≥ 0.90** (which the bug *did* destroy: 0.000 →
1.000) plus the convergence assertion, and **not** to absolute ordering floors it
cannot reach. Holding it higher would make it a test of the fake embedder. This
is the one threshold that was deliberately narrowed and it is called out so
nobody "tightens" it back without reading this paragraph.

### 2.5 The rule

> **No ranking change lands without moving a number in `internal/memory/eval/`.**

Corollaries, all of which have already bitten:

- If you add a scoring axis and the eval does not move, you have added a knob,
  not a signal. Revert it.
- If a proposed change makes an existing eval assertion fail, decide explicitly
  whether the assertion is a real invariant or an encoding of the bug — and write
  down which, in the test. (`TestRerankHitsRecencyDemotesStale`,
  `rank_internal_test.go:70`, looks like the bug stated as a spec and is not: it
  asks fresh to climb exactly one rank over stale, which is precisely the budget
  the fix preserves. It passes untouched, and now carries a comment saying so.)
- The eval must be extended *before* the mechanism it measures. Every section
  below therefore names its metric before its implementation.
- Cross-system benchmark numbers (LoCoMo, DMR, LongMemEval leaderboards) are
  useful for understanding a *mechanism* and worthless as a go/no-go here. They
  are author-reported on conversational transcripts, not on a 787-row engineering
  decision store. Local eval or it didn't happen.

### 2.6 Coverage gap to close before it bites again

Enabling the cross-encoder flips `Recall` from `rerankHits` to
`crossEncoderReorder` + `foldRecencyPin` **and deliberately skips `mmrPool`**
(`registry.go:1090-1096`). The eval harness uses `NoopReranker`, so in that
configuration **the gate would no longer cover the production ranking path at
all** — exactly the structural blindness of §2.1, one config flag away. A third
scaled scenario with a deterministic fake cross-encoder must land before the
reranker is enabled anywhere.

(For reference: `HTTPReranker` (`internal/memory/rerank_provider.go`) already
speaks the shared Jina/Cohere/OpenAI `/rerank` shape and validates coverage
strictly. Enablement is env-only — the sole reads are `os.Getenv` at
`cmd/mcplexer/serve.go:1100-1102`. There is no persisted setting, no dashboard
field, and no `DetectLocalRerankEndpoint` analogous to
`cmd/mcplexer/memory_embedder.go:65`. Note also that the existing local-endpoint
probe list is wrong for reranking: LM Studio (`:1234`) and Ollama (`:11434`) do
not expose `/v1/rerank` at all. llama.cpp built with reranking, HF
text-embeddings-inference, and infinity do.)

---

## 3. The four gaps

mcplexer's memory subsystem is a well-built **retrieval** system with almost no
**memory-formation** machinery. Retrieval is five fused axes over a bi-temporal
store. Formation is: a `pinned` boolean.

| Capability | Human memory | mcplexer today |
|--|--|--|
| Encoding salience | amygdala/dopaminergic modulation of consolidation | `pinned bool`, manual, the only write-time signal that reaches ranking |
| Consolidation to gist | hippocampal replay → neocortical schema | a nightly near-duplicate compactor over the 50 most-recent notes, env-gated off |
| Forgetting | interference management; retrieval-induced suppression | none. `DecayPressure` computed, display-only, zero consumers |
| Reconstruction | cue-driven pattern completion | none. `summary` is `fmt.Sprintf("%d memories recalled")` |

Each section below: what biology does, what the code does (with line numbers),
the smallest useful implementation, the metric, and the anti-patterns.

---

### 3.1 Encoding-time salience

#### What biology does

Importance is **not** stamped synchronously at encoding. McGaugh's programme
(Annu. Rev. Neurosci. 2004; PNAS 2013) shows that post-training β-adrenergic
infusions into the basolateral amygdala *enhance* retention and post-training
antagonists *impair* it — the drug is given **after** the experience. Consolidation
is a time-limited window (minutes to hours) during which a trace's importance is
still revisable by what happened next. A system that stamps importance once at
ingestion and freezes it is modelling the wrong thing.

Two mechanisms compound this:

- **Synaptic tagging and capture** (Frey & Morris, Nature 1997; behavioural
  version, Moncada & Viola, J. Neurosci. 2007): a weak trace sets a transient tag;
  a strong event *nearby in time* triggers plasticity-related proteins that any
  still-tagged synapse captures, converting the weak trace into a lasting one.
  Salience is **retroactive** and spreads over a temporal neighbourhood with a
  decaying kernel. No agent memory system in the survey (MemGPT, Mem0, Zep, A-MEM,
  LangMem) implements anything like it.
- **Goal-relevance gates encoding prospectively** (Adcock et al., Neuron 2006;
  Lisman & Grace hippocampal–VTA loop, Neuron 2005): mesolimbic activation
  *preceding* stimulus onset predicts later recollection. Anticipated value, not
  post-hoc interest.

Engineered analogues worth naming: Generative Agents (Park et al., UIST 2023) is
the only production-shaped system with an explicit numeric write-time importance —
an LLM 1–10 poignancy rating, persisted, used both in retrieval and as the
reflection trigger (fires when accumulated importance crosses 150). Its retrieval
weights are the empirical prior worth stealing: **relevance 3 > importance 2 >>
recency 0.5**. Titans (arXiv:2501.00663) makes surprise computable rather than
judged — write strength = gradient magnitude of an associative loss, accumulated
with momentum so salience persists for a window after the surprising event.
Prioritized Experience Replay (ICLR 2016) contributes the two safety rules:
always keep an ε floor so nothing reaches zero priority, and if you bias sampling
by importance you have introduced estimator bias that needs an explicit
counterweight.

#### What mcplexer does today

`pinned` is the only importance signal captured at save time and the only one
that reaches ranking.

- `WriteOptions` (`internal/memory/registry.go:401-423`) carries
  `Name, Kind, Content, Tags, Metadata, WorkspaceID, UserID, WorkerID, RunID,
  Source*, OriginPeerID, Pinned, Entities`. No importance, salience, surprise,
  novelty, goal-relevance or confidence field.
- `store.MemoryEntry` (`internal/store/models.go:849-874`) likewise: the only
  non-provenance qualifier is `Pinned bool` (`models.go:870`).
- The tool schema (`internal/gateway/builtin_tools_memory.go:24`) documents
  `pinned` as *"Pin this memory so the consolidator won't auto-prune it"* — i.e.
  it is presented as a retention flag, not an importance signal, and users treat
  it that way.
- The only other write-time filter is negative: `minSaveContentChars = 8`
  (`rank.go:14`), enforced at `registry.go:463`.

**The surprise signal already exists and is thrown away.** `surfaceContradictions`
(`registry.go:580`) runs on **every** note write: lexical Jaccard overlap always,
plus vector neighbours when an embedder is live, with thresholds at
`registry.go:380-397` (`contradictionScanCap=3`, `contradictionMinJaccard=0.5`,
`contradictionDupJaccard=0.6`). `WriteWithResult` (`registry.go:450`) calls it,
passes the result to `recordConflicts` (`registry.go:526`) → `RecordMemoryConflicts`
(`internal/store/sqlite/memory_conflicts.go:21`, migration
`117_memory_conflicts.sql`), and returns the candidates to the caller. The header
comment (`registry.go:316-318`) is explicit that it is **advisory only**: nothing
is auto-invalidated, and the consumers are `ListOpenConflicts` (`registry.go:547`)
and `ResolveConflict` (`registry.go:557`) — a dashboard review queue. **Nothing
writes the result back onto the memory row, and no ranking code reads it.**

A conflict firing on save *is* a prediction error: the new content contradicts
what memory predicted. That is precisely Titans' surprise term, computed for free,
and discarded.

#### Smallest useful implementation

One migration, no LLM in the write path.

**Migration `151_memory_salience.sql`** (next free number; `150_monitoring_incident_actions.sql`
is current head):

```sql
ALTER TABLE memories ADD COLUMN importance REAL;   -- NULL = unscored
ALTER TABLE memories ADD COLUMN novelty    REAL;
ALTER TABLE memories ADD COLUMN utility    REAL;   -- written by the consolidator, §3.3
```

All nullable. **Every consumer must treat NULL as neutral 0.5** so the ~787
existing rows do not regress.

**Save path**, `internal/memory/registry.go:450` `WriteWithResult`, after the
existing `surfaceContradictions` call:

1. `novelty` — free. The embedding is already computed on the save path
   (`internal/memory/embed.go`, upserted async at `registry.go:1687`). Compute
   `novelty = 1 − max cosine` against the top-10 same-scope active neighbours
   using the vector you just built. That is Mem0's redundancy check and SAGE's
   novelty gate with no LLM call and no network hop. **Do not gate the write on
   it** (see anti-patterns).
2. `goalRelevance` — **mcplexer's unfair advantage, and nothing else in the survey
   has it.** No other agent memory system sits next to a durable task ledger. At
   save time, if the session holds a claimed task lease, stamp the task id into
   `metadata_json` and set the term. This is the Adcock/VTA prospective-value
   signal, available for free, and it is strictly better evidence of importance
   than any text-salience judgement.
3. `arousal` — use **consequence, not sentiment**. The amygdala analogue in this
   codebase is the monitoring subsystem (`internal/logwatch/distill/incident.go`,
   the monitoring triage handlers). A memory saved while an incident is open, or
   naming an entity referenced by an open incident, gets the term.
4. `surprise` — read the `surfaceContradictions` result that is already in hand.

Then, bounded and monotone:

```go
importance = clamp(0.35 +
    0.30*novelty +
    0.15*hasActiveTask +
    0.25*conflictTriggered +
    0.20*incidentOpen +
    0.30*pinned, 0.1, 1.0)
```

The 0.1 floor is PER's ε: a memory that can never be sampled can never accumulate
the evidence that would raise its score.

**Retroactive pass (the McGaugh window), in the consolidator.** When a memory
lands above a high-water mark, propagate a decaying boost to memories from the
same `source_session_id` within ±30 minutes:
`importance_j += δ · exp(−|t_i − t_j| / τ)`. This is Frey/Morris tag-and-capture,
it costs one indexed query per high-salience row, and it targets the real failure
mode of an agent memory store: **the boring setup note written five minutes before
the incident is exactly the note you need and exactly the one nothing scores
highly.** Also apply surprise retroactively to the *superseded* row — a fact that
turned out to be wrong is a high-value fact about the world's volatility.

**Ranking**, `rank.go:58`. Fold `importance` into the *existing shared nudge
budget*, not as a new independent term:

```go
nudge := normRecency(...) + pinned + recallSignal(...) + importanceTerm
```

with `rerankMaxNudgeSteps` raised from `3*rerankNudgeSteps` to `4*rerankNudgeSteps`
= 6.4 steps, still far below the 14.7-step pool spread. The invariant asserted by
`TestRerankHitsNudgeBudgetArithmetic` must be updated *and must still hold*. Do
**not** add importance to a raw RRF sum — RRF scores are uncalibrated and an
additive term's effect swings with candidate-set size.

Target effective mass, following Generative Agents' fitted prior:
**relevance : importance : recency ≈ 3 : 2 : 0.5**. Today, post-fix, recency and
pin share the whole 4.8-step budget; importance should take the larger share of it
and recency should shrink.

#### How to measure it

New eval scenario, `eval/corpus_salient.go` + `TestScaledRetrievalSalience`:

1. **Salience-graded corpus.** Extend `FixtureMemory` with `Importance *float64`
   and `ConflictSeed bool`. Generate a corpus where the gold document for each
   query is *not* the freshest and *not* the highest lexical scorer, but is the
   one carrying the salience markers (written under an open task, contradicting a
   prior note). Metric: **P@1 and nDCG@10 with the importance term on vs. off**,
   same corpus, same seed. If P@1 does not move, revert.
2. **Tag-and-capture scenario.** Seed a session: three low-salience setup notes,
   then one high-salience incident note, all within 30 minutes and sharing
   `source_session_id`. Query for the setup content with terms that do not appear
   in the incident note. Metric: **recall@10 of the setup notes before and after
   the retroactive pass.** This is the only test that can detect whether
   behavioural tagging did anything, and it is the one mechanism here with no
   prior art to copy from.
3. **The orthogonality check — mandatory, and it is a gate.** Compute
   `corr(importance, created_at)` across the real corpus and assert it in a test
   against a ceiling (start at |r| < 0.3). If every importance term correlates
   with write time ("recent work is important", "active session", "current
   sprint"), you have not added an orthogonal axis — you have re-implemented the
   §1 defect with more code. This is the single most likely way this section goes
   wrong.
4. **Combined-multiplier cap.** Assert in `rank_internal_test.go` that
   `pinned + max importance` cannot climb more than N positions. Without it the top
   of every result set converges on the same handful of pinned, high-importance
   rows regardless of query.

#### What not to do

- **No synchronous LLM importance call on the save path.** Generative Agents can
  afford one LLM call per observation because it is a simulation with nobody
  waiting. `memory.save` is an interactive tool call — a model round-trip adds
  latency, adds a failure mode that can *lose the write*, and is the first thing
  anyone disables under load.
- **No 1–10 LLM integer as a ranking axis.** LLM integer scales are badly
  calibrated: outputs cluster on 5/7/8, the distribution shifts between model
  versions, and re-scoring the same text later gives a different number. A stored
  numeric column silently becomes non-comparable across the corpus. If LLM
  judgement is used at all, it goes in the nightly consolidator, emits **three
  coarse buckets** (routine / notable / pivotal), and acts as a tie-breaker.
- **Do not hard-gate writes on novelty.** SAGE-style admission control is
  seductive because it shrinks the corpus, but a rejected write is unrecoverable
  and you cannot evaluate what you never stored. A memory that looks redundant is
  very often a temporal *update* whose whole value is that it near-duplicates the
  old one — which is exactly what bi-temporal invalidation exists to handle.
  **Write always, downweight sometimes.**
- **Do not use a global novelty threshold across scopes.** Embedding density
  differs enormously per workspace; one cosine cutoff marks everything in a dense
  workspace redundant and everything in a sparse one novel. Follow EM-LLM
  (arXiv:2407.09450) and make it adaptive: `μ + γ·σ` of the similarity distribution
  *within the scope*.
- **Do not equate emotional salience with sentiment.** "The user seemed frustrated"
  will happily score a rant about a typo above a quiet note recording a credential
  rotation. Consequence, not affect.
- **Do not score once and freeze.** A static importance prior decays into another
  arbitrary constant within weeks. §3.3's utility-from-usage pass is what keeps it
  alive.

---

### 3.2 Consolidation to gist

#### What biology does

Complementary Learning Systems (McClelland, McNaughton & O'Reilly, Psych. Review
102:419-457, 1995) argues from the failure mode of connectionist nets: a single
store trained at high learning rate on sequential input suffers catastrophic
interference. The fix is architectural — hippocampus writes one-shot, sparse and
pattern-separated; neocortex changes a little on each reinstatement; and the
hippocampus **replays episodes to cortex interleaved with other memories**. The
interleaving is the load-bearing part: replaying one episode 100× in a row
reproduces the interference it was meant to prevent.

Systems consolidation is **lossy and directional**. Over consolidation, retrieval
accuracy for idiosyncratic detail decays while category-level and
schema-consistent content is preserved or improves. A system that appends a
summary while keeping every source in the retrieval pool has performed
*abstraction* but not *consolidation*: it never paid the specificity cost that
makes the gist win at retrieval.

Tse et al. (Science 316:76-82, 2007; Science 2011) give the rate law: once a
compatible schema exists, **new one-trial learning becomes hippocampus-independent
within ~48h instead of weeks**, with immediate-early-gene expression appearing in
mPFC at encoding. Translation: the expensive part is building the *first* gist
node for a topic. After that, new observations on that topic should be cheap
merges into it, not new rows.

The discard operator is contested. Tononi & Cirelli's Synaptic Homeostasis
Hypothesis (Neuron 2014) says sleep globally renormalises synaptic strength, and
because downscaling is multiplicative-with-threshold, connections supported by
many overlapping episodes survive while single-episode idiosyncrasies fall below
threshold — gist extraction falls out of down-selection alone. **This is
disputed** (Frank, "Why I Am Not SHY"): the downscaling evidence is argued to be
confounded. Replay is solid; global downscaling as *the* gist mechanism is
moderate/contested and this doc does not build on it.

Sleep-time compute (Lin et al., arXiv:2504.13171) is the economic argument for
doing any of this offline: ~5× reduction in test-time compute for equal accuracy,
+13–18% accuracy from scaling the offline pass, 2.5× amortisation across queries
sharing a context. The stated limit matters — the benefit correlates with how
**predictable** the query is from the context. If you cannot anticipate what will
be asked, precomputation is wasted spend.

#### What mcplexer does today

There is a real consolidator, and it is a near-duplicate compactor over a recency
window.

- Template + prompt: `internal/workertemplates/seeds/memory-consolidator.json`,
  field `prompt_template`. Pass 1: *"Call `memory__list({kind:"note",
  scope:"global_only", limit:50})` … Group near-duplicates and topical neighbours
  into clusters of 2–5 … write ONE consolidated note via `memory__save` … Call
  `memory__invalidate(id, superseded_by_id=<new>)` on each original. NEVER
  `memory__forget`."* Pass 2 repeats for `scope:"workspace_only"`.
- It does genuinely merge and retire — that is more than Generative Agents or
  A-MEM do, both of which are append-only.
- `tool_allowlist` is `[memory__list, memory__get, memory__save, memory__recall,
  memory__invalidate]`. Don'ts: no `kind=fact` rows, no pinned rows.
- **`memory__list` is ordered `updated_at DESC`**
  (`internal/gateway/builtin_tools_memory.go:100`). So `limit:50` is a **recency
  window, not a corpus pass**. At 787 memories the tail is never visited: nightly
  runs re-chew the same recent 50 forever. This is precisely the anti-interleaving
  failure CLS warns about, and it means the corpus bifurcates into a hot set that
  is consolidated repeatedly and a frozen tail that only grows.
- **It may never have run here.** Autoinstall is gated at
  `cmd/mcplexer/consolidator_autoinstall.go:32-35,49-51` on
  `MCPLEXER_AUTO_INSTALL_MEMORY_CONSOLIDATOR`, called once at boot from
  `cmd/mcplexer/serve.go:1490`. That variable is **not** in the running launchd
  plist's `EnvironmentVariables`. (`MCPLEXER_ALLOW_CLAUDE_CLI=1` *is* set, so the
  template's `model_provider_hint: claude_cli` would not block it if enabled.)
  Last-run tracking exists and works
  (`internal/api/memory_consolidate_handler.go:116-131`, including a `CanRun` /
  `RunBlockedReason` probe); default schedule `0 3 * * *`.
- **There is no gist tier.** The template writes the merged note with the same
  `kind:"note"`, so gist and source compete in one ranked pool. Zep separates its
  semantic subgraph from its community subgraph precisely to avoid this.
- **The success metric counts appends.** `internal/workers/runner/consolidator.go:52`
  emits `consolidations_performed`, which is the generic write-action tally
  (`memory__save` dispatches); `dream.go` reuses the same counter for
  `actions_performed`. **A run that saves 12 notes and invalidates nothing reports
  12 successful consolidations.**

#### Smallest useful implementation

Four changes, in dependency order.

1. **Fix the KNN prefilter first** (§1.4b). Until `InvalidateMemory` drops the
   `memories_vec` row and `VectorSearchMemories` over-fetches, *consolidation makes
   vector recall worse*, and every measurement below is confounded.

2. **Cluster-seeded selection instead of a recency window.** Add
   `memory__consolidation_candidates({scope, limit})`: pick a seed by **lowest
   recall count in the last 90 days** (`internal/store/sqlite/memory_recall.go:134`
   already aggregates over `recallStatsWindow`), then pull its top-10 vector
   neighbours (the arm exists) and consolidate *that cluster*. One cluster per run,
   seeded from the cold tail, is the interleaved-replay analogue and is strictly
   better than 50-by-recency.

3. **A gist tier.** Add `kind='gist'` (or a `tier` column: `episode` | `gist`) plus
   a `derived_from` join table capturing the source ids the template currently
   footers as prose. Then adopt RAPTOR's **collapsed-tree** trick: put gist rows in
   the *same* index as leaves so the existing ranker picks granularity per query,
   with a hard rule that **a gist hit always returns its `derived_from` ids
   alongside it**. Do not build traversal logic; the corpus is 787 rows.

4. **Schema-accelerated save path** — the Tse result, and the highest-leverage item
   in this section because it is the only one that bounds growth *at the source*.
   In `Service.Write` (`registry.go:450`), before insert, run one vector query for
   the top-3 neighbours. Smallest version calls no LLM at all: if cosine > ~0.95
   and same scope/kind, return `{noop: true, existing_id}` and let the caller
   decide. Nightly consolidation cannot bound growth; this can.

#### How to measure it

- **`live_rows_delta` is the metric that decides whether consolidation happened.**
  Add it, plus `invalidations_performed`, to the `memory__consolidator_run` audit
  row (`internal/workers/runner/consolidator.go:52`,
  `internal/workers/runner/audit.go:228-248`) and to the mesh broadcast string.
  Count rows with `deleted_at IS NULL AND t_valid_end IS NULL` before and after.
  **If `live_rows_delta >= 0`, the run summarised; it did not consolidate.**
- **Coverage.** Fraction of the corpus visited by a consolidator run in the last
  30 days. Under the current recency window this is pinned near `50/787`; the
  cluster-seeded selector should drive it toward 1.0. Assert a floor.
- **Eval scenario `TestGistTierPrecision`.** Corpus with 5 near-duplicate episode
  notes plus one gist derived from them. Query the shared topic. Metric: **top-5
  composition** — the correct outcome is `[gist, +sources on request]`, not
  `[gist, src1..src5]`. Assert that the number of distinct *topics* in the top-5
  does not fall when gist rows are added. This is the direct test for the
  append-only-reflection failure.
- **Dedup effectiveness.** For the schema-accelerated save path: corpus growth rate
  before and after, plus a false-NOOP check — a table of pairs that *look*
  near-duplicate but are temporal updates, asserting they all still write.

#### What not to do

- **Append-only reflection** (Generative Agents, A-MEM). Writing derived insights
  into the same pool as their sources, with no invalidation, makes precision *fall*
  as the system consolidates more: a query that should return one gist returns the
  gist, its five sources, and last week's reflection over those reflections.
- **Hard DELETE on contradiction** (Mem0's `DELETE` op). It destroys the ability to
  answer "what did I believe in March, and what changed my mind" — which for a
  mesh-shared memory is exactly the query that matters during an incident.
  mcplexer already does the better thing (`t_valid_end` + `invalidated_by`); do not
  regress. Note Mem0's own graph variant retreats to marking edges invalid rather
  than removing them.
- **Recursive summarise-the-summary without source pointers** (MemGPT's
  `summary_{n+1} = f(summary_n, evicted_n)`). Error compounds monotonically and is
  unrecoverable. Every level must retain explicit source ids and be re-derivable
  from the episode tier.
- **Do not let the consolidator re-ingest its own output** as ordinary input to the
  next round. That is a self-consuming loop with a documented outcome
  (distribution-tail loss / model collapse). Tag gist rows with a distinct kind and
  exclude them from the input pool of the next pass.
- **Unprovenanced retroactive rewriting of neighbours** (A-MEM's memory evolution).
  Because A-MEM embeds the LLM-generated attributes, rewriting a neighbour's
  context silently *moves its embedding* — a memory you never touched becomes
  unfindable by the query that used to find it, with no audit row. If you adopt
  reconsolidation-style rewrites, version them (new row + supersession) rather than
  mutating in place.
- **Do not run an LLM consolidator over `kind=fact` rows.** Facts already have a
  supersession model (`invalidateActiveFact`, `internal/store/sqlite/memory.go:71`)
  maintaining one active row per bucket. An LLM merging them produces a note whose
  validity window is a fiction. The template's existing rule is correct — keep it.
- **Do not run a global decay job as the SHY analogue.** "Decay everything, keep
  what survives" preferentially destroys rare-but-critical facts because they were
  never reinforced. Biology gets away with it because replay reinforces *before*
  downscaling; a naive cron has no replay.
- **Do not build the reflection tree before `rank.go` is trustworthy.** A
  consolidator producing good gist notes into a ranker where recency dominates will
  surface the newest gist regardless of query — and the consolidation work will get
  the blame.

---

### 3.3 Forgetting as interference management

#### What biology does

The framing that matters: at 787 memories, **storage is free and interference is
the only thing forgetting buys you.** Design the retirement policy to maximise
top-k precision, not to reclaim bytes.

Anderson & Schooler (Psych. Science 2:396-408, 1991) mined real corpora and found
that environmental **need-probability** rises with past frequency and decays as a
*power* function of recency — human forgetting curves are a well-calibrated
estimate of P(needed now), not the decay of a leaky store. The engineering
consequence is precise: **the decay term is a prior, and a prior combines with a
query-conditioned likelihood additively in log space and bounded**, so strong
evidence overrides it. ACT-R implements exactly this shape —
`A_i = B_i + Σ_j W_j·S_ji + ε`, then a *threshold* — where the base level
`B_i = ln(Σ_k t_k^{-d})`, `d = 0.5`, sums over every prior use and thereby produces
the power law of practice, the power law of forgetting, and the spacing effect
from one formula. The O(1) approximation `B_i ≈ ln(n/(1−d)) − d·ln(T)` needs two
integers (`n`, `first_seen`) instead of a timestamp list. (Caveat with a citation:
van Rijn/Petrov et al., Comp. Brain & Behav. 2018, found this approximation
non-monotonic in `d` — fine if you pin `d = 0.5`, invalid if you intend to *fit*
`d`.)

**The curve should be a power law, not an exponential.** FSRS moved off the
exponential in v4 because the power law fit real review logs measurably better
across billions of reviews: `R(t,S) = (1 + (19/81)·t/S)^(−0.5)`. The difference is
not cosmetic — at a 30-day half-life a 6-month-old memory sits at ~1.6% under the
exponential and ~37% under the power law. An agent's most valuable memories skew
**old** (architecture decisions, user preferences, hard-won gotchas), so
exponential decay is a direct attack on the corpus.

Bjork & Bjork's New Theory of Disuse (1992) gives the safety mechanism: every
memory has **two** independent scalars. *Storage strength* is monotonically
non-decreasing — use only ever adds. *Retrieval strength* fluctuates freely.
Current accessibility is therefore a terrible proxy for value: a fact can have
very high SS and near-zero RS (you haven't needed it in a year, and it is
load-bearing when you do). A single decayed score cannot represent this and will
retire exactly the memories that are expensive to lose. Two further consequences:
the SS gain from a retrieval is *inversely* related to current RS (desirable
difficulties — a deep-rank rescue is worth more than re-serving rank 0), and the
forget-then-relearn cycle is a feature, not damage, provided reinstatement is
cheap.

Retrieval-induced forgetting (Anderson, Bjork & Bjork 1994; Storm & Levy 2012) is
the most speculative mechanism in this section and is flagged as such. Practising
retrieval of some items *suppresses* unpractised competitors from the same
category, below the level of unpractised items from unpractised categories. The
inhibitory account's **interference-dependence** property is the safety rail: only
competitors that actually competed get suppressed. Cue-independence — the most
interesting prediction — is also the most contested; non-inhibitory blocking
accounts explain much of the data. Treat the phenomenon as robust and the
mechanism as unsettled.

The one architectural finding with numbers attached: Yang, arXiv:2606.15903,
builds ForgetEval (1000 templated + 385 adversarial cases, Fleiss' κ = 0.958,
MIT-licensed) across five families — supersession, decay, amnesia, purge, drift —
and measures by **where you put the LLM**. Deterministic primitives (BM25
substring purge, vector soft-delete): 62.9–68.3% overall, ≥95% on lexical/temporal
categories, ≤5% on identifier obfuscation. Inscribe-time LLM: 100% canonicalisation
but **0%** on intent-aware deletion. Mutation-time hook (a narrow JSON-emitting
planner): **91.7–94.2%**, +22.6 to +24.1pp over deterministic baselines, with the
recall path staying LLM-free. Caveats the author states and this doc repeats: no
quantitative production metrics behind the motivating anecdotes; the lift is
DeepSeek-V3-specific (Llama-3.1-70B fails JSON parsing); English-heavy;
single-author preprint. The thesis is quotable anyway: *"production failures are
predominantly forgetting failures rather than recall failures, yet existing
benchmarks emphasize recall."* That asymmetry describes this repo exactly.

#### What mcplexer does today

**There is no automatic retirement path of any kind.** Every exit from the
retrievable set is manually triggered:

- `Forget` (`registry.go:1583-1593`) → `SoftDeleteMemory`, reachable only from
  `memory__forget`.
- `ForgetBySource` (`registry.go:1599`) — forensic session purge.
- `Invalidate` (`registry.go:1498-1504`) → `InvalidateMemory`. Its only call sites
  are `internal/api/memory_entries.go:213` and
  `internal/gateway/handler_memory.go:1117` — a human on the dashboard, or an agent
  calling `memory__invalidate` (the consolidator being one such agent).
- `internal/brain/indexer.go:309` — soft-delete when a brain `.md` file disappears.
  Filesystem-driven, not usage-driven.

A grep for `ttl|expire|decay|retire|prune|archive` across `internal/memory/*.go`
and `internal/store/sqlite/memory*.go` finds no scheduled path. The only `retire`
hit is `retireStaleEmbedding` (`internal/store/sqlite/memory.go:270`), which drops
a stale *vector* on content change.

"Decay" exists in two places and neither forgets anything:

- `recencyFactor` (`rank_tuning.go:153`) — a ranking term whose floor
  (`recencyFloor = 0.05`, `rank_tuning.go:27`) exists explicitly so nothing is ever
  excluded.
- `fetchMemoryDecayPressure` (`internal/store/sqlite/memory_stats.go:300-349`) —
  counts non-pinned, valid, non-deleted rows with `updated_at < now−180d` **and no
  recall event within 30d**, assembled into `MemoryStats` at `memory_stats.go:59`
  and served to the dashboard. **Zero code acts on the value.** The retirement
  candidate set is already computed; nothing consumes it.

**The usage axis is dark in production.** `recallEnabled: os.Getenv("MCPLEXER_RECALL_TRACKING") == "1"`
(`registry.go:243`) and the variable is not in the live plist. Consequences:
`registry.go:1126` returns nil early, so the recall map is always nil,
`recallSignal` (`rank.go:89`) returns 0, and the AR4 term is mathematically zero;
`memory__co_recalled` is not even advertised
(`internal/gateway/builtin_tools_memory_caps.go:52-55`); the co-recall axis of
`memory__suggestions` is empty (`:56-59`); and `DecayPressure` falls back to a pure
`updated_at` heuristic (`memory_stats.go:313-328`, the `logEmpty` branch). Migration
`077_memory_recall_events.sql` — `(memory_id, query, rank_position, result_set_id,
source)` — is the right schema and it is collecting nothing.

**Supersession is keyed on the literal memory name.** `invalidateActiveFact`
(`internal/store/sqlite/memory.go:71`) fires only on an exact
`(workspace, worker, name)` collision. A paraphrased or differently-named
correction never invalidates its predecessor, so the corrected fact and the stale
fact both sit with `t_valid_end IS NULL`, both recall-eligible — and until §1.3 the
stale one often won on recency. This is silent proactive interference: no error is
raised, the wrong answer is confidently sourced. ForgetEval puts this class of
deterministic matcher at 62.9–68.3% overall and near 0–5% on paraphrase and
identifier-obfuscation attacks.

#### Smallest useful implementation

Ordered. Steps 1–2 are data collection and are safe to ship immediately; step 3 is
the highest-value change; steps 4–5 are the actual forgetting and must not precede
them.

1. **Turn `MCPLEXER_RECALL_TRACKING=1` on by default.** Nothing here works without
   a use clock, and the schema has existed since migration 077. Everything else in
   this section is blocked on this. Do it now, so the data has weeks to accumulate
   before any consumer reads it.

2. **Migration `152_memory_strength.sql`:** `storage_strength REAL`,
   `use_count INTEGER`, `first_used_at INTEGER`. Written by the existing
   recall-event hook. **Nothing reads them yet.** Retrieval strength is computed on
   the fly with the ACT-R O(1) approximation `B ≈ ln(n/(1−d)) − d·ln(T)`, `d=0.5`,
   from `use_count` and `now − first_used_at` — two integers, no timestamp list.
   Increment `storage_strength` on **confirmed use, not on appearing in top-k**,
   weighted by difficulty: `Δ_SS = 1 / (1 + current_retrieval_strength)`, so
   rescuing a memory from rank 18 credits far more than re-serving perpetual rank
   0. `storage_strength` **never decreases**. This ratchet is what makes decay safe.

3. **Mutation-time semantic supersession** — the ForgetEval headline, and the
   highest-leverage item in this doc. In `Service.Write` for `kind=fact`: embed the
   candidate, pull the top **s=10** semantically nearest *active* memories in the
   same scope (the vector arm exists), and issue **one narrow LLM call that must
   return JSON only**: `{op: "add"|"supersede"|"noop", supersedes: [id...],
   confidence: 0..1}`. On supersede above threshold, call the **existing**
   `store.InvalidateMemory(ctx, id, supersededByID)`. No new deletion primitive, no
   new table. Bounds: `s=10` keeps the call O(1) in corpus size; require same-scope
   **and** entity overlap before an id is even a candidate; require confidence
   ≥ ~0.8; cap at 3 supersedes per save; write an audit row for every stamp; this
   path may never hard-delete.

4. **Dormancy as a consolidator step, not a new job.** `fetchMemoryDecayPressure`
   already computes the candidate set. Add `dormant_at`. A dormant memory **stays
   in the candidate pool** and stays findable by name, tag, entity, or explicit
   query, but is excluded from the default top-k of an open-ended semantic recall.
   Reversible, auditable, zero data loss. Then the reinstatement rule, which is what
   makes it self-healing: **if a dormant memory is ever retrieved by an explicit
   query, clear `dormant_at` and bump `storage_strength` by the
   desirable-difficulties increment.** A false-positive retirement costs one
   degraded query and then repairs itself permanently — which is what licenses
   being moderately aggressive.

5. **Curve and clock fixes in `rank_tuning.go`.** Swap `recencyFactor`'s
   `0.5^(t/30d)` for the power law `(1 + (19/81)·t/S)^(−0.5)`, and switch its input
   from `Entry.UpdatedAt` to `max(last_recall_at, updated_at)`. Both are contingent
   on step 1 having collected data and on §1.4a being fixed (otherwise a backfill
   resets every clock). Split the decay parameter by `kind` (fact vs note) and
   scope; one global half-life guarantees you are wrong for half the corpus.

6. **RIF-style competitor demotion — last, speculative, optional.** Co-presence in a
   `result_set_id` where one memory was used and its near-neighbours were not is a
   competitor signal, and `077_memory_recall_events.sql` already records exactly
   that. Scope any demotion to same-`result_set_id` co-occurrence *and* semantic
   proximity, per interference-dependence. Ship only if steps 1–5 leave measurable
   near-duplicate crowding in the top-k.

#### The seven safety rules

1. **Decay affects rank, never existence.** Three tiers, three triggers:
   activation → ordering (`rank.go`); dormancy → default visibility (consolidator);
   deletion → explicit human/tool intent only. Tier 1 and 2 must never escalate into
   tier 3 automatically.
2. **Storage strength is a ratchet.** Any memory whose `storage_strength` exceeds a
   floor is permanently exempt from decay-driven dormancy regardless of age. A fact
   that has ever proven load-bearing can only be removed by contradiction or
   explicit intent.
3. **Never remove from the candidate pool.** Dormant rows stay reachable by exact
   name, tag, entity, or `include_dormant`.
4. **Bi-temporal tombstones, already built.** Everything the forgetting subsystem
   does goes through `t_valid_end` / `dormant_at`, never `DELETE`. The `ValidAt`
   predicate means every past belief state stays reconstructible.
5. **Contradiction requires evidence, not similarity.** The nearest neighbour of
   "prefer Postgres for the events table" is very often "prefer Postgres for the
   audit table", not its negation.
6. **Quarantine, then audit.** Every dormancy and every invalidation writes an audit
   row with the reason and the triggering score, and there is a reversal tool. If
   you cannot answer *"why did the agent stop knowing this"*, it is not shippable.
7. **Mind the resurrection path.** mcplexer meshes memories to paired peers.
   Retiring locally while a peer re-shares the same memory back is the
   cross-pathway recontamination failure. Decide and **write down** whether dormancy
   propagates. For explicit purges, propagation is mandatory, not optional.

#### How to measure it

- **Port ~40 ForgetEval cases** (MIT-licensed, five families) as a Go table-driven
  test against the real store, before shipping any of the above. The paper's
  baseline prediction: a name-keyed deterministic superseder like the current
  `invalidateActiveFact` should land in the 60s; the mutation-time hook in step 3
  should land in the low 90s. If it does not, you have learned something for the
  price of a test file. This is the single most valuable test in this document,
  because mcplexer currently has recall tests and **zero** forgetting tests.
- **Stale-answer rate.** New eval scenario: seed a fact, then a paraphrased
  correction under a different name. Assert the correction outranks the original
  *and* that the original is invalidated. Today this fails on both counts.
- **`live_rows_delta`** — same metric as §3.2.
- **False-positive dormancy rate**, measured by the reinstatement counter: how often
  a dormant memory is subsequently retrieved by an explicit query. A rate near zero
  means dormancy is too timid; a high rate means it is too aggressive. This is the
  tuning dial and it is self-reporting.

#### The tension, stated plainly

**Aggressive forgetting improves top-k precision. Never losing a load-bearing fact
requires timid forgetting. These are in genuine conflict and no amount of tuning
dissolves it.**

This doc resolves it structurally rather than by picking a threshold:

- Precision is bought at the **visibility** tier (dormancy), which is reversible,
  so being wrong is cheap.
- Durability is guaranteed at the **existence** tier (never automatic), which is
  irreversible, so being wrong there is forbidden.
- The two are bridged by the **monotone storage-strength ratchet** and the
  **reinstatement-on-hit** rule, which together bound the cost of a false positive
  to one degraded query.

What this does *not* solve: a memory that is load-bearing but has never been
retrieved (nobody has needed the disaster-recovery note yet) has `storage_strength = 0`
and is indistinguishable from junk. Pinning is the only current defence and it is
manual, so it protects only what someone thought to protect in advance — which is
precisely the wrong set. §3.1's importance signal is the partial answer (a note
written during an incident gets arousal weight without anyone pinning it), but it
is partial, and this remains an open problem. See §5.

#### What not to do

- **Do not add decay-driven retirement before §1.3.** Retiring memories using scores
  from a mis-scaled ranker selects for exactly the wrong survivors.
- **Do not use exponential decay** (or MemoryBank's `e^(−t/S)`). See the FSRS
  result above.
- **Do not make decay a multiplicative factor on the relevance score at all.** That
  is the §1 defect restated.
- **Do not decay on `updated_at`.** It is a write clock. A fact nobody has needed in
  a year but that got a whitespace edit last week reads as fresh; a fact retrieved
  yesterday but written in 2024 reads as stale. Inverted on both counts — and
  §1.4a makes it worse, because a re-embed stamps `updated_at` across the corpus.
- **Do not use LRU or a global TTL.** Both are capacity policies and capacity is not
  the constraint at 787 rows. LRU evicts the rarely-needed-but-critical and keeps
  the constantly-restated, which is the exact inversion of value.
- **Do not treat "appeared in top-k" as reinforcement.** MemoryBank resets `t=0` on
  every recall, making any memory that keeps surfacing and keeps being ignored
  effectively immortal — a self-reinforcing rank parasite.
- **Do not key supersession on the literal memory name.** See ForgetEval numbers
  above.
- **Do not rely on inscribe-time entity extraction to solve deletion.** It is the
  seductive middle option and it measurably fails: 100% canonicalisation, 0%
  intent-aware deletion. Extracting entities when you *write* cannot tell you, later,
  which rows a deletion intent covers.
- **Do not put an LLM in the recall hot path.** The mutation-time hook gets
  91.7–94.2% while keeping reads deterministic and testable.
- **Do not aggressively dedupe near-duplicates at write time.** Two differently
  worded copies are reachable from two different query phrasings — that is the
  encoding-variability benefit. Prefer link-and-merge (enrich the survivor with the
  new phrasing's keywords) over drop.
- **Do not let pin be the only safety valve.** It is manual, so it protects only what
  was predicted to matter.
- **Do not ship retirement without reinstatement.** Without it every false positive
  is permanent and errors accumulate monotonically.

---

### 3.4 Reconstruction over retrieval

#### What biology does

Tulving & Thomson's encoding specificity principle (Psych. Review 80(5), 1973):
*"specific encoding operations performed on what is perceived determine what is
stored, and what is stored determines what retrieval cues are effective."*
Retrieval effectiveness is a function of **overlap(cue, encoded trace)**, not of
semantic similarity to the target — demonstrated by the recognition-failure
paradox, where a weak associate present at encoding out-cues a strong semantic
associate that was absent. Engineering corollary: **whatever text you index at
write time IS the cue set.** A query that does not overlap it cannot reach the
trace regardless of embedding quality.

CA3 pattern completion is the recall mechanism: dense recurrent collaterals store
an experience in one shot, and a *subset* of the pattern later drives the network
to settle into the full stored attractor. Neunuebel & Knierim (Neuron 2014) showed
CA3 output is closer to the originally stored representation than its own degraded
input — it *cleans up* a corrupted cue. Its complement is dentate-gyrus pattern
separation. The tradeoff is explicit: widen the completion basin and you recall
from thinner cues but start merging distinct episodes.

ACT-R's spreading term shows how to keep that from degenerating:
`S_ji = S − ln(fan_j)`, where `fan_j` is the number of chunks a cue appears in.
**A cue that points at many memories contributes almost nothing.** Without the
`ln(fan)` discount, spreading activation returns whatever hub node is most
connected.

And the failure mode to design against: the DRM paradigm (Roediger & McDermott,
JEP:LMC 21(4), 1995). Study 15 associates of an unpresented lure; the lure is
falsely recalled ~40% of the time — comparable to real list items — and 72% of
falsely *recognised* lures get vivid "remember" judgements rather than "know".
**Any system that spreads activation over an associative graph and summarises into
gist will manufacture plausible items that were never stored, and the confidence
signal will not distinguish them.** Provenance tagging is the source-monitoring
function; without it the failure is undetectable.

The empirical numbers that should decide the design:

- **Query rewriting loses to plain hybrid search.** Wang et al., EMNLP 2024,
  Table 6, TREC DL19/DL20 (mAP / nDCG@10 / latency): BM25 30.13/50.58/0.07s;
  hybrid 47.14/72.50/3.20s; **LLM query rewriting 44.56/67.89/7.80s**; **LLM query
  decomposition 41.93/66.10/14.98s**; HyDE+hybrid 52.13/73.34/11.16s. Expansion
  that *replaces* the query measurably loses. Expansion is only safe as an
  **additional fused arm with the verbatim query always retained.**
- **Cue expansion at index time wins.** LongMemEval (ICLR 2025): fact-augmented key
  expansion — indexing each memory unit under LLM-extracted facts *in addition to*
  its raw text — gives **+9.4% recall@k and +5.4% QA accuracy**. That is encoding
  specificity implemented on the write path. Conversely, compressing sessions down
  to bare extracted facts *hurts* (information loss).
- **Reading is a bigger lever than ranking.** LongMemEval's Chain-of-Note result
  adds up to **+10 accuracy points even under oracle retrieval** — a large share of
  end-to-end "memory failure" is a synthesis failure, not a ranking failure. But
  that belongs in the caller's prompt contract, not hidden inside the store.
- **Citation-grounded generation has a hard ceiling.** ALCE (EMNLP 2023): best
  systems reach ASQA 84.8 recall / 81.6 precision, ELI5 69.3/67.8, QAMPARI
  20.5/20.9 — i.e. on long-form questions ~30% of generated statements are **not
  entailed by their own citations**, and on list-style questions ~80% are not.
- **Synthesis belongs offline.** Sleep-time compute again: ~5× test-time token
  reduction, 2.5× amortisation across queries sharing a context. GraphRAG's C0 root
  community summaries cost 2.6% of full source-text summarisation.

#### What mcplexer does today

`summary` is a literal count string. `recallSummary`
(`internal/gateway/memory_recall_envelope.go:67-79`) is
`fmt.Sprintf("%d memor%s recalled%s (count=%d) — read hits[]…")`, wired at `:53-62`.
The file header (`:17-30`) states its whole purpose: a **compaction-proof scalar**,
defending against `compact()` columnarising `hits` so an agent reading
`hits.length` sees `undefined`. It contains no content from the hits, no synthesis,
no model call. Hits are verbatim — `toRecallHits`
(`internal/gateway/handler_memory.go:407-426`) copies `Content: h.Entry.Content`
unchanged — and `Recall` (`registry.go:997`) has no synthesis stage. **Recall is
pure verbatim retrieval end to end.** That is a defensible position and this doc
mostly endorses it (see below).

**The associative layer has zero call sites inside `Recall`.** `RelatedEntities`
(`registry.go:808`), `SpreadingActivation` (`registry.go:833`), `CoRecalled`
(`registry.go:1362`), `SuggestionsFor` (`registry.go:1381`) are reachable only via
an explicit MCP tool call or REST endpoint — dispatch switch at
`internal/gateway/handler_memory.go:128-139`, `:560`, `:602`, `:652`, `:693`;
`internal/api/memory_entities_handler.go:136,167,197,225`, routed at
`internal/api/router.go:980`. `Recall`'s pipeline is `SearchMemories` → optional
`Embed` + `VectorSearchMemories` → `rrfFuse` → optional `crossEncoderReorder` +
`foldRecencyPin` → else `mmrPool` + `rerankHits` → `capHits`. It calls none of them.

Worse, three of the four have a **bootstrap problem in their signatures**:
`SuggestionsFor(ctx, memoryID string, ...)` and `CoRecalled(ctx, memoryID string, ...)`
take a memory id, not a query. **They presuppose the retrieval that is failing.**
And two of them are conditionally hidden entirely
(`internal/gateway/builtin_tools_memory_caps.go:43-64`) when recall tracking or an
embedder is absent — which, per §3.3, is the live configuration.

`SpreadingActivation` (`registry.go:833`) sums `weight = 1/(1+dist)` across seeds
with **no fan discount, no per-hop decay, and one fixed hop**. Without the
`ln(fan)` term this surfaces whatever hub entity is most linked — the DRM failure
in software form.

#### Smallest useful implementation

**The answer to "what should `recall()` return" is: ranked evidence by default,
and a synthesised reconstruction only when it was built offline and every sentence
carries memory-id provenance.**

The best-performing systems agree on this split. MRAgent (arXiv:2606.06036),
current SOTA on LoCoMo/LongMemEval, explicitly returns evidence passages plus
structured metadata and lets the caller's LLM synthesise. GraphRAG only synthesises
because its summaries are pre-built at index time, and it *still* gates every
partial answer with a 0–100 helpfulness score and drops the zeros. And the decisive
local argument: **the mcplexer caller is already an LLM.** A second synthesis pass
inside `recall()` buys nothing the caller cannot do, while adding latency, cost, and
an unauditable confabulation layer.

Ordered by expected value per line changed:

1. **Save-path cue expansion — biggest expected win, smallest change.** In
   `WriteWithResult` (`registry.go:450`), persist a cue block per memory
   `{keywords, tags, one-line context, canonical entity names}`, index it in FTS5
   as a weighted column, and embed `concat(content, cues)` rather than content
   alone. This is A-MEM's construction and LongMemEval's fact-augmented key
   expansion (+9.4% recall@k). It is the direct implementation of encoding
   specificity: **today mcplexer indexes the author's words and queries with the
   reader's words.** Zero-LLM fallback: derive cues from the existing
   `entities[]`/`tags` columns. One backfill over 787 rows — trivial, and it must
   not stamp `updated_at` (§1.4a).

2. **Multi-cue fusion in `Recall`, never query substitution.** `Recall`
   (`registry.go:997`) runs exactly one FTS arm and one vector arm over the verbatim
   query. Add arms and fuse with the existing `rrfFuse` (`registry.go:1734`):
   (a) the verbatim query, **always retained**; (b) a self-query arm that *lifts
   explicit constraints* — time range → the bi-temporal validity window, tags,
   scope, entity names — rather than paraphrasing; (c) an entity arm seeded from
   entities matched in the query. RRF makes adding arms safe because a bad arm
   contributes bounded rank mass. LongMemEval's time-aware expansion is worth
   +6.8–11.3% temporal recall — and degrades badly if a weak model does the
   extraction, so skip the arm rather than degrade it.

3. **Fan-normalise and budget `SpreadingActivation`** (`registry.go:833`). Two small
   changes: multiply each seed→neighbour contribution by `S − ln(fan_j)` where
   `fan_j` is a `COUNT` on `memory_entities` (migration `076_memory_entities.sql`),
   and apply a per-hop decay `D < 1` with a no-repeat-firing set so it can safely go
   2 hops. Then expose it as a **recall arm fused via RRF**, not only as an
   entity-suggestion sidecar.

4. **Consolidator-built synthesis rows** (this is where reconstruction belongs).
   Cluster the entity-link graph — at 787 memories plain connected-components or
   agglomerative clustering is sufficient; Leiden is overkill — and emit one report
   per cluster using GraphRAG's element-prioritisation rule (order members by entity
   degree, fill to a token budget). Store as `kind='synthesis'` with `derived_from`
   ids, `generated_at`, and the model id. Then RAPTOR's collapsed-tree trick again:
   same index as the leaves, ranker picks granularity.

5. **`memory.recall({..., mode:"brief"})`** returning
   `{reconstruction, citations[], evidence[]}`. The reconstruction is **assembled
   from pre-built synthesis rows, never generated per call**; every sentence carries
   ≥1 memory id; a cheap post-check drops any sentence whose cited ids are absent
   from the evidence set (a poor man's ALCE citation-recall gate — upgrade to an
   entailment check if a small local NLI model becomes available). Default mode stays
   `hits`, so nothing existing changes.

#### How to measure it

- **Cue-expansion A/B.** Same corpus, same queries, cue column on vs. off. Metric:
  **recall@10 and nDCG@10**. LongMemEval's reported delta is +9.4% recall@k; if the
  local number is near zero, the cue generator is producing restatements of the
  content rather than alternative access paths, and the fix is the generator, not
  the retriever.
- **Paraphrase-gap scenario, `eval/corpus_paraphrase.go`.** The eval corpus today
  shares vocabulary between query and gold document, which is exactly the case cue
  expansion does not help. Add a scenario where each query is a *paraphrase* using
  none of the gold document's discriminative tokens. This is the encoding-specificity
  test and the current corpus cannot express it.
- **Arm-ablation table.** With N fused arms, report recall@10 and P@1 for each subset.
  The rule from Wang et al. is that any arm which *replaces* the verbatim query loses;
  the ablation is how you catch an arm that is net-negative.
- **Hub-domination check for spreading activation.** Assert that the top-5 of a
  spreading-activation query is not invariant across three semantically distant seed
  queries. If it is, the `ln(fan)` discount is not working and you have built the DRM
  lure.
- **Citation recall for `mode:"brief"`.** Fraction of returned sentences whose cited
  ids are present in the evidence set — and assert it is **1.0**, because unlike ALCE
  we control the assembler and can simply refuse to emit an uncited sentence. Also
  assert determinism: two identical `brief` calls must return byte-identical output,
  which is only possible because synthesis is pre-built.

#### What not to do

- **Do not replace the user's query with an LLM rewrite or decomposition.** Numbers
  above. Expansion is safe only as an additional arm.
- **Do not put HyDE (or any per-call generative expansion) in the recall hot path**
  for a 787-memory corpus. It costs 7–11s per query in the measured setup, and its
  documented win condition is *no in-domain supervision* — it loses ~10% to a
  fine-tuned dense retriever on DL20 and merely ties BM25 on TREC-COVID. With a small
  corpus the bottleneck is precision, not candidate recall.
- **Do not return a synthesised answer without the evidence set.** ALCE's ceiling
  makes the failure invisible to the caller and to the audit log otherwise.
- **Do not synthesise per `recall()` call.** Non-deterministic across two identical
  recalls, which breaks caching, diffing, and reproducible incident review.
- **Do not shred memories into atomic facts and discard the source episode.**
  LongMemEval measured that this *hurts* overall accuracy through information loss —
  unresolvable pronoun references and lost interpreting context. Store cues alongside
  the episode, not instead of it.
- **Do not run spreading activation without a fan discount and a hop budget.**
- **Do not use a weak model for constraint extraction.** Weak models hallucinate time
  ranges, and the expansion becomes a filter that excludes the correct memory. Skip
  the arm instead.
- **Do not make cue expansion a hard LLM dependency on the write path.** If the
  enrichment model is unavailable, saves must still succeed with cues derived from
  `entities`/`tags`. A memory subsystem that can fail to *store* because a model call
  failed is worse than one with weaker cues.
- **Do not add MMR diversity reordering ahead of a cross-encoder.** A cross-encoder
  scores each (query, doc) pair independently of input order, so pre-reordering has
  zero effect on the final ranking while costing up to `k*2` embedding fetches and
  O(n²) cosines. `registry.go:1090-1096` already gets this right — the anti-pattern is
  re-adding it "for consistency" during a refactor.

---

## 4. Sequenced roadmap

Ordered by value/risk. Every item names its gating metric. Items are marked
**[proven]** (mechanism validated somewhere with numbers, and cheap here),
**[plausible]** (sound reasoning, no direct local evidence yet), or
**[speculative]** (interesting, unvalidated, ship last or not at all).

### Do this first: the three one-line correctness fixes

Not because they are exciting, but because **every measurement below is confounded
until they land**, and each is under 10 lines.

| # | Change | File | Why first |
|--|--|--|--|
| 0.1 | Drop `updated_at` from the embedding stamp | `internal/store/sqlite/memory_query.go:174-177` | A backfill currently resets the age of the entire corpus. Any decay or recency work measured before this is measuring nothing. **[proven]** |
| 0.2 | `DELETE FROM memories_vec` inside `InvalidateMemory` + over-fetch `v.k = k*4` in `VectorSearchMemories` | `internal/store/sqlite/memory.go:367`, `memory_query.go:126-132` | Consolidation currently degrades vector recall, silently. Any consolidation measurement is confounded. **[proven]** |
| 0.3 | Set `MCPLEXER_RECALL_TRACKING=1` in the daemon environment | launchd plist / systemd unit | The entire usage axis is dark. Every §3.3 item and the utility signal in §3.1 are blocked on data that takes weeks to accumulate. Start the clock now. **[proven]** |

### Then, in order

1. **Cross-encoder eval scenario** — a deterministic fake cross-encoder in
   `internal/memory/eval/`, so `crossEncoderReorder` + `foldRecencyPin` is covered.
   *Rationale:* enabling the reranker today would move production onto a path the
   gate does not test — the exact §2.1 blindness, one env var away. Cheap, pure
   test code, zero production risk. **[proven]**
   *Gate:* the new scenario fails against a deliberately reintroduced multiplicative
   `foldRecencyPin`.

2. **ForgetEval subset (~40 cases) as a Go table-driven test.** *Rationale:* mcplexer
   has recall tests and zero forgetting tests. This establishes the baseline number
   before anything is changed, and the paper predicts where it will land (60s), so a
   wildly different result is itself information. One day of work; the highest
   information-per-line item in this document. **[proven]**
   *Gate:* none — this **is** the gate for items 5 and 7.

3. **Save-path cue expansion + FTS5 cue column + backfill.** *Rationale:* the largest
   measured retrieval gain available (+9.4% recall@k, LongMemEval), no hot-path LLM
   call required (entities/tags fallback), and it is the one change that addresses
   encoding specificity — currently mcplexer indexes the author's words and queries
   with the reader's words. **[proven]**
   *Gate:* recall@10 and nDCG@10 on a new paraphrase-gap eval scenario, cue column on
   vs. off.

4. **Migration 151 (`importance`/`novelty`/`utility`) + free novelty at save +
   goal-relevance from the task ledger + surprise from the existing
   `surfaceContradictions` output.** *Rationale:* adds the missing third axis. The
   §1 fix is currently a principled *retune* of a two-axis system; with importance it
   becomes a three-axis system with the Generative Agents prior
   (relevance 3 : importance 2 : recency 0.5) as its target shape. Goal-relevance from
   the task ledger is mcplexer's genuine differentiator — nothing else in the survey
   has a durable task ledger next to its memory store. **[plausible]** — the terms are
   sound individually; the specific coefficients are guesses until the eval says
   otherwise.
   *Gate:* P@1 on a salience-graded corpus, **plus** the orthogonality check
   (`|corr(importance, created_at)| < 0.3`). If importance correlates with write time,
   revert — you have rebuilt the §1 defect.

5. **Mutation-time semantic supersession** (`s=10`, JSON-only, confidence-gated,
   reusing `InvalidateMemory`). *Rationale:* ForgetEval's largest single measured lift
   (+22.6 to +24.1pp), and it fixes a live silent-wrong-answer bug — supersession is
   currently keyed on the literal memory name. Risk is real (an LLM invalidating good
   memories), which is why it is gated behind item 2's baseline and hard-bounded:
   same-scope + entity overlap + confidence ≥0.8 + ≤3 ids per save + audit row + never
   hard-delete. **[proven]** as a mechanism, **[plausible]** at the reported effect
   size (single-author preprint, model-specific).
   *Gate:* the ForgetEval subset moves from the 60s toward the low 90s, and no
   regression in the §2 scaled scenarios.

6. **Consolidator: cluster-seeded selection + gist tier + `live_rows_delta`.**
   *Rationale:* the consolidator currently re-chews the same 50 recent notes forever
   (`builtin_tools_memory.go:100` orders `updated_at DESC`) and reports append counts
   as success. `live_rows_delta` is the single metric that distinguishes consolidation
   from summarisation. **[plausible]** — the CLS interleaving argument is strong,
   but the local effect size is unmeasured.
   *Gate:* `live_rows_delta < 0` on a run; corpus coverage over 30 days rises from
   ~6% toward 1.0; `TestGistTierPrecision` shows top-5 topic diversity does not fall
   when gist rows are added.

7. **Migration 152 (`storage_strength`/`use_count`/`first_used_at`) + power-law decay
   + last-use clock.** *Rationale:* the two-strength split is what makes any future
   forgetting safe, and it is pure data collection at first — nothing reads the
   columns. Must land well before item 8. **[proven]** for the curve choice (FSRS,
   billions of reviews); **[plausible]** for the specific `Δ_SS = 1/(1+RS)` increment.
   *Gate:* eval metrics **unchanged** at first (data collection only), then a measured
   improvement when the recency input switches from `updated_at` to
   `max(last_recall_at, updated_at)`.

8. **Dormancy pass in the consolidator, gated on the storage-strength floor + pin,
   with reinstatement-on-hit.** *Rationale:* this is where interference management
   actually pays off, and it is deliberately last among the non-speculative items
   because it is the first change that can *hide* a correct answer.
   **[plausible]** — the mechanism is well-motivated but the threshold is a guess and
   the false-positive rate is unknown until measured.
   *Gate:* top-k precision up on the scaled scenarios, false-positive dormancy rate
   (reinstatement counter) inside a stated band, ForgetEval subset not regressed.

9. **Retroactive behavioural tagging (temporal-neighbourhood importance propagation).**
   *Rationale:* the mechanism nobody has shipped, and it targets the real failure mode
   of an agent memory store — the boring setup note written five minutes before the
   incident. Costs one indexed query per high-salience row. **[speculative]** — the
   biology (Frey & Morris; Moncada & Viola) is solid, the engineering translation has
   **no prior art in any surveyed system**, and `δ`/`τ` are pure guesses.
   *Gate:* the tag-and-capture eval scenario in §3.1. If it does not move, delete the
   code — do not tune it into significance.

10. **RIF-style competitor demotion from `result_set_id` co-occurrence.**
    *Rationale:* the most speculative item here and the easiest to get wrong.
    **[speculative]** — the phenomenon is robust; the inhibitory mechanism is
    contested; a mis-scoped implementation strips the corpus of exactly the
    complementary facts that make it worth having. Ship only if 1–8 leave measurable
    near-duplicate crowding in the top-k, and scope strictly to same-`result_set_id`
    co-occurrence.

11. **`mode:"brief"` reconstruction from pre-built synthesis rows.**
    *Rationale:* deliberately last. The caller is already an LLM; this is a
    convenience surface, not a capability gap, and it is only safe once item 6 exists
    to build the synthesis rows offline. **[plausible]**
    *Gate:* citation recall = 1.0 (by construction) and byte-identical output across
    two identical calls.

### What is explicitly not on this roadmap

- An LLM importance judge on the save path. Ever. If LLM judgement is wanted, it goes
  in the nightly consolidator as a batch pass emitting three coarse buckets.
- HyDE or per-call generative query expansion.
- LRU, global TTL, or any capacity-driven eviction.
- Automatic hard deletion from any decay or scoring path.
- Leiden community detection. At 787 rows, connected components is enough; revisit at
  10k.

---

## 5. Open questions and known unknowns

**5.1 The never-retrieved load-bearing memory.** The one problem this design does not
solve. A disaster-recovery note that nobody has needed yet has `storage_strength = 0`,
no recall events, and — unless it happened to be written during an incident — a
middling importance score. It is indistinguishable from junk by every signal proposed
here. Pinning is the only defence and it is manual, so it protects the set someone
predicted would matter, which is systematically the wrong set. Partial mitigations
worth exploring: an explicit `criticality` kind that is exempt by construction;
periodic **sampling below the floor** (PER's insight — a transition sampled with
probability zero can never correct its own stale estimate); or treating "written but
never once retrieved in 180 days" as a *review* signal surfaced to a human rather than
a retirement signal. None is satisfying.

**5.2 Does importance stay orthogonal to recency in practice?** The orthogonality gate
in §3.1 is stated as `|corr(importance, created_at)| < 0.3`, and that threshold is a
guess. Every proposed term has a plausible correlation with write time: novelty is
higher early in a topic's life, active tasks are recent by definition, open incidents
are recent. It is entirely possible that the honest measured correlation is 0.5 and the
correct response is to drop the goal-relevance and arousal terms and keep only novelty
and surprise. **The measurement decides; the design does not get a vote.**

**5.3 Where should the recency half-life actually sit, per kind and scope?** §3.3
proposes splitting the decay parameter by `kind` and scope on the argument that a
session note and a global architecture decision have opposite need-profiles. Nobody has
measured the need-profile of either on this corpus. Anderson & Schooler's method — mine
the actual access log for `P(needed | time since last use, frequency)` — is directly
applicable once `MCPLEXER_RECALL_TRACKING` has been on for a few months, and would
replace guessed constants with fitted ones. That is the correct way to set these
numbers and it is currently impossible.

**5.4 Does dormancy propagate over the mesh?** Undecided, and it must be decided before
item 8 ships. Retiring a memory locally while a paired peer re-shares it back is the
cross-pathway recontamination failure. Three coherent positions: dormancy is
machine-local (defensible — it is a local usage judgement), dormancy propagates
(consistent, but one machine's disuse silently degrades another's recall), or dormancy
is scoped to the originating peer. For **explicit purges**, propagation is not optional
in any of the three.

**5.5 Is the hashed-embedder ceiling hiding a real fused-path regression?** §2.4 holds
the fused eval arm to recall@10 plus convergence rather than absolute ordering floors,
because `hashEmbedder` tops out at nDCG 0.796 with recency inert. That is the honest
call, but it means the fused arm's *ordering* quality is currently unmeasured in
absolute terms. A small local embedding model in the test harness (deterministic, no
network, checked-in weights or a fixed local endpoint) would close this, at the cost of
test-suite weight. Until then, a fused-path ordering regression under ~0.05 is invisible.

**5.6 What is the actual production query distribution?** The eval reuses 10
hand-labelled queries. The sleep-time-compute result says offline precomputation pays
off in proportion to how **predictable** queries are from the context — so whether item
6's synthesis rows are worth their cost depends entirely on a distribution nobody has
sampled. `memory_recall_events` records the query string (migration 077). Once tracking
is on, the first analysis worth running is: how concentrated is the query distribution,
and how many recalls are re-asks of a previous question?

**5.7 The consolidator has possibly never run here.** `MCPLEXER_AUTO_INSTALL_MEMORY_CONSOLIDATOR`
is absent from the running launchd plist (`cmd/mcplexer/consolidator_autoinstall.go:32-35`).
Everything in §3.2 about "what the consolidator does today" is read from the template,
not observed. Before redesigning it, run it once with `live_rows_delta` instrumented and
find out what it actually does to this corpus. It is entirely possible the answer is
"nothing, because 50-by-recency finds no clusters worth merging", which would change the
priority of item 6.

**5.8 Contested science, flagged.** Three mechanisms cited above are not settled and
this doc does not build load-bearing design on any of them: synaptic homeostasis as
*the* gist mechanism (Frank's rebuttal), cue-independence in retrieval-induced
forgetting (non-inhibitory blocking accounts explain much of the data), and SAGE's
learned vMF novelty gate (single preprint, no production deployment, equations not
verified line-by-line). Where they appear, they motivate a direction rather than a
formula.
