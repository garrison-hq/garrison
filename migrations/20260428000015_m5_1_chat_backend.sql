-- M5.1 — CEO chat backend (read-only).
--
-- Adds the two supervisor-owned tables that back the chat runtime:
--
--   chat_sessions  — one row per CEO conversation; lifecycle status
--                    (active|ended|aborted), per-session running cost
--                    rolled up from message-level cost_usd values.
--
--   chat_messages  — one row per turn (operator and assistant alike).
--                    UNIQUE(session_id, turn_index) keeps the transcript
--                    monotonically ordered. status enum tracks the
--                    assistant lifecycle pending → streaming →
--                    completed|failed|aborted (operator rows always
--                    INSERT at status='completed' per clarify Q1).
--
-- Cross-domain note: started_by_user_id is intentionally NOT a Postgres
-- FK to users(id). users is dashboard-domain (Drizzle-managed); this
-- table is supervisor-domain (goose-managed). The M4 retro established
-- the precedent of avoiding supervisor → dashboard cross-domain FKs
-- (vault_access_log.metadata.actor_user_id JSONB took the same call).
-- Integrity is preserved at the application layer: the dashboard's
-- startChatSession server action populates this value from an
-- authenticated better-auth session.user.id, which is already validated
-- against users at session-cookie verify time.
--
-- Dashboard role grants (FR-042): garrison_dashboard_app needs
--   - INSERT, SELECT, UPDATE on chat_sessions (server action creates
--     session row + first message in one tx; UPDATE for the rare
--     dashboard-side status transitions in M5.2).
--   - INSERT, SELECT on chat_messages (operator messages INSERTed by
--     the dashboard; assistant messages only INSERTed by the
--     supervisor's primary role; no UPDATE from dashboard).
--
-- No DB triggers in this migration. Per the M5.1 plan §3.4, all
-- pg_notify emissions for chat audit channels are supervisor-driven
-- inside the same transaction as the row write (mirrors the M2.1
-- commit pattern). Keeping the channel-name vocabulary supervisor-owned
-- avoids hidden trigger plumbing that future maintainers would have to
-- discover.

-- +goose Up

CREATE TABLE chat_sessions (
    id                    UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    started_by_user_id    UUID         NOT NULL,
    started_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    ended_at              TIMESTAMPTZ  NULL,
    status                TEXT         NOT NULL DEFAULT 'active'
                                       CHECK (status IN ('active','ended','aborted')),
    total_cost_usd        NUMERIC(20,10) NOT NULL DEFAULT 0,
    claude_session_label  TEXT         NULL
);

CREATE INDEX idx_chat_sessions_user_started
    ON chat_sessions (started_by_user_id, started_at DESC);

CREATE INDEX idx_chat_sessions_active
    ON chat_sessions (status)
    WHERE status = 'active';

CREATE TABLE chat_messages (
    id                    UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id            UUID         NOT NULL REFERENCES chat_sessions(id),
    turn_index            INTEGER      NOT NULL,
    role                  TEXT         NOT NULL
                                       CHECK (role IN ('operator','assistant')),
    status                TEXT         NOT NULL
                                       CHECK (status IN ('pending','streaming','completed','failed','aborted')),
    content               TEXT         NULL,
    tokens_input          INTEGER      NULL,
    tokens_output         INTEGER      NULL,
    cost_usd              NUMERIC(20,10) NULL,
    error_kind            TEXT         NULL,
    raw_event_envelope    JSONB        NULL,
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
    terminated_at         TIMESTAMPTZ  NULL,
    UNIQUE (session_id, turn_index)
);

CREATE INDEX idx_chat_messages_inflight
    ON chat_messages (session_id, status)
    WHERE status IN ('pending','streaming');

GRANT INSERT, SELECT, UPDATE ON chat_sessions TO garrison_dashboard_app;
GRANT INSERT, SELECT          ON chat_messages TO garrison_dashboard_app;

-- +goose Down

REVOKE INSERT, SELECT          ON chat_messages FROM garrison_dashboard_app;
REVOKE INSERT, SELECT, UPDATE  ON chat_sessions FROM garrison_dashboard_app;

DROP TABLE chat_messages;
DROP TABLE chat_sessions;
