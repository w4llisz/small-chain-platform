ALTER TABLE jobs
    DROP CONSTRAINT jobs_state_check,
    DROP CONSTRAINT jobs_attempts_check,
    ADD COLUMN max_attempts integer,
    ADD COLUMN available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ADD COLUMN lease_owner text COLLATE "C",
    ADD COLUMN lease_version bigint NOT NULL DEFAULT 0,
    ADD COLUMN lease_expires_at timestamptz;

UPDATE jobs
SET max_attempts = substring(
    spec::text FROM '"max_attempts"[[:space:]]*:[[:space:]]*([0-9]+)'
)::integer;

ALTER TABLE jobs
    ALTER COLUMN max_attempts SET NOT NULL,
    ADD CONSTRAINT jobs_state_check CHECK (
        state IN ('queued', 'running', 'retrying', 'succeeded', 'failed', 'canceled')
    ),
    ADD CONSTRAINT jobs_attempts_check CHECK (
        attempts >= 0 AND attempts <= max_attempts
    ),
    ADD CONSTRAINT jobs_max_attempts_check CHECK (max_attempts BETWEEN 1 AND 5),
    ADD CONSTRAINT jobs_lease_version_check CHECK (lease_version >= 0),
    ADD CONSTRAINT jobs_lease_owner_check CHECK (
        lease_owner IS NULL OR lease_owner ~ '^[A-Za-z0-9._:-]{1,128}$'
    ),
    ADD CONSTRAINT jobs_lease_state_check CHECK (
        (state = 'running' AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR
        (state <> 'running' AND lease_owner IS NULL AND lease_expires_at IS NULL)
    );

CREATE INDEX jobs_ready_idx ON jobs (available_at, id)
WHERE state IN ('queued', 'retrying') AND attempts < max_attempts;
