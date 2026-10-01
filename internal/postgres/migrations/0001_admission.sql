CREATE TABLE jobs (
    id text COLLATE "C" PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
    idempotency_key text COLLATE "C" NOT NULL UNIQUE
        CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{1,128}$'),
    request_fingerprint bytea NOT NULL CHECK (octet_length(request_fingerprint) = 32),
    -- json, not jsonb: preserve valid escaped NUL in a checksum payload.
    spec json NOT NULL CHECK (json_typeof(spec) = 'object'),
    state text NOT NULL DEFAULT 'queued' CHECK (state = 'queued'),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts = 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
