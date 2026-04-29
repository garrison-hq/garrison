-- M5.2 — chat dashboard surface (read-only).
--
-- Three changes to the M5.1 chat schema:
--
--   1. chat_sessions.is_archived BOOLEAN NOT NULL DEFAULT false
--      — operator-driven archive flag for thread housekeeping.
--      Backfills existing rows to false. Does NOT extend the
--      M5.1 status enum (FR-233).
--
--   2. chat_messages.session_id FK recreated with ON DELETE CASCADE
--      — operator-driven thread deletion (deleteChatSession server
--      action) cascades the transcript. Original M5.1 FK had no
--      cascade clause; recreate is online for typical row counts
--      and runs inside the migration's wrapping transaction.
--
--   3. GRANT DELETE ON chat_sessions TO garrison_dashboard_app
--      — required for the deleteChatSession server action. No
--      grant on chat_messages — the cascade does the work, and
--      keeping chat_messages INSERT/SELECT-only on the dashboard
--      role mirrors M4's "narrow grants" stance.
--
-- vault_access_log is intentionally NOT touched (FR-236):
--   - rows referencing a deleted chat_session_id via JSONB
--     metadata survive as forensic trail
--   - garrison_dashboard_app keeps M4's INSERT-only grant on
--     vault_access_log; no DELETE grant added

-- +goose Up

ALTER TABLE chat_sessions
    ADD COLUMN is_archived BOOLEAN NOT NULL DEFAULT false;

CREATE INDEX idx_chat_sessions_user_active_unarchived
    ON chat_sessions (started_by_user_id, started_at DESC)
    WHERE is_archived = false;

ALTER TABLE chat_messages
    DROP CONSTRAINT chat_messages_session_id_fkey;
ALTER TABLE chat_messages
    ADD CONSTRAINT chat_messages_session_id_fkey
    FOREIGN KEY (session_id) REFERENCES chat_sessions(id) ON DELETE CASCADE;

GRANT DELETE ON chat_sessions TO garrison_dashboard_app;

-- +goose Down

REVOKE DELETE ON chat_sessions FROM garrison_dashboard_app;

ALTER TABLE chat_messages
    DROP CONSTRAINT chat_messages_session_id_fkey;
ALTER TABLE chat_messages
    ADD CONSTRAINT chat_messages_session_id_fkey
    FOREIGN KEY (session_id) REFERENCES chat_sessions(id);

DROP INDEX idx_chat_sessions_user_active_unarchived;

ALTER TABLE chat_sessions DROP COLUMN is_archived;
