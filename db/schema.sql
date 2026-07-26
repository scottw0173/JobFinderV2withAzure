-- One row per distinct live posting. Mutable liveness record.
-- Re-scraped jobs re-match on composite_key (regenerated from the scrape),
-- which is why it's the key rather than a surrogate.
CREATE TABLE IF NOT EXISTS jobs (
    composite_key     TEXT PRIMARY KEY,          -- generated: stablekey + posted_at
    stablekey         TEXT NOT NULL,             -- kept separate for querying
    posted_at         TIMESTAMPTZ,               -- kept separate for querying
    source            TEXT NOT NULL,             -- greenhouse | ashby | lever
    title             TEXT,
    company           TEXT,
    url               TEXT,                       -- cheap, kept for spot-checks
    payload           JSONB,                      -- full scraped Job struct
    description_chars INTEGER,                    -- covariate for token/cost analysis
    payload_chars     INTEGER,                    -- full marshaled payload length
    first_seen        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per HTTP scoring call (1..N jobs per call). Holds the call-level
-- token facts the API reports ONCE for the whole batch — never per job.
CREATE TABLE IF NOT EXISTS scoring_calls (
    call_id           BIGSERIAL PRIMARY KEY,
    model             TEXT NOT NULL,              -- logical model (the weights)
    deployment        TEXT,                       -- where served (native vs FW-, endpoint)
    batch_size        INTEGER NOT NULL,           -- jobs in this call
    temperature_sent  DOUBLE PRECISION,           -- what we asked for
    run_kind          TEXT NOT NULL,              -- 'main' | 'floor'
    -- itemized token facts, verbatim from usage; NULL = provider didn't itemize
    input_uncached    INTEGER,
    cached_read       INTEGER,
    cache_write       INTEGER,
    output_tokens     INTEGER,
    reasoning_tokens  INTEGER,
    usage_raw         JSONB,                      -- full usage blob, backstop
    scored_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- multi-contributor identity (CLAUDE.md §10): without these, person-
    -- effects and model-effects are inseparable once data from more than
    -- one contributor exists. Run-level constants, same table as the other
    -- run-level conditions above, not duplicated onto scoring_events.
    contributor_id       TEXT NOT NULL,             -- who ran this
    resume_id            TEXT NOT NULL,             -- which candidate/resume profile
    config_id            TEXT NOT NULL,             -- which broader config bundle (sources/filter/instructions dir)
    instructions_version TEXT NOT NULL              -- content hash of instructions.md at time of use
);

-- One row per (job scored in a call). This is THE event table: one job's
-- result. batch_size lives on the call; per-job token apportionment is NOT
-- stored (fabrication) — derive later from call tokens if ever needed.
CREATE TABLE IF NOT EXISTS scoring_events (
    event_id          BIGSERIAL PRIMARY KEY,
    call_id           BIGINT NOT NULL REFERENCES scoring_calls(call_id),
    composite_key     TEXT NOT NULL REFERENCES jobs(composite_key),
    emitted_score     DOUBLE PRECISION,           -- what the model returned
    ev_score          DOUBLE PRECISION,           -- logprob EV; NULL if unavailable
    reasoning         TEXT,                       -- model's justification text
    raw               JSONB NOT NULL,             -- this job's structured output, verbatim
    logprobs          JSONB                       -- score-token distribution; NULL if unsupported
);

CREATE INDEX IF NOT EXISTS ON scoring_events (composite_key);
CREATE INDEX IF NOT EXISTS ON scoring_events (call_id);
CREATE INDEX IF NOT EXISTS ON scoring_calls (model, scored_at);

-- (CLAUDE.md's fixed score-stratified 30-job
-- panel task). One row per job selected into a panel build. Each run reads
-- the active panel (panel_id with the greatest built_at, unless pinned via
-- ActivePanelID) and scores job_snapshot - the frozen text captured at
-- build time - never a fresh re-scrape, so a job disappearing from the
-- source doesn't break the run and every run scores byte-identical input.
-- No FK to jobs(composite_key): panel jobs must survive the source posting
-- disappearing.
CREATE TABLE IF NOT EXISTS panel_jobs (
  panel_id        text        NOT NULL,   -- one panel build (seed + built_at tag)
  stablekey       text        NOT NULL,
  company         text        NOT NULL,
  posted_at       timestamptz,
  score_band      text        NOT NULL,   -- band this job was selected into
  screening_score double precision,       -- provisional score used for selection (audit only, never scoring_calls)
  job_snapshot    jsonb       NOT NULL,   -- frozen job payload used as scorer input
  seed            bigint      NOT NULL,
  built_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (panel_id, stablekey)
);

CREATE INDEX IF NOT EXISTS ON panel_jobs (built_at DESC);