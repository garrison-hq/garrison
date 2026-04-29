-- M5.1 chat backend queries.
-- Read/write patterns for chat_sessions + chat_messages used by the
-- supervisor's internal/chat/ package.

-- name: CreateChatSession :one
-- Operator-initiated session creation. The dashboard server action
-- INSERTs alongside the first chat_messages row in one tx; supervisor
-- never lazy-creates sessions per FR-080.
INSERT INTO chat_sessions (id, started_by_user_id, status, total_cost_usd, started_at)
VALUES (gen_random_uuid(), @started_by_user_id, 'active', 0, NOW())
RETURNING *;

-- name: GetChatSession :one
SELECT * FROM chat_sessions WHERE id = @id;

-- name: ListSessionsForUser :many
SELECT * FROM chat_sessions
 WHERE started_by_user_id = @user_id
 ORDER BY started_at DESC
 LIMIT @lim;

-- name: UpdateChatSessionStatus :exec
UPDATE chat_sessions
   SET status = @status, ended_at = COALESCE(ended_at, NOW())
 WHERE id = @id AND status = 'active';

-- name: RollUpSessionCost :exec
-- Increments total_cost_usd by the assistant turn's cost. Called inside
-- the same tx as the assistant chat_messages terminal commit (FR-060).
UPDATE chat_sessions
   SET total_cost_usd = total_cost_usd + @delta_usd
 WHERE id = @id;

-- name: MarkActiveSessionsIdle :many
-- Idle-sweep query (FR-081). Marks sessions whose newest chat_messages
-- row is older than the supplied cutoff as 'ended' and returns the
-- updated session ids so the caller can pg_notify per session.
UPDATE chat_sessions
   SET status = 'ended', ended_at = NOW()
 WHERE status = 'active'
   AND id NOT IN (
       SELECT DISTINCT session_id
         FROM chat_messages
        WHERE created_at > @idle_cutoff
   )
RETURNING id;

-- name: InsertOperatorMessage :one
-- Dashboard side INSERTs operator messages directly via Drizzle; the
-- supervisor doesn't call this in production (operator messages flow
-- through the dashboard server action). Provided for testcontainer
-- tests that simulate the dashboard side.
-- turn_index is computed inside the same statement: COALESCE(MAX,-1)+1.
INSERT INTO chat_messages (
    id, session_id, turn_index, role, status, content, created_at
)
VALUES (
    gen_random_uuid(),
    @session_id,
    COALESCE(
        (SELECT MAX(turn_index) FROM chat_messages WHERE session_id = @session_id),
        -1
    ) + 1,
    'operator',
    'completed',
    @content,
    NOW()
)
RETURNING *;

-- name: InsertAssistantPending :one
-- Supervisor-side INSERT of the assistant row at the start of a turn.
-- turn_index = the operator turn's turn_index + 1 (caller computes).
INSERT INTO chat_messages (
    id, session_id, turn_index, role, status, created_at
)
VALUES (
    gen_random_uuid(),
    @session_id,
    @turn_index,
    'assistant',
    'pending',
    NOW()
)
RETURNING *;

-- name: TransitionMessageToStreaming :exec
-- ChatPolicy.OnInit calls this once MCP health is verified.
UPDATE chat_messages
   SET status = 'streaming'
 WHERE id = @id AND status = 'pending';

-- name: CommitAssistantTerminal :exec
-- Single UPDATE that commits the assistant turn at terminal time:
-- content + cost + tokens + raw_event_envelope + status + terminated_at.
-- Called inside the same tx as RollUpSessionCost + the audit pg_notify.
UPDATE chat_messages
   SET status              = @status,
       content             = @content,
       tokens_input        = @tokens_input,
       tokens_output       = @tokens_output,
       cost_usd            = @cost_usd,
       error_kind          = @error_kind,
       raw_event_envelope  = @raw_event_envelope,
       terminated_at       = NOW()
 WHERE id = @id;

-- name: TerminalWriteWithError :exec
-- Used for non-spawn error paths (vault failure, cost-cap reached,
-- session ended): writes status + error_kind + terminated_at without
-- touching content / cost / envelope. Caller is the chat.Worker before
-- a docker container is even started.
UPDATE chat_messages
   SET status = @status, error_kind = @error_kind, terminated_at = NOW()
 WHERE id = @id;

-- name: MarkPendingMessagesAborted :many
-- Restart-sweep query (FR-083). Marks chat_messages rows in pending or
-- streaming state older than the supplied cutoff as aborted with the
-- supplied error_kind, and returns the affected ids so the caller can
-- terminal-write log lines + roll up the session status.
UPDATE chat_messages
   SET status        = 'aborted',
       error_kind    = @error_kind,
       terminated_at = NOW()
 WHERE status IN ('pending', 'streaming')
   AND created_at < @cutoff
RETURNING id, session_id;

-- name: AbortSessionsWithAbortedMessages :exec
-- Companion to MarkPendingMessagesAborted: rolls the parent session to
-- aborted state. Run in the same tx so the sweep is atomic.
UPDATE chat_sessions
   SET status = 'aborted', ended_at = NOW()
 WHERE status = 'active'
   AND id IN (
       SELECT DISTINCT session_id
         FROM chat_messages
        WHERE status = 'aborted' AND error_kind = @error_kind
   );

-- name: GetSessionTranscript :many
-- Replay-side query: returns operator rows + completed-assistant rows
-- in turn_index order. Failed/aborted assistant rows are excluded
-- (clarify Q4 + AssembleTranscript golden fixtures).
SELECT id, session_id, turn_index, role, status, content, created_at
  FROM chat_messages
 WHERE session_id = @session_id
   AND (role = 'operator'
        OR (role = 'assistant' AND status = 'completed'))
 ORDER BY turn_index ASC;

-- name: GetChatMessageByID :one
-- Listener fast-path lookup: given the message_id from a
-- chat.message.sent notify, find the row + its parent session_id
-- so the worker can scope its serial mutex.
SELECT * FROM chat_messages WHERE id = @id;

-- name: GetMaxTurnIndex :one
-- Used by the supervisor when computing the assistant turn_index for
-- a fresh INSERT. Returns -1 (via COALESCE) when the session has no
-- messages yet.
SELECT COALESCE(MAX(turn_index), -1)::INTEGER AS max_turn_index
  FROM chat_messages
 WHERE session_id = @session_id;

-- name: CountInflightMessages :one
SELECT COUNT(*)::INTEGER AS count
  FROM chat_messages
 WHERE session_id = @session_id
   AND status IN ('pending', 'streaming');

-- name: FindOrphanedOperatorSessions :many
-- M5.2 — orphan-row sweep query (FR-290).
-- Detect chat_sessions with status='active' whose newest chat_messages
-- row is role='operator', older than the supplied cutoff, with no
-- assistant pair at turn_index+1. The boot-time sweep in
-- internal/chat/listener.go uses this to detect sessions where the
-- supervisor crashed between the operator INSERT and the assistant
-- pending row creation; the M5.1 pending-message sweep cannot catch
-- these because there's no pending/streaming row to filter on.
SELECT cm.session_id AS session_id,
       cm.id AS operator_message_id,
       cm.turn_index AS orphan_turn_index
  FROM chat_messages cm
  JOIN chat_sessions cs ON cs.id = cm.session_id
 WHERE cs.status = 'active'
   AND cm.role = 'operator'
   AND cm.created_at < @cutoff
   AND cm.turn_index = (
       SELECT MAX(turn_index)
         FROM chat_messages cmx
        WHERE cmx.session_id = cm.session_id
   )
   AND NOT EXISTS (
       SELECT 1 FROM chat_messages cm2
        WHERE cm2.session_id = cm.session_id
          AND cm2.turn_index = cm.turn_index + 1
   );

-- name: InsertSyntheticAssistantAborted :exec
-- M5.2 — synthesise an aborted assistant row for an orphaned operator
-- turn. Used only by the M5.2 orphan-sweep extension at supervisor boot.
-- The synthetic row is aborted-from-inception; it never transitions
-- through pending/streaming.
INSERT INTO chat_messages (
    id, session_id, turn_index, role, status, content, error_kind, terminated_at
)
VALUES (
    gen_random_uuid(),
    @session_id,
    @turn_index,
    'assistant',
    'aborted',
    @content,
    @error_kind,
    NOW()
);

-- name: MarkSessionAborted :exec
-- M5.2 — marks a single chat_sessions row aborted. Used by the orphan
-- sweep alongside InsertSyntheticAssistantAborted. The existing M5.1
-- AbortSessionsWithAbortedMessages query takes a different shape (it
-- joins via error_kind across all aborted messages); this is the
-- single-row-by-id companion.
UPDATE chat_sessions
   SET status = 'aborted', ended_at = NOW()
 WHERE id = @id
   AND status = 'active';
