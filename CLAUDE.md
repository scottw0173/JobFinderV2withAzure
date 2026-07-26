# CLAUDE.md — Azure Main Run: Fixed 30-Job Panel

Task brief for a Claude Code session on the `azure` branch. Run in **plan mode**;
propose before editing.

## Goal

Cap the Azure main run at a **fixed, score-stratified panel of 30 jobs**, selected
once, persisted with a frozen snapshot of each job's text, and reused verbatim on
every scheduled run.

Current behavior: the main run scores the full post-keyword-filter set (~400–500
jobs) once per model, per run. This change replaces "score everything today" with
"score the same curated 30 every day."

## Design decisions — READ FIRST

**1. Fixed panel, NOT fresh-daily.** The 30 are chosen once and reused; they are not
re-sampled each run. Holding job identity constant is what lets a score change over
the window be attributed to the *model* rather than to job-set churn. A freshly
sampled set each day reintroduces that confound and invalidates the comparison.

**2. Instrument, NOT sample.** The panel must span the score range, not mirror the
population. The majority of scraped jobs are non-viable; a naive random draw risks 30
duds with nothing above the viability floor, which kills discriminating power — every
model agrees on an obvious reject, so there is nothing to differentiate or drift. The
panel is deliberately weighted toward the informative (borderline + viable) region
with a few low anchors. It is a curated instrument for model comparison, not a
representative market sample.

**3. Size = 30.** Chosen so it divides evenly by every batch size in the [1,2,3,5,10]
sweep (30/b = 30, 15, 10, 6, 3), keeping batch partitioning balanced. Do not use 25
(fails 2, 3, 10).

## Explore before editing

Read and summarize these before proposing changes — do not assume shapes:

- The main-run scoring loop in `main.go` — how it iterates the filtered set and hands
  jobs to the scorer (the scorer is reused for the screening pass below).
- The job struct and how `stablekey` is derived; where `company`/employer lives.
- The scrape → keyword-filter pipeline — where the ~400–500 set is materialized; that
  set is the input to both the screening pass and panel selection.
- The `scoring_calls` schema and pgx/pgxpool conventions (raw SQL, no ORM).
- Whether any existing per-job production score is already reachable (optional; see
  screening pass).

Report the plan; wait for approval before writing code.

## Screening pass (provisional score for stratification)

To stratify by score you need a score before the panel exists. Resolve at
**panel-build time only** (not on every run):

1. Score the full post-filter set once with a single cheap model (`ScreeningModel`,
   default DeepSeek-V4-Flash).
2. Use that provisional score **only** to bucket jobs into bands — never as
   experimental data. Coarse rank-ordering is all that's needed, so a flash model
   suffices. (Caveat: this guarantees spread in the screener's judgment; other models
   may compress it. Acceptable — the goal is only to avoid an all-duds panel.)
3. If a production per-job score is already reachable, it may be reused instead — but
   default to the screening pass so the Azure path stays self-contained and portable.

Cost is one cheap model over ~450 jobs, once per panel build — negligible.

## Selection algorithm (score-band × company, seeded)

Build from **today's** post-filter set at panel-build time:

1. Screening pass → provisional score per job.
2. Bucket jobs into score bands per `ScoreBands` edges.
3. Allocate the 30 slots across bands per `BandTargets`. **Floor the top band** so the
   scarce viable jobs are guaranteed present; keep a few low anchors.
4. Within each band, seeded **round-robin across companies** to fill that band's
   allotment — only take a company's 2nd after every company in the band has a 1st.
   Company spread is the secondary balance; score-band coverage is primary.
5. Seed from `PanelSeed` for reproducibility; persist the result.

## Persistence & schema

New table (propose DDL; **do not** run the migration — see guardrails). Each run reads
the active panel and scores the **snapshot**, not a fresh scrape:

```sql
CREATE TABLE panel_jobs (
  panel_id        text        NOT NULL,   -- one panel build (seed + built_at tag)
  stablekey       text        NOT NULL,
  company         text        NOT NULL,
  posted_at       timestamptz,
  score_band      text        NOT NULL,   -- band this job was selected into
  screening_score double precision,       -- provisional score used for selection (audit)
  job_snapshot    jsonb       NOT NULL,   -- frozen job payload used as scorer input
  seed            bigint      NOT NULL,
  built_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (panel_id, stablekey)
);
```

- **Build path:** when no active panel exists (or `RebuildPanel` is set), run the
  screening pass + selection, write 30 rows, record `panel_id`/`seed`.
- **Run path:** `SELECT ... WHERE panel_id = <active>`; feed `job_snapshot` to the
  scorer. Scoring output continues to land in `scoring_calls` unchanged — this task
  only alters *which* jobs get scored and *where the input text comes from*.

## Why snapshot the job text

Freezing the description at build time means every run scores the **identical
prompt**, so day-to-day movement is model drift, not an edited or expired posting. It
also decouples the panel from the daily scrape: a job disappearing from the source no
longer breaks the run. Do **not** re-fetch panel jobs from the live source per run.

## Config knobs (no hardcoding)

Expose via the existing config path (env/blob config, consistent with `RunMode`/model
config) so a future user can retune without code edits:

- `PanelSize` — default `30`
- `ScreeningModel` — default DeepSeek-V4-Flash
- `ScoreBands` — band edges over the screening-score range
- `BandTargets` — slot count per band (top band floored)
- `PanelSeed` — required; recorded with the panel for reproducibility
- `MaxPerCompany` — optional secondary cap
- `ActivePanelID` / `RebuildPanel` — select or regenerate the panel

## Out of scope / guardrails

- **Do not touch:** model configs / `azureModelsJson`, auth (managed identity, Entra
  token acquisition), RBAC, Bicep/IaC, or the scoring logic and prompt.
- **Do not apply the migration.** Propose the DDL; schema changes are applied by hand.
- Keep the change additive — the full-set scoring path stays behind config, not
  deleted.
- Batch partitioning of the 30 and floor/noise-floor runs are **separate tasks**; do
  not implement them here. This task only needs 30 to be batch-divisible, not the
  batching itself.

## Commit

`feat`: fixed score-stratified 30-job panel for the Azure main run.
