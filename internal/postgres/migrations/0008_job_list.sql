CREATE INDEX jobs_recent_idx
ON jobs (created_at DESC, id DESC);

CREATE INDEX jobs_state_recent_idx
ON jobs (state, created_at DESC, id DESC);
