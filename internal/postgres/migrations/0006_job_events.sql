CREATE TABLE job_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id text COLLATE "C" NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    kind text COLLATE "C" NOT NULL CHECK (kind IN ('submitted', 'claimed')),
    attempt integer NOT NULL CHECK (attempt >= 0),
    lease_owner text COLLATE "C",
    lease_version bigint,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    CONSTRAINT job_events_shape_check CHECK (
        (kind = 'submitted' AND attempt = 0 AND lease_owner IS NULL AND lease_version IS NULL)
        OR
        (kind = 'claimed' AND attempt >= 1
            AND lease_owner IS NOT NULL
            AND lease_owner ~ '^[A-Za-z0-9._:-]{1,128}$'
            AND lease_version IS NOT NULL AND lease_version >= 1)
    )
);

CREATE UNIQUE INDEX job_events_submitted_once_idx
ON job_events (job_id) WHERE kind = 'submitted';

CREATE UNIQUE INDEX job_events_claim_once_idx
ON job_events (job_id, lease_version) WHERE kind = 'claimed';

CREATE INDEX job_events_job_order_idx ON job_events (job_id, id);

-- Older rows have a reliable admission timestamp but no trustworthy history
-- for transitions that happened before event recording existed.
INSERT INTO job_events (job_id, kind, attempt, created_at)
SELECT id, 'submitted', 0, created_at
FROM jobs
ORDER BY created_at, id;
