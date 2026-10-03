ALTER TABLE jobs
    ADD COLUMN result text,
    ADD COLUMN error text,
    ADD CONSTRAINT jobs_result_length_check CHECK (
        result IS NULL OR octet_length(result) <= 4096
    ),
    ADD CONSTRAINT jobs_error_length_check CHECK (
        error IS NULL OR octet_length(error) BETWEEN 1 AND 1024
    );

-- Older schemas allowed every state before outcomes were stored. Preserve
-- those rows with explicit "unknown" values so this migration is deployable
-- against a non-empty database instead of only a fresh test database.
UPDATE jobs
SET result = CASE
        WHEN state = 'succeeded' THEN 'result unavailable before migration 0003'
        ELSE NULL
    END,
    error = CASE
        WHEN state IN ('failed', 'canceled', 'retrying')
            THEN 'outcome unavailable before migration 0003'
        ELSE NULL
    END;

ALTER TABLE jobs
    ADD CONSTRAINT jobs_outcome_check CHECK (
        (state = 'succeeded' AND result IS NOT NULL AND error IS NULL)
        OR
        (state IN ('failed', 'canceled') AND result IS NULL AND error IS NOT NULL)
        OR
        (state IN ('queued', 'running') AND result IS NULL AND error IS NULL)
        OR
        (state = 'retrying' AND result IS NULL AND error IS NOT NULL)
    );
