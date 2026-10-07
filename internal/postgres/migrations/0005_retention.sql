CREATE TABLE admission_control (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    retained_jobs integer NOT NULL CHECK (retained_jobs >= 0),
    max_jobs integer NOT NULL CHECK (max_jobs >= 1),
    replay_window interval NOT NULL CHECK (
        replay_window BETWEEN interval '1 minute' AND interval '30 days'
    ),
    CONSTRAINT admission_control_capacity CHECK (retained_jobs <= max_jobs)
);

-- Preserve every pre-migration row. If an existing installation already has
-- more than the default cap, hold capacity at its current size until terminal
-- records become eligible for cleanup.
INSERT INTO admission_control (retained_jobs, max_jobs, replay_window)
SELECT count(*)::integer, greatest(10000, count(*)::integer), interval '24 hours'
FROM jobs;

CREATE FUNCTION reserve_job_capacity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE admission_control
    SET retained_jobs = retained_jobs + 1
    WHERE singleton AND retained_jobs < max_jobs;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'job capacity reached'
            USING ERRCODE = 'check_violation',
                  CONSTRAINT = 'admission_control_capacity';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION release_job_capacity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE admission_control
    SET retained_jobs = retained_jobs - 1
    WHERE singleton AND retained_jobs > 0;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'retained job counter underflow'
            USING ERRCODE = 'check_violation',
                  CONSTRAINT = 'admission_control_retained_jobs';
    END IF;
    RETURN OLD;
END;
$$;

-- AFTER INSERT does not fire for ON CONFLICT DO NOTHING, so idempotent replay
-- never consumes another slot. Both trigger updates lock the singleton row;
-- concurrent inserts/deletes therefore cannot overshoot or lose a count.
CREATE TRIGGER jobs_reserve_capacity
AFTER INSERT ON jobs
FOR EACH ROW EXECUTE FUNCTION reserve_job_capacity();

CREATE TRIGGER jobs_release_capacity
AFTER DELETE ON jobs
FOR EACH ROW EXECUTE FUNCTION release_job_capacity();

CREATE INDEX jobs_terminal_retention_idx ON jobs (updated_at, id)
WHERE state IN ('succeeded', 'failed', 'canceled');
