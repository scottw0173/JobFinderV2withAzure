# CLAUDE.md — JobFinder (Azure branch / data-gathering instrument)

> **How to use this file.** This is a reasoning-first spec, not a changelog. Each
> decision below states *why* it is the way it is. The "why" is load-bearing:
> several of these choices look like obvious targets for "helpful" simplification
> (e.g. "just batch the calls," "just store the cost," "just use one price"), and
> undoing them silently corrupts data on a **non-repeatable 30-day window**. If a
> change seems to contradict a decision here, surface the contradiction and the
> reasoning before acting — do not optimize it away.

---

## 1. Frame: what this project *is now*

This branch began as an **AWS→Azure migration** (make the app cloud-agnostic). That
goal is essentially done. The project has since crossed from a **build goal** to a
**use goal**: the Azure implementation is now a **measurement instrument** whose
purpose is to collect data comparing AI models on job-fit scoring.

The consequence: most design questions are no longer about *portability/equivalence*
(does Azure do what AWS did) but about *measurement validity* (is what I capture
attributable, are my comparisons confounded, does an operating choice destroy a
quantity I need). When in doubt, optimize for **measurement validity and data
recoverability**, not for operational tidiness or cost-per-run.

### Goal separation (why this is a separate repo)
- **JobFinder-AWS (original, solo-built):** demonstrates independent engineering.
- **JobFinder-Azure (this, AI-assisted):** demonstrates AI-tool fluency *and* a
  multi-model evaluation study. Kept in a **separate repository** deliberately —
  merging blurs two distinct portfolio signals. This CLAUDE.md belongs to the
  Azure project only; it should not bleed into the solo repo.

---

## 2. Current state (build frontier)

**The Azure subscription is now live** — treat credit burn as the operative clock.
The full Bicep stack in `infra/` is deployed to the resource group and region named
in `main.bicepparam`, and the keyless auth chain is **validated end-to-end against
live Azure**, not merely built offline. This section supersedes the pre-account
framing that used to live here and in §9.

**Deployed and confirmed working:**
- Full stack provisioned: two user-assigned identities (app UAMI + deploy-script
  UAMI), ACR, Log Analytics, Container Apps environment + Job, Storage account with
  the `config` blob container, Key Vault, Foundry/OpenAI account, Postgres Flexible
  Server (AAD-only, `passwordAuth: Disabled`), and all RBAC assignments.
- **Keyless managed-identity auth, end-to-end:** the Job pulls from ACR, the app
  UAMI acquires an Entra token, and that token authenticates to Postgres *as the
  password* via the `pgxpool` `BeforeConnect` hook. A manual Job run reaches app
  startup and `pool.Ping` succeeds — the single biggest design unknown is proven
  live. This required injecting **`AZURE_CLIENT_ID`** (the app UAMI's client ID,
  from the `uamiClientId` deploy output) into the Job env: with a user-assigned
  identity the SDK otherwise falls back to a non-existent system-assigned one and
  the MSI endpoint 400s with "Unable to load the proper Managed Identity."
- Config files (`instructions.md`, `sources.json`, `filterKeywords.json`) are
  **uploaded to the `config` blob container** (via the deployer's identity, which
  holds Storage Blob Data Contributor; the app UAMI holds only Blob Data Reader).

**Hard-won deployment facts — do NOT "simplify" these away (each cost a failed deploy):**
- The Postgres-principal `deploymentScript` installs the `psql` client with
  **`tdnf`** (the CLI script container is Azure Linux, not Alpine — `apk` does not
  exist), authenticates with **`az login --identity --allow-no-subscriptions`**
  (the script identity has no subscription-level RBAC), and uses an Entra token as
  the psql password. Do **not** revert to `az postgres flexible-server execute` /
  the `rdbms-connect` extension — its pip install fails in that container.
- The Foundry account requires **`publicNetworkAccess: 'Enabled'`** on update.
- The two Postgres `administrators` sub-resources are **serialized** (human admin →
  script admin via `dependsOn`) to avoid a first-deploy "server not accessible for
  AAD operation" race. A first-time deploy may still need one retry.
- **Model deployments were removed from `openai.bicep`** so the account could
  provision — the placeholder catalog entries (formats/versions) weren't valid.
  They must be re-added with values verified against the live catalog (§12).
- Image build is **manual local `docker build` + `docker push`** — `az acr build`
  (ACR Tasks) is blocked on trial-credit subscriptions. A redeploy that changes the
  Job forces a fresh `:latest` pull; a bare re-push of `:latest` can run the stale
  cached image.

**Database stood up (schema + grants applied to the live DB):**
- `db/schema.sql` is applied — `jobs`, `scoring_calls`, `scoring_events`, each with
  its own identity-PK sequence (the per-call / per-event capture grain, §4–§5). The
  app UAMI principal holds **least-privilege grants**: `SELECT, INSERT, UPDATE` on
  the three tables + `USAGE, SELECT` on the sequences — no DELETE/TRUNCATE/ownership.
  The sequence grant is not optional: surrogate-PK inserts fail without it even when
  table `INSERT` is granted.
- **Prereqs for applying the schema from a laptop** — neither is discoverable from
  the error messages, both cost a debugging loop: (a) add your dev IP as a Postgres
  **firewall rule**; `AllowAzureServices` covers only Azure-internal callers, so an
  external IP times out at the TCP layer (looks like a dead server, is actually the
  firewall). (b) Connect as your **UPN** with a fresh **`oss-rdbms`** token as the
  password (`az account get-access-token --resource-type oss-rdbms`), to
  `dbname=jobfinder` — not `postgres`. On the Ubuntu host, `psql` installs via
  `apt`, not `tdnf` (that was the Mariner script container, a different environment).
- **Open decision:** whether the `GRANT` block lives in `schema.sql` (reproducible
  for the next deployer, but couples the file to the `namePrefix`-derived principal
  name, e.g. `jf-dev-uami`) or stays a documented manual step. Leaning toward a
  placeholder-commented reference (`<app-principal>`) in `schema.sql` — reproducible
  guidance without the coupling. If migrations are expected, add `ALTER DEFAULT
  PRIVILEGES IN SCHEMA public GRANT … ON TABLES` so future tables inherit the grants.

**The frontier — bounded next tasks:**
1. **Blob-backed `ConfigSource`** (the mechanism §9 previously deferred; now
   decided). In `config_azure.go`, branch on an `AZURE_STORAGE_ACCOUNT` env the same
   way `wireAzure` branches on a password-bearing DSN: when set (the deployed Job),
   download config from the `config` container via the managed-identity `cred`; when
   unset (docker-compose dev loop), keep reading the local `AZURE_CONFIG_DIR` mount
   unchanged. Only `File` changes; the env-based methods (`Models`, `Temperature`,
   `BatchSize`, …) stay. `newAzureConfigSource` gains a `cred` param, passed from the
   one `wireAzure` already builds. Add the `azblob` dependency.
2. **Wire `AZURE_STORAGE_ACCOUNT`** into the Job env in `containerAppsJob.bicep`,
   sourced from `storage.outputs.name` in `main.bicep`.
3. **`ModelConfig` transcription — gated on harvested values (§11).** Once the
   per-model `BaseURL` / `TPM` / `RPM` / deployment / `AuthScope` values exist,
   write them into `defaultAzureModels` (or the `AZURE_MODELS` override) and
   reconcile the model names against what `openai.bicep` actually deploys.
4. **Stale-comment cleanup:** the `POSTGRES_DSN` note in `containerAppsJob.bicep`
   claims the Go side has no AAD-token wiring — it does (the `BeforeConnect` hook).
   The model-list comment in `main.bicepparam` names an outdated set.
5. **§§4–8 measurement instrument — mostly done; split by gating, NOT one blob.**
   A code read of the pieces understates how much is wired, so be precise:
   - **Done and wired through `handler()`** (verified by tracing the scoring loop,
     not just the files): the calendar-driven batch-size sweep (run-level), run-level
     temperature, the per-model 75%-derated TPM/RPM throttle (§8), protocol-split
     scorer routing (§7), itemized token capture + `usage_raw` backstop, best-effort
     `ev_score` (§4.6), and multi-contributor identity (§10). Don't rebuild these.
   - **Remaining, code-only — NOT blocked on live models** (unlike task 3): the
     **noise-floor tier**. `handler()` runs a single pass and the insert hardcodes
     the literal `"main"` for `run_kind` in `store_azure.go`; there is no path that
     repeat-scores a representative subset per model and tags it `"floor"`. The
     schema is ready (`run_kind` = `'main' | 'floor'`, distinguishing the two
     sampling tiers) but the invocation logic isn't. This is writable now — its
     only dependency is a **Scotty decision**, not a live value: *how a floor run is
     triggered* (separate cron / an env-arg on the Job / a distinct execution),
     which determines what the code branches on. Claude Code can plumb the
     repeat-loop and a real `run_kind` once that trigger is decided; it can't invent
     the trigger.
   - **Remaining, verify-only — blocked on live scoring** (folds into launch-day,
     §11): confirm the `ev_score` EV path actually *fires* against a real
     logprob-returning model rather than always silently falling back to the emitted
     integer, and that throttle/token-capture behave against real provider response
     shapes. Can't be proven without a real scoring call (needs task 3's deployed
     models).

> Read code in **execution order** (`main` → `wireAzure` → `handler` →
> `collect`/`filter`/scorer/store), not file order, and treat "why this instead of
> the obvious simpler thing" as the comprehension test.

---

## 3. Architecture seams

`App` holds `Store`, `ConfigSource`, `Secrets`, `Scorer` behind interfaces so the
same `handler()` runs on either cloud. **Cloud selection lives in `wireAWS`/
`wireAzure`.** The scoring layer must *not* care about cloud — it cares about
**wire protocol** (see §7). This is the key seam correction for the use frame: the
axis that varies is no longer the cloud, it's the provider protocol.

---

## 4. Measurement decisions (the core of the instrument)

### 4.1 Store every scoring event; never overwrite
`scores` **appends one row per (job × model × run)**, surrogate PK, with
`scored_at`. AWS's DynamoDB store overwrites one item per job; the Azure store must
not. Rationale: drift, consistency, and cost analyses all need the full history of
scoring events. Collapsing to one-row-per-job destroys the second moment
(variance) that the consistency question depends on. `scored_at` is also
load-bearing for cost derivation, which is downstream/out-of-scope (§4.3).

### 4.2 Capture token facts maximally itemized — they are perishable
The API reports token counts **once, in the response**, and they are **not
reconstructable later**. Capture, per event, as finely as each provider reports:
`input_uncached`, `cache_read`, `cache_write`, `output` (visible), `reasoning`
(hidden CoT), plus **the raw `usage` JSON blob verbatim** as backstop. Null means
"provider didn't itemize it," which is itself information. Store `batch_size` and
all submission conditions alongside (§4.5) — granular counts are uninterpretable
without the conditions that produced them.

**Do not** compute or store cost/ratios/apportionment at capture time. Store the
atomic facts; derive summaries later. *Rule: store what's perishable and atomic;
derive what's reconstructable.*

### 4.3 Do not compute or store cost — capture tokens instead
The program must **never calculate or persist cost**. Cost = tokens × prices;
prices are mutable (they change, or a rate is later found mis-read), so any stored
cost is wrong the moment they do. Tokens are immutable facts. So the app's only job
here is to **capture the full itemized token facts** (§4.2) and store nothing
derived from them. Cost is computed later, **outside this program**, from the
captured tokens and a separate price table — not app code, out of scope for what
you build here.


### 4.4 Batch size is **swept**, not fixed; batch=1 is one condition among equals
There is **no default batch size**. Batch size is a variable, rotated across
`[1,2,3,5,10]` (mechanics in §4.5). Each size is a legitimate condition serving a
real question; none is over-weighted.

Batch=1 has one property the others lack: **per-job output/reasoning tokens are
measurable only there.** In a batched call the API returns *one* `completion_tokens`
for the whole batch; the per-job split was never emitted, so any apportionment
(input-proportional, equal, etc.) is a *fabrication* — and input-proportional
apportionment is specifically *biased*, because reasoning tracks job *difficulty*,
not JD *length*, so the error correlates with the very signal being studied. So
per-job token cost must be **measured at batch=1, not assumed from batched runs**.

But **necessity is not importance.** Batch=1 being the only place a quantity is
measurable does *not* make it the primary regime or earn it extra sampling. The
batch-size→consistency and batch-size→cost curves are equally central, and they
need the *other* sizes with equal weight to have any shape; amortization/cache
economics are only observable *by* batching. Over-sampling batch=1 would starve the
curves to over-power a variable already adequately covered. Treat all five sizes as
co-equal conditions (§4.5, equal rotation).

### 4.5 Batch size is **run-level and constant within a comparison**
Batch size changes what's measured (anchoring, attention dilution, per-job
reasoning compression), so it is a **treatment**, not a nuisance. Any two things
compared must share a batch size, else model-vs-batch is unidentifiable. Therefore
batch size is a **single per-run env value** (`BATCH_SIZE`), inherited by all
models in that run — *not* a per-model field.

**Sweep design:** rotate batch size across `[1,2,3,5,10]` **daily**, not in weekly
blocks — interleaving decorrelates batch size from calendar/feed drift. **Equal
allocation** — each size gets the same number of days (~6 over a clean ~30-day
window); no size is over-sampled (§4.5). Use a **max-spread, cycle-rotated
permutation** (e.g. `1,10,2,5,3`, re-permuted each cycle) so adjacent days span the
range *and* each size walks across weekdays by construction (near-Latin-square),
rather than a numeric cycle (leaves fixed day-of-week pairings) or pure random (can
hand a bad single realization on a one-shot window). Rescore the **fixed panel**
under every batch size so batch size varies against constant jobs.


### 4.6 Logprob EV over integer scoring
Store two separate columns per scoring event; never collapse them into one:
- **`emitted_score`** — the number the model actually returned. Always populated.
  This is a raw fact from the response.
- **`ev_score`** — the probability-weighted expected value over the score token
  (`bestEffortScoreEV`), computed from logprobs. **Nullable:** `NULL` whenever
  logprobs are absent or unusable and the EV path didn't fire.

Why both, separate: they are different facts (one returned, one derived from it),
and the EV path fires **unevenly across the catalog** (providers differ in whether
they return usable logprobs). A single blended "score" column would silently mix
two kinds of measurement in a way that correlates with model — and would erase, per
row, which one you got. Keeping them apart preserves provenance and lets "how much
does EV differ from emitted, and for which models" be answerable.

- The **`NULL` on `ev_score` is itself the provenance signal** — it marks exactly
  the rows where logprobs weren't usable. No separate flag needed.
- EV is **best-effort, never required.** A model returning no usable logprobs still
  produces a valid row: real `emitted_score`, `NULL` `ev_score`.
- **Known gap:** multi-token numerals (a score split across tokens) aren't handled
  by the EV path → `ev_score` is `NULL` there too. Don't "fix" this by guessing;
  leave the fallback.
- Requesting logprobs is gated by `WantLogprobs` on `ModelConfig` (§6).


### 4.7 Temperature is a run-level variable, not per-model
Temperature is **run-level** (e.g. a `TEMPERATURE` env value), constant across all
models within a run — **not** a `ModelConfig` field. Same rule as batch size (§4.5):
a variable compared across models must be held constant across the comparison, or
model-vs-temperature is confounded and unidentifiable. If temperature is swept, it
is applied **uniformly to every model on a given run** and varied *across* runs, not
between models within one run.

Code requirements:
- Read one temperature per run; apply to every model's scoring request.
- **Record the temperature actually used, per scoring event** (a column on the
  row) — so it's a groupable condition, and so any provider-side clamping/reinterp
  is visible (providers differ in how they honor a given value; capture what was
  sent, don't assume it was obeyed).

---

## 5. Capture grain (analysis is out of scope for this program)
This program **collects data only**. It does not compute consistency, correlations,
cost, aggregates, or any analysis — those happen later, off-machine, against the
collected rows. Do **not** add stdev/correlation/cost/summary logic to the app.

The program's sole obligation is to **capture each scoring event at a fine enough
grain that downstream analysis is possible** — the raw atomic facts (§4.1–4.2,
§4.6) plus **every condition under which the event was produced**, one un-aggregated
row per event. Capture conditions maximally: any variable that could differ between
events and isn't recorded becomes an unrecoverable confound on a non-repeatable run.
If a fact is captured per-event and un-aggregated, the analysis can be done later;
if it's aggregated or dropped at capture, it can't. That asymmetry governs what the
program stores.

---

## 6. ModelConfig vs run-level params

**Per-model (`ModelConfig`)** — instance/protocol data:
- `Name` (model string) 
- `Deployment` (Azure OpenAI deployment; distinct from Name)
- `BaseURL` (endpoint)
- `Protocol` (which scorer to use — see §7)
- `AuthScope` / credential kind (differs per endpoint: Cognitive Services scope,
  Gemini key, OpenAI-compatible key)
- `TPM` 
- `RPM` (per-deployment rate limits)
- `WantLogprobs`

**Explicitly NOT in ModelConfig:**
- **Per-token prices** → not stored/computed by the app at all (§4.3).
- **Temperature** §4.7
- **Batch size, RescoreEveryRun** → **run-level** (§4.5), constant across models
  within a run.

---

## 7. Scorer seam: split by **protocol**, not cloud, not provider-name

`azureScorer` is misnamed — it's really an **OpenAI-compatible** scorer (Ollama
today, Azure OpenAI/Foundry later; only `baseURL`/auth/`deployment` change). Rename
accordingly. The seam is **one implementation per distinct wire protocol**, chosen
per model via `ModelConfig.Protocol`:
- `openaiScorer` — the OpenAI `/chat/completions` dialect. Covers OpenAI, most
  Foundry MaaS, Ollama, and likely DeepSeek (it exposes an OpenAI-compatible API).
- `geminiScorer` — genuinely different shape (AWS incumbent).
- Additional protocol scorers **only** if a Foundry model exposes a non-OpenAI
  surface (verify — see §9).

**Consequence:** adding an OpenAI-compatible model is **config only** (a new
ModelConfig row). Only a genuinely new protocol requires code. `handler()` routes
to a scorer by the model's `Protocol` tag; the scorer encodes protocol logic,
ModelConfig supplies instance data.

> Note the real-Azure-OpenAI request shape may differ from the OpenAI-compatible
> one (`/openai/deployments/{deployment}/chat/completions?api-version=…` and an
> `api-key` header vs. Bearer). Reconcile which surface each model uses, and wire
> `Deployment` accordingly, when keyless auth lands.

---

## 8. Throttle logic

Current throttle is Gemini-shaped and hardcoded — will 429 on a mixed panel. Make
it a **function of `BATCH_SIZE` and the per-model `TPM`/`RPM`**, derated to **75%**:
- Token budget = `floor(0.75 * TPM)` per window.
- Request interval = `ceil(60 / (0.75 * RPM))` seconds (ceil the *interval* — flooring
  it rounds you *faster*, toward the limit; ceil keeps the 75% margin protective).
- **Effective throttle = whichever limit binds at the current batch size**: small
  batches are RPM-bound (many requests), large batches TPM-bound (many tokens/req).
  Apply both, derated; the tighter one governs. A token-only throttle will breach
  RPM at batch=1.
- Throttle is **per-model** (each has its own TPM/RPM), recomputed when `BATCH_SIZE`
  changes.


---

## 9. Live-account status & the offline-simulation cap

**The subscription is live** — the "build only the offline *shape* and stop"
boundary that used to govern this section is lifted for auth and config: both are
deployed, and auth is confirmed working live. What remains is the *discipline*, not
the prohibition.

**Auth scopes — now partly confirmed:**
- **Postgres-AAD** (`https://ossrdbms-aad.database.windows.net/.default`) is
  **confirmed correct** — a live Job run authenticates to Postgres with it.
- **Cognitive Services** scope for Azure OpenAI is still **unexercised** until the
  model deployments are re-added and a real scoring call runs. Keep it marked
  to-confirm until then; don't assume it because Postgres works.

**Config delivery — mechanism now decided:** blob-backed `ConfigSource` (§2, task
1), reading the `config` container via managed identity, with the local
`AZURE_CONFIG_DIR` mount preserved as the dev-loop fallback. The old
"undecided / don't implement" hold is released.

### Still the rule: don't over-simulate offline — "buildable" ≠ "worth simulating"
Three tiers of work; the middle one is a trap:
1. **Pure logic, no external dependency** (schema, throttle math, batch rotation,
   JSON parsing, ModelConfig plumbing) — build *and* genuinely unit-test. The test
   means something.
2. **Talks to an external service** (scorers, auth hooks) — build, prove the
   *shape* against the **cheapest possible fake** (a hardcoded OpenAI-compatible
   JSON response, a fake token provider), then **stop**. Do **not** build
   infrastructure to make the fake realistic.
3. **Behavior only exists live** (real endpoint shapes, quotas, a scope's real
   handshake) — prove it against the real thing, which now exists; don't build a
   simulator to stand in for it.

The cautionary tale, from a real prior mistake: a past session stood up a full local
Ollama/Phi-4 serving stack so the scorer could be "tested" offline. That validated
*that Ollama works*, not *that the scorer is correct* — a stub returning canned JSON
would have proven the wiring for a fraction of the effort. **Do not build local
models, mock servers, or realistic simulators to make offline tests more lifelike.**
A trivial stub is cheaper *and* more honest: a realistic local harness breeds false
confidence ("it worked against Ollama!") that masks real integration gaps (e.g.
Azure OpenAI's request shape differing from Ollama's — see §7).

---

## 10. Multi-contributor optionality (design for it now; recruit later)

The strongest version of this study is **multi-subject**: other people running
their own months with **different resumes/instructions/panels**. That's the only
way to ask whether fit-scoring quality is a property of the *model* or of the
*person-model match* — turning "model X agrees with *me*" into a general claim. It
also sidesteps the single-window sample-size limit.

Two cheap-now / expensive-later design consequences — build them in even if you run
only your own month:
- **Per-event contributor/config identity:** contributor id, resume/config id,
  instructions version, on every row. Without it, person-effects and model-effects
  are inseparable — the whole point.
- **Turnkey redeployability:** the Bicep + container + config must stand up from
  scratch on a *stranger's* subscription with *their* inputs. This is the same bar
  as "day-one deploy-and-run," and the keyless-auth/config-delivery work above is
  exactly what makes it possible. "A distributable measurement harness multiple
  people deployed to their own cloud accounts" is a far stronger portfolio claim
  than a personal script.

---

## 11. Working agreements (leash)
- **Understand before changing.** Explore/read code before executing edits; prefer
  plan-mode (`defaultMode: "plan"` in `.claude/settings.json`).
- **Don't undo §4–§8 decisions to simplify.** They look like optimization targets;
  they're validity constraints on a window that can't be rerun. Surface, don't
  silently "fix."
- **Raw SQL via `pgx/v5` / `pgxpool`**, no ORM (matches existing style;
  `lib/pq` avoided — maintenance mode).
- **No secrets in this file or any committed file.** Reasoning and structure only.
- **Ownership boundary (mechanical vs. account-bound).** Claude Code owns the
  self-contained mechanical work: the blob `ConfigSource`, the Bicep env wiring,
  `ModelConfig` transcription *once the harvested values exist*, and comment
  cleanup. Scotty owns anything account-bound or judgment-heavy: choosing and
  deploying the Foundry models and harvesting their real
  `BaseURL`/`TPM`/`RPM`/scope values (these exist nowhere Claude Code can reach —
  only in the live portal, post-deploy), the deploy-capacity/quota decisions, and
  **live-DB DDL and privilege grants** (schema + least-privilege grants were applied
  this session — see §2; this class of work touches the live DB with Scotty's
  identity, and over-privileging is exactly what to see rather than delegate). If a
  task needs a value that only exists in the live account, it stays Scotty's until
  that value is in hand.
- **README is deferred** until the program is complete and the repo/goal split is
  finalized. This file is committed *with the code* so the reasoning travels with it.

---

## 12. Model panel (selected) & launch-day wiring

The candidate selection is **done**; the sheet was scaffolding for it and is not
needed by the program. What the program needs is per-model operational config.
Selection rule was objective: **open-weight + current-generation + chat-capable**.
Capability tiers are deliberately **not** pre-labeled — the capability axis is meant
to emerge from the collected data, not be asserted up front. Architecture is a
**recorded tag**, not the selection spine (the current open-weight frontier is
almost entirely MoE, so a clean Dense-vs-MoE split isn't available). Two
**within-company, within-generation matched pairs** preserve a partial architecture
contrast: Gemma-4-31B (Dense) vs Gemma-4-26B-A4B (MoE), and Qwen3.6-27B (Dense) vs
Qwen3.6-35B-A3B (MoE).

### The 12 (build-now prior)
All are near-certainly **OpenAI-compatible** (`protocol = openai`). This is the
documented prior to build against; the *instance* values are VERIFY-on-launch (below).

| # | Model | Company | Arch | Protocol (prior) | Serving (VERIFY) |
|---|-------|---------|------|------------------|------------------|
| 1 | DeepSeek-V4-Pro | DeepSeek | MoE | openai | native or Fireworks |
| 2 | DeepSeek-V4-Flash | DeepSeek | MoE | openai | native or Fireworks |
| 3 | Kimi-K2.6 | Moonshot | MoE | openai | native or Fireworks |
| 4 | Kimi-K2.5 | Moonshot | MoE | openai | native or Fireworks |
| 5 | MiniMax-M2.5 | MiniMax | MoE | openai | Fireworks (FW-) |
| 6 | GLM-5.2 | Zhipu | MoE | openai | Fireworks (FW-) |
| 7 | Nemotron-3-Super-120B-A12B | NVIDIA | MoE | openai | Fireworks (FW-) |
| 8 | Qwen3.6-35B-A3B | Alibaba | MoE | openai | Fireworks (FW-) |
| 9 | Qwen3.6-27B | Alibaba | Dense* | openai | Fireworks (FW-) |
| 10 | Gemma-4-26B-A4B | Google | MoE | openai | Fireworks (FW-) |
| 11 | Gemma-4-31B | Google | Dense | openai | Fireworks (FW-) |
| 12 | Qwen3.5-397B-A17B | Alibaba | MoE | openai | Fireworks (FW-) |

\* Qwen3.6-27B Dense is a moderate-confidence architecture call — verify before
relying on it as a matched-pair anchor.

**Serving path matters even though protocol is the same:** Fireworks-served (`FW-`)
deployments are OpenAI-compatible but sit at a *different base URL / path* (and
possibly different auth) than native Foundry deployments. That's exactly why
`BaseURL` is a per-model `ModelConfig` field (§6), not a global constant. Same
`protocol` tag, different `BaseURL`.

### VERIFY-on-launch (these values only exist once the account/deployments exist)
Per model, fill into the ModelConfig slot that is **already built** pre-launch:
- `BaseURL` — exact endpoint (native Foundry vs Fireworks path differ)
- `Deployment` — the deployment string, if the native Azure-OpenAI path is used
- `TPM` / `RPM` — your assigned per-deployment quotas (drive the §8 throttle)
- `AuthScope` / credential — confirm the Cognitive Services token scope live
- The OpenAI-compatible vs native Azure-OpenAI request shape (§7) per deployment

**Launch day is verification, not construction.** If the build is truly done, day
one is: provision via Bicep → read back the values above → drop them into config →
confirm managed-identity auth connects live → shakedown run. Every VERIFY item must
have its ModelConfig **slot already built** beforehand, so launch day is *fill-in +
confirm*, never *write code*. Clear the whole day — not for building under credit
pressure, but because first-contact auth/endpoint/quota reality *will* surprise you
and you want slack to debug without burning the window.

### Closed frontier models (deferred, additive, week-1 gated)
Closed models (e.g. `gpt-5.x`, `claude-*`) are **not** in the panel now and may be
added after **week-1 measured cost** shows headroom against the $200 window. Rules:
- **Additive, never destructive.** Adding closed rows extends the dataset; the
  architecture analysis simply runs on the known-architecture subset and the closed
  rows are absent from *that one* graphic. Nothing is removed; no other cut breaks.
- They are a **cost/capability ceiling reference**, not part of the architecture
  contrast (architecture = `Closed` for them).
- **Gate on real data:** extrapolate week-1 cost-per-job to the remaining weeks;
  one frontier closed model can cost several open ones combined. "A couple OpenAI +
  an Anthropic" may be one slot's worth of budget — the data decides.
- Mid-run additions get **fewer days**, so they yield a partial (cost/capability)
  comparison, not the many-repeat consistency data. Add on the *same panel &
  conditions* or they aren't comparable.
- **Anthropic is the one code-touching add:** `claude-*` on Foundry uses the
  **Messages** API, not OpenAI-compatible — it needs the third protocol scorer
  (`geminiScorer`/`openaiScorer` → three-way). Budget ~30 min, not a config row.
