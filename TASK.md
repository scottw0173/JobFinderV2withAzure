TASK: Two-tier (initial/full) Foundry deployment + auto-generated AZURE_MODELS,
      with the screener separated from the daily grade loop.

SCOPE: infra/modules/openai.bicep, infra/main.bicep, infra/main.bicepparam,
       azure_config.go, and handler()'s grade loop (STEP 3 only, gated on STEP 0b).
       Touch nothing else.

RULES:
  - Do NOT run any `az deployment`. Produce diffs + a `what-if` command for Scotty.
  - Stop for review before any commit. No auto-commit.
  - Never fabricate a model version string. Placeholders are marked VERIFY;
    Scotty fills real versions from the live catalog.

────────────────────────────────────────────────────────────────────────
STEP 0 — READ AND REPORT (do first; WAIT for Scotty before wiring):

  0a. Read handler() + the panel-build path. Report: does the daily grade loop
      iterate every entry Models() returns, or does it already exclude the
      screener?
  0b. Report whether a one-line skip in the grade loop
      (`if model.Name == cfg.ScreeningModel() { continue }`) is clean —
      specifically: is the screener name in scope at the loop, and does the loop
      feed batch-size / token accounting that a skip would miscount?
  Do NOT proceed to STEP 3's loop edit until Scotty gives go/no-go on 0b.
  Do NOT proceed to the JSON build until Scotty confirms (expected: screener IS
  included in azureModelsJson, because Models() is where its BaseURL/TPM/RPM come
  from — but confirm against 0a).

────────────────────────────────────────────────────────────────────────
STEP 1 — infra/modules/openai.bicep:
  - Add:  param enableFullPanel bool = false
  - Add:  param screenerModel object     // {name, model, version, capacity}; tier-0 (gpt-5-mini)
  - Keep: param modelDeployments array    // tier-1 graders (the 8-minus-screener set)
  - Add:  var activeModels = enableFullPanel
            ? concat([screenerModel], modelDeployments)
            : [screenerModel]
  - Point the existing @batchSize(1) loop at activeModels.
    (Screener ALWAYS deploys; graders only when enableFullPanel = true.)
  - Leave outputs (id / endpoint / name) unchanged.

────────────────────────────────────────────────────────────────────────
STEP 2 — infra/main.bicep:
  - Add:  param enableFullPanel bool = false ; pass through to the openai module.
  - Lift screenerModel + the grader array UP to main.bicep as the single source
    of truth; feed the same values into (a) the openai module and (b) the JSON
    build below.
  - Build azureModelsJson from that same source:
      * Screener entry: ALWAYS present (both flag states).
      * Grader entries: present ONLY when enableFullPanel = true.
        Day-1 JSON must NOT list the tier-1 graders — they don't exist yet, so
        the scorer would 404 every run.
  - Set azureScreeningModel = screenerModel.name  (replaces the '' default).
  - openai.outputs.endpoint → containerAppsJob is already wired; leave it.

────────────────────────────────────────────────────────────────────────
STEP 3 — azure_config.go + handler():

  3a. ScreeningModel() — remove the default; return the raw env value:

        func (c *azureConfigSource) ScreeningModel() string {
            return strings.TrimSpace(os.Getenv("AZURE_SCREENING_MODEL"))
        }

      (Deletes the current "DeepSeek-V4-Flash" default, which mismatches the
      list key anyway.)

  3b. Fatal guard at the PANEL-BUILD consumer (NOT in the getter): when a panel
      build runs with an empty ScreeningModel(), fail loudly with a clear
      message. A grade-only run (RebuildPanel=false, panel exists) must NOT touch
      the screener and must NOT fatal. Mirror the existing PanelSeed() pattern:
      quiet getter, strict consumer.

  3c. (ONLY IF Scotty approved 0b) Add the screener skip to the grade loop so the
      screener fills panel_jobs but never lands in scoring_calls:

        if model.Name == cfg.ScreeningModel() { continue }

      Keeps scoring_calls graders-only by construction, no analysis-time filter.

────────────────────────────────────────────────────────────────────────
STEP 4 — infra/main.bicepparam:
  - enableFullPanel = false   (initial-deploy target)
  - screenerModel = gpt-5-mini entry; version marked VERIFY for Scotty.

────────────────────────────────────────────────────────────────────────
VERIFY BEFORE HANDBACK:
  - `az bicep build --file infra/main.bicep` passes clean.
  - azureModelsJson is valid JSON at BOTH flag states:
      enableFullPanel=false → screener only, well-formed (not empty/broken).
      enableFullPanel=true  → screener + all graders present.
  - `go build ./...` passes; grade-only path does not reference the screener.
  - Produce: the `what-if` command against jobfinder-rg (do not run it).