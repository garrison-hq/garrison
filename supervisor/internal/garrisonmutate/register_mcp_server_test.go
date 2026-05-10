//go:build integration

package garrisonmutate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRegisterRequiresCustomerPrefix(t *testing.T) {
	fx := setupIntegration(t)
	deps := Deps{Pool: fx.pool}
	args := `{"customer_slug":"garrison","name":"linear","transport":"http","url":"http://x"}`
	r, err := realRegisterMcpServerHandler(context.Background(), deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Errorf("expected rejection; got %+v", r)
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want validation_failed", r.ErrorKind)
	}
	if !strings.Contains(r.Message, "customer-prefix") {
		t.Errorf("message %q missing customer-prefix surface", r.Message)
	}
}

func TestRegisterWritesPendingRow(t *testing.T) {
	fx := setupIntegration(t)
	// Make sure the 'garrison' customer_slug exists.
	if _, err := fx.pool.Exec(context.Background(),
		`UPDATE companies SET customer_slug = 'garrison' WHERE id IN (SELECT id FROM companies LIMIT 1)`); err != nil {
		t.Fatalf("set customer slug: %v", err)
	}
	deps := Deps{Pool: fx.pool}
	args := `{"customer_slug":"garrison","name":"garrison.linear","transport":"http","url":"http://linear-mcp"}`
	r, err := realRegisterMcpServerHandler(context.Background(), deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !r.Success {
		t.Fatalf("expected success; got %+v", r)
	}
	var status string
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT status FROM mcp_servers WHERE id::text = $1`, r.AffectedResourceID).Scan(&status); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if status != "pending" {
		t.Errorf("status = %s; want pending", status)
	}
}

func TestRegisterDoesNotWriteAuditAtServerActionTime(t *testing.T) {
	fx := setupIntegration(t)
	if _, err := fx.pool.Exec(context.Background(),
		`UPDATE companies SET customer_slug = 'garrison' WHERE id IN (SELECT id FROM companies LIMIT 1)`); err != nil {
		t.Fatalf("set customer slug: %v", err)
	}
	deps := Deps{Pool: fx.pool}
	args := `{"customer_slug":"garrison","name":"garrison.linear","transport":"http","url":"http://x"}`
	if _, err := realRegisterMcpServerHandler(context.Background(), deps, json.RawMessage(args)); err != nil {
		t.Fatalf("handler error: %v", err)
	}
	// FR-306 single-row invariant — the worker writes the audit row,
	// NOT the Server Action. Count must be 0 here.
	var count int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat_mutation_audit WHERE verb = 'register_mcp_server'`).Scan(&count); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if count != 0 {
		t.Errorf("audit rows at Server-Action time = %d; want 0 (worker writes the row)", count)
	}
}

func TestRegisterDuplicateNameRejects(t *testing.T) {
	fx := setupIntegration(t)
	if _, err := fx.pool.Exec(context.Background(),
		`UPDATE companies SET customer_slug = 'garrison' WHERE id IN (SELECT id FROM companies LIMIT 1)`); err != nil {
		t.Fatalf("set customer slug: %v", err)
	}
	deps := Deps{Pool: fx.pool}
	args := `{"customer_slug":"garrison","name":"garrison.linear","transport":"http","url":"http://x"}`
	if _, err := realRegisterMcpServerHandler(context.Background(), deps, json.RawMessage(args)); err != nil {
		t.Fatalf("first call: %v", err)
	}
	r, err := realRegisterMcpServerHandler(context.Background(), deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if r.Success {
		t.Errorf("expected duplicate rejection; got %+v", r)
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want validation_failed", r.ErrorKind)
	}
	if !strings.Contains(r.Message, "already registered") {
		t.Errorf("message %q missing duplicate surface", r.Message)
	}
}

func TestRegisterRejectsBadTransport(t *testing.T) {
	deps := Deps{}
	args := `{"customer_slug":"garrison","name":"garrison.x","transport":"grpc"}`
	r, err := realRegisterMcpServerHandler(context.Background(), deps, json.RawMessage(args))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Errorf("expected rejection; got %+v", r)
	}
	if !strings.Contains(r.Message, "transport") {
		t.Errorf("message %q missing transport surface", r.Message)
	}
}
