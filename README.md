# JobFinder V2 — cloud-agnostic job scorer & model-drift instrument

JobFinder collects postings from public ATS feeds (Greenhouse, Lever, Ashby), filters them to roles matching a keyword profile, and scores each one against a personal rubric using an LLM. V2 makes the whole pipeline **cloud-agnostic**: the same Go binary runs on either AWS (Lambda/DynamoDB) or Azure (Container Apps Job/PostgreSQL), selected at runtime by the `CLOUD_PROVIDER` env var.

The two backends serve different goals:

- **AWS path** — the original single-invocation daily scorer. A practical tool that surfaces relevant jobs and exports them to a Google Sheet.
- **Azure path** — a **measurement instrument**. It scores a fixed, score-stratified **30-job panel** every day against a **panel of many models**, writing per-call and per-job records to Postgres. The accumulated data measures behavioral drift and inter-model variance on a real scoring task over a non-repeatable window. Optimize this path for measurement validity and data recoverability, not operational tidiness.

This README covers the **Azure path**. The AWS path is documented inline in `template.yaml` / `samconfig.toml`.

---

## Architecture (Azure)

| Component | Role |
|---|---|
| **Azure Container Apps Job** (`<prefix>-job`) | Runs the pipeline on a daily cron (`0 13 * * *`) |
| **PostgreSQL Flexible Server** | Stores jobs, per-call metadata, per-job scores, and the panel. AAD-token auth (no password) |
| **Blob Storage** (`config` container) | Holds `instructions.md`, `sources.json`, `filterKeywords.json` |
| **Azure Key Vault** | Holds external-provider API keys; resolved at runtime by managed identity |
| **Azure Container Registry** | Holds the `jobfinder` image |
| **Azure AI Foundry / Cognitive Services** | Hosts the in-Azure (OpenAI-compatible) models |
| **User-assigned managed identities** | Runtime identity (`<prefix>-uami`) + DB-bootstrap identity; keyless throughout |
| **Bicep** (`infra/`) | Provisions all of the above as one deployment |
| **GitHub Actions** (OIDC) | Keyless image build/push to ACR on push |

**Keyless by design.** The runtime identity authenticates to Postgres, Blob, Key Vault, and Foundry via Entra/managed identity — no secrets in code or Bicep. The *only* stored secrets are the external-provider API keys (below), which live in Key Vault because those providers have no managed-identity path.

---

## The model panel

Two tiers of OpenAI-compatible models run in the same handler loop under one run-level batch size and temperature, routed by `protocol`:

**1. In-Azure (Foundry-hosted).** Defined in `infra/main.bicepparam` (`azureModelsJson`) — 8 models: `gpt-5.4-mini`, `gpt-5.3-codex`, `gpt-5.4-nano`, `gpt-5.4`, `gpt-5.5`, `gpt-5.6-terra`, `gpt-5.6-luna`, `gpt-5.6-sol`. These are deployed to the Foundry account by `infra/modules/openai.bicep`. Add new-region Foundry accounts via `extraOpenAiAccountIds` in the param file.

**2. External providers.** Off-Foundry, OpenAI-compatible endpoints (defined in `config_azure.go`). Each needs an API key stored in **Key Vault** under the exact secret name below:

| Provider | Key Vault secret name |
|---|---|
| Google Gemini | `GEMINI-API-KEY` |
| NVIDIA NIM | `NVIDIA-API-KEY` |
| Mistral | `MISTRAL-API-KEY` |
| Cohere | `COHERE-API-KEY` |

> Locally, override any key with an env var (hyphens → underscores, e.g. `GEMINI_API_KEY`) so no Azure identity is needed for dev.

Distinction that matters for the data: **proprietary models are drift subjects; open-weight models served by a host are frozen baselines.** Running the same weights across multiple hosts measures serving differences, not model drift.

---

## Data model

Four Postgres tables (`db/schema.sql`), append-only except `panel_jobs`:

- **`jobs`** — deduped postings (stable composite key from company+title+location).
- **`scoring_calls`** — one row per model call: config, token usage (itemized + raw usage blob), identity columns.
- **`scoring_events`** — one row per job scored: score, reasoning, raw model output, logprobs.
- **`panel_jobs`** — the fixed 30-job panel (regenerable derived state; the only table with DELETE grant).

**Cost is never stored.** Token facts are captured per call; cost is derived at analysis time from a separate temporal price table, so a price change never corrupts historical rows. Batch size and temperature are **run-level** (constant within a comparison), so they never confound a cross-model comparison.

---

## Setup for a fresh account

Reusable end to end: fork, point the params at your tenant, run four scripts. Prereqs: `az` CLI (logged in), `gh` CLI, Docker, Go, `psql`.

### 1. Set your parameters
Edit `infra/main.bicepparam`:
- `namePrefix`, `location`
- `postgresAdminObjectId` / `postgresAdminPrincipalName` — **your** Entra object ID and UPN (no safe defaults; the placeholders will fail)
- `extraOpenAiAccountIds` — any hand-created Foundry accounts in other regions (else leave empty)
- `azureModelsJson` / `azureScreeningModel` — the Foundry model set and the cheap model used to stratify the panel

### 2. Deploy + build + point the job — `scripts/bootstrap.sh`
Deploys the Bicep stack, then builds (`--platform linux/amd64`), pushes the image (SHA-tagged), and repoints the Job at it. Derives your `contributorId` (hash of your UPN) automatically.
```bash
RG=jobfinder-rg ./scripts/bootstrap.sh
```
> Bicep deploy, image push, and job update are three independent operations. Config-only changes don't need a rebuild; a real image change does.

### 3. Create schema + grants — `scripts/db_setup.sh`
Run **once**, as yourself (the AAD admin), with your client IP on the server firewall. Applies `db/schema.sql` and grants the runtime identity least-privilege access.
```bash
SERVER_FQDN=<pg-fqdn> ADMIN_UPN=<your-upn-#EXT#-form> RUNTIME_UAMI=<prefix>-uami ./scripts/db_setup.sh
```

### 4. Grant yourself Key Vault access, then set the keys — `scripts/kvperm.sh`
Grants your user **Key Vault Secrets Officer** on the deployed vault. **Wait 2–5 min** for propagation, then set the external-provider keys.
```bash
./scripts/kvperm.sh
VAULT=$(az keyvault list -g jobfinder-rg --query "[0].name" -o tsv)
az keyvault secret set --vault-name "$VAULT" --name GEMINI-API-KEY   --value "..."
az keyvault secret set --vault-name "$VAULT" --name NVIDIA-API-KEY   --value "..."
az keyvault secret set --vault-name "$VAULT" --name MISTRAL-API-KEY  --value "..."
az keyvault secret set --vault-name "$VAULT" --name COHERE-API-KEY   --value "..."
```

### 5. (Optional) keyless CI to ACR — `scripts/oidc.sh`
Sets up GitHub OIDC federated credentials so `image.yml` can build and push to ACR with no stored keys. Derives owner/repo IDs from your git remote via `gh`.
```bash
./scripts/oidc.sh
```

### 6. Upload config blobs
Put your `instructions.md` (rubric + résumé), `sources.json`, and `filterKeywords.json` into the `config` blob container. Use the `.example` files in the repo as templates.

### 7. First run
The first run must build the panel, which refuses to proceed without a seed. Set these on the Job before triggering it:
```bash
az containerapp job update -g jobfinder-rg -n <prefix>-job --set-env-vars \
  AZURE_SWEEP_START=$(date -u +%F) \
  AZURE_PANEL_SEED=<nonzero-int> \
  AZURE_REBUILD_PANEL=true
```
Then start it (`az containerapp job start ...`). After the panel exists, unset `AZURE_REBUILD_PANEL`; the daily cron takes over. The batch-size sweep rotates `{1,2,3,5,10}` deterministically from `AZURE_SWEEP_START`.

**Verify the job image before a real run:**
```bash
az containerapp job show -g jobfinder-rg -n <prefix>-job \
  --query "properties.template.containers[0].image" -o tsv
```

---

## Runtime configuration (env)

The Bicep module injects the wiring (`KEYVAULT_URI`, `AZURE_STORAGE_ACCOUNT`, `POSTGRES_DSN`, `AZURE_OPENAI_ENDPOINT`, `AZURE_MODELS`, `AZURE_SCREENING_MODEL`, `AZURE_CONTRIBUTOR_ID`, `AZURE_CLIENT_ID`). The knobs you may set by hand:

| Env | Purpose | Default |
|---|---|---|
| `AZURE_SWEEP_START` | Anchor date for the batch-size sweep | (unset → smallest batch) |
| `AZURE_PANEL_SEED` | Seed for panel build (required for a build) | `0` (refuses) |
| `AZURE_REBUILD_PANEL` | Force a panel rebuild this run | `false` |
| `AZURE_PANEL_ENABLED` / `AZURE_PANEL_SIZE` | Toggle / size the fixed panel | `true` / `30` |
| `AZURE_BATCH_SIZE` | Manual override of the sweep (debug) | (sweep) |
| `AZURE_TEMPERATURE` | Run-level temperature | `1` |
| `AZURE_MAX_PER_COMPANY` | Cap postings per company | `0` (no cap) |

---

## Status / roadmap

- **`openai.bicep` model deployments** — the Foundry *account* deploys today; the 8 model deployment resources are authored but commented out. Wiring them up (the same 8 in `azureModelsJson`) is the next branch.
- **Anthropic Messages scorer** for Claude — deferred; needs a separate protocol implementation.
- **`run_kind` / floor-run tier** — schema-ready, currently hardcoded `"main"`.

## Repo layout

```
.
├── main.go                 # entrypoint; dispatches on CLOUD_PROVIDER
├── config_azure.go / _aws.go   # per-cloud config sources (models, panel, sweep)
├── store_azure.go  / _aws.go   # per-cloud persistence
├── secrets_azure.go / _keyvault.go / _aws.go  # per-cloud secret resolution
├── scorer_openai.go        # OpenAI-compatible scorer (Foundry + external)
├── panel.go / batchsweep.go    # 30-job panel build + batch-size rotation
├── db/schema.sql, db/grants.sql
├── infra/                  # Bicep: main + modules (openai, postgres, keyVault, …)
└── scripts/                # bootstrap, db_setup, kvperm, oidc
```
