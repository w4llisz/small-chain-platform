ALTER TABLE job_events
    DROP CONSTRAINT job_events_kind_check,
    DROP CONSTRAINT job_events_shape_check,
    ADD COLUMN state text COLLATE "C",
    ADD COLUMN message text;

UPDATE job_events
SET state = CASE kind
    WHEN 'submitted' THEN 'queued'
    WHEN 'claimed' THEN 'running'
END;

ALTER TABLE job_events
    ALTER COLUMN state SET NOT NULL,
    ADD CONSTRAINT job_events_kind_check CHECK (kind IN (
        'submitted', 'claimed', 'succeeded', 'retry_scheduled',
        'failed', 'lease_recovered', 'canceled'
    )),
    ADD CONSTRAINT job_events_state_check CHECK (state IN (
        'queued', 'running', 'retrying', 'succeeded', 'failed', 'canceled'
    )),
    ADD CONSTRAINT job_events_message_check CHECK (
        message IS NULL OR octet_length(message) BETWEEN 1 AND 1024
    ),
    ADD CONSTRAINT job_events_shape_check CHECK (
        (kind = 'submitted' AND state = 'queued' AND attempt = 0
            AND lease_owner IS NULL AND lease_version IS NULL AND message IS NULL)
        OR
        (kind = 'claimed' AND state = 'running' AND attempt >= 1
            AND lease_owner IS NOT NULL
            AND lease_owner ~ '^[A-Za-z0-9._:-]{1,128}$'
            AND lease_version IS NOT NULL AND lease_version >= 1
            AND message IS NULL)
        OR
        (kind = 'succeeded' AND state = 'succeeded' AND attempt >= 1
            AND lease_owner IS NOT NULL
            AND lease_owner ~ '^[A-Za-z0-9._:-]{1,128}$'
            AND lease_version IS NOT NULL AND lease_version >= 1
            AND message IS NULL)
        OR
        (kind = 'retry_scheduled' AND state = 'retrying' AND attempt >= 1
            AND lease_owner IS NOT NULL
            AND lease_owner ~ '^[A-Za-z0-9._:-]{1,128}$'
            AND lease_version IS NOT NULL AND lease_version >= 1
            AND message IS NOT NULL)
        OR
        (kind = 'failed' AND state = 'failed' AND attempt >= 1
            AND lease_owner IS NOT NULL
            AND lease_owner ~ '^[A-Za-z0-9._:-]{1,128}$'
            AND lease_version IS NOT NULL AND lease_version >= 1
            AND message IS NOT NULL)
        OR
        (kind = 'lease_recovered' AND state IN ('retrying', 'failed') AND attempt >= 1
            AND lease_owner IS NOT NULL
            AND lease_owner ~ '^[A-Za-z0-9._:-]{1,128}$'
            AND lease_version IS NOT NULL AND lease_version >= 1
            AND message IS NOT NULL)
        OR
        (kind = 'canceled' AND state = 'canceled' AND attempt >= 0
            AND lease_owner IS NULL
            AND lease_version IS NOT NULL AND lease_version >= 1
            AND message IS NOT NULL)
    );

CREATE UNIQUE INDEX job_events_worker_outcome_once_idx
ON job_events (job_id, lease_version)
WHERE kind IN ('succeeded', 'retry_scheduled', 'failed');

CREATE UNIQUE INDEX job_events_recovery_once_idx
ON job_events (job_id, lease_version)
WHERE kind = 'lease_recovered';

CREATE UNIQUE INDEX job_events_terminal_once_idx
ON job_events (job_id)
WHERE state IN ('succeeded', 'failed', 'canceled');
