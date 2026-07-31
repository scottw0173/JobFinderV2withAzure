-- Azure-only. NOT run by local docker (compose mounts schema.sql only).
-- Run AFTER schema.sql and AFTER the runtime role exists
-- (pgaadauth_create_principal, infra/modules/postgres.bicep), connected to
-- the app DB as the human admin. :uami = runtime identity role, passed via -v.

-- Append-only experimental tables: SELECT/INSERT/UPDATE, no DELETE (protect
-- accumulating data).
GRANT SELECT, INSERT, UPDATE         ON jobs           TO :"uami";
GRANT SELECT, INSERT, UPDATE         ON scoring_calls  TO :"uami";
GRANT SELECT, INSERT, UPDATE         ON scoring_events TO :"uami";

-- Regenerable derived state: + DELETE so the panel can be reset.
GRANT SELECT, INSERT, UPDATE, DELETE ON panel_jobs     TO :"uami";

-- scoring_calls/scoring_events are BIGSERIAL; INSERT needs USAGE on their
-- sequences. panel_jobs/jobs have no sequence.
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public  TO :"uami";