-- Written only by fl-ledger, derived entirely from the llm.usage topic.
-- No foreign key to apps: an event for a since-deleted app must not wedge the consumer.
CREATE TABLE usage_events (
    request_id        UUID        PRIMARY KEY,   -- the dedupe key: a redelivered event is a no-op
    ts                TIMESTAMPTZ NOT NULL,
    app_id            BIGINT      NOT NULL,
    provider          TEXT        NOT NULL,      -- '' when no provider answered
    model             TEXT        NOT NULL,
    prompt_tokens     INT         NOT NULL,
    completion_tokens INT         NOT NULL,
    tokens_estimated  BOOLEAN     NOT NULL,
    latency_ms        INT         NOT NULL,
    status            INT         NOT NULL,
    cache_hit         BOOLEAN     NOT NULL
);
CREATE INDEX usage_events_app_ts ON usage_events (app_id, ts);

-- Rollup the panel reads. requests counts every event (cache hits included, so
-- cache hit rate = cache_hits / requests); tokens = prompt + completion.
CREATE TABLE usage_daily (
    day        DATE   NOT NULL,                  -- UTC day of the event
    app_id     BIGINT NOT NULL,
    provider   TEXT   NOT NULL,
    requests   BIGINT NOT NULL DEFAULT 0,
    tokens     BIGINT NOT NULL DEFAULT 0,
    cache_hits BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (day, app_id, provider)
);
