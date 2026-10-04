CREATE INDEX jobs_expired_lease_idx ON jobs (lease_expires_at, id)
WHERE state = 'running';
