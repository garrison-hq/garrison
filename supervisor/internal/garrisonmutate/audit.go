package garrisonmutate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// AuditTx is the transaction interface garrison-mutate verbs share with
// the store package. Mirrors the seam used by internal/finalize for
// per-tx queries. Concretely satisfied by *store.Queries.
type AuditTx interface {
	InsertChatMutationAudit(ctx context.Context, params store.InsertChatMutationAuditParams) (store.InsertChatMutationAuditRow, error)
}

// AuditWriteParams groups the fields callers populate before calling
// WriteAudit, keeping the signature within Sonar's S107 limit.
type AuditWriteParams struct {
	ChatSessionID        pgtype.UUID
	ChatMessageID        pgtype.UUID
	Verb                 string
	Args                 any // anything json.Marshalable; full args incl. operator-typed text per FR-473
	Outcome              string
	ReversibilityClass   int16
	AffectedResourceID   *string
	AffectedResourceType *string
}

// WriteAudit inserts one chat_mutation_audit row inside the supplied
// transaction. Per Rule 3 (chat-threat-model.md), every successful
// verb commits its audit row in the same tx as the data write; failure
// audit rows are written by FailureAudit (separate-tx, best-effort)
// because the data-side ROLLBACK invalidates a same-tx audit INSERT.
//
// args_jsonb is marshaled here so the verb implementations don't need
// to round-trip JSON themselves; if marshal fails (which should never
// happen for sane Go inputs), the audit row records a placeholder JSON
// object describing the marshal failure rather than dropping the row.
func WriteAudit(ctx context.Context, tx AuditTx, p AuditWriteParams) (pgtype.UUID, error) {
	argsJSON, err := json.Marshal(p.Args)
	if err != nil {
		argsJSON = []byte(fmt.Sprintf(`{"_audit_marshal_error":%q}`, err.Error()))
	}
	row, err := tx.InsertChatMutationAudit(ctx, store.InsertChatMutationAuditParams{
		ChatSessionID:        p.ChatSessionID,
		ChatMessageID:        p.ChatMessageID,
		Verb:                 p.Verb,
		ArgsJsonb:            argsJSON,
		Outcome:              p.Outcome,
		ReversibilityClass:   p.ReversibilityClass,
		AffectedResourceID:   p.AffectedResourceID,
		AffectedResourceType: p.AffectedResourceType,
	})
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("garrisonmutate: insert audit row: %w", err)
	}
	return row.ID, nil
}
