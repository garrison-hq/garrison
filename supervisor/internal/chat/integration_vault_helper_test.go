//go:build integration

package chat

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

// chatVaultStack stands up a real testcontainer Infisical (M2.3
// vault.testutil) wired to a chat-shaped Machine Identity, optionally
// seeded with the operator OAuth token at the M5.1 path convention
// /<customer_id>/operator/CLAUDE_CODE_OAUTH_TOKEN.
//
// seed=true: token present → real vault.Client returns it on Fetch
// seed=false: token absent → real Fetch returns vault.ErrVaultSecretNotFound
//
// Returns (chat-ready vault.Fetcher, customer_id pgtype.UUID).
// The harness's own Cleanup is registered via t.Cleanup automatically
// by StartInfisical, so caller doesn't need extra teardown handling.
func chatVaultStack(t *testing.T, seed bool) (vault.Fetcher, pgtype.UUID) {
	t.Helper()
	client, customerID, _ := chatVaultStackWithHarness(t, seed)
	return client, customerID
}

// chatVaultStackWithHarness is the lower-level helper exposing the
// underlying *vault.InfisicalTestHarness so tests that need to
// manipulate the harness directly (StopInfisical, etc.) can do so.
func chatVaultStackWithHarness(t *testing.T, seed bool) (vault.Fetcher, pgtype.UUID, *vault.InfisicalTestHarness) {
	t.Helper()
	harness := vault.StartInfisical(t)

	mlClientID, mlClientSecret, err := harness.CreateMachineIdentity("garrison-chat-test-ml-" + strings.ReplaceAll(t.Name(), "/", "-"))
	if err != nil {
		t.Fatalf("CreateMachineIdentity: %v", err)
	}

	customerID := newUUID(t)
	customerStr := uuidString(customerID)
	folderPath := "/" + customerStr + "/operator"

	if seed {
		if err := harness.SeedSecret(folderPath, "CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-test-token"); err != nil {
			t.Fatalf("SeedSecret: %v", err)
		}
	}

	client, err := vault.NewClient(context.Background(), vault.ClientConfig{
		SiteURL:      harness.URL(),
		ClientID:     mlClientID,
		ClientSecret: mlClientSecret,
		CustomerID:   customerStr,
		ProjectID:    harness.ProjectID(),
		Environment:  harness.Environment(),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("vault.NewClient: %v", err)
	}

	return client, customerID, harness
}

// chatVaultStackShortLived stands up real Infisical with a Machine
// Identity whose access token has a 1-second TTL + numUsesLimit=1 so
// re-auth fails after the first fetch — proves the chat path
// surfaces vault.ErrVaultAuthExpired as ErrorTokenExpired without a
// synthetic mock. Seeds the token by default since this test is
// about expiry, not absence.
func chatVaultStackShortLived(t *testing.T) (vault.Fetcher, pgtype.UUID, *vault.InfisicalTestHarness) {
	t.Helper()
	harness := vault.StartInfisical(t)

	mlClientID, mlClientSecret, err := harness.CreateShortLivedMachineIdentity("garrison-chat-shortlived-" + strings.ReplaceAll(t.Name(), "/", "-"))
	if err != nil {
		t.Fatalf("CreateShortLivedMachineIdentity: %v", err)
	}

	customerID := newUUID(t)
	customerStr := uuidString(customerID)
	folderPath := "/" + customerStr + "/operator"
	if err := harness.SeedSecret(folderPath, "CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-test-token"); err != nil {
		t.Fatalf("SeedSecret: %v", err)
	}

	client, err := vault.NewClient(context.Background(), vault.ClientConfig{
		SiteURL:      harness.URL(),
		ClientID:     mlClientID,
		ClientSecret: mlClientSecret,
		CustomerID:   customerStr,
		ProjectID:    harness.ProjectID(),
		Environment:  harness.Environment(),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("vault.NewClient: %v", err)
	}

	return client, customerID, harness
}
