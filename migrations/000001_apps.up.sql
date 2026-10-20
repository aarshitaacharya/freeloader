CREATE TABLE apps (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name              TEXT        NOT NULL UNIQUE,
    key_hash          BYTEA       NOT NULL UNIQUE,   -- SHA-256 of the API key; the key itself is never stored
    rpm_limit         INT         NOT NULL DEFAULT 60,
    tokens_per_day    BIGINT      NOT NULL DEFAULT 100000,
    cache_ttl_s       INT         NOT NULL DEFAULT 86400,
    provider_priority JSONB       NOT NULL DEFAULT '[]',  -- ordered provider names; empty = alias default order
    cache_all         BOOLEAN     NOT NULL DEFAULT FALSE, -- opt in to caching non-deterministic (temperature != 0) requests
    disabled          BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
