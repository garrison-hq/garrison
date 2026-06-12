package ingress

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
)

// webhookSecretVaultPath is the Infisical path for the GitHub webhook HMAC
// secret (FR-302, plan decision 12). The supervisor fetches it at boot;
// rotation requires a supervisor restart (noted in ops-checklist M10 section).
const webhookSecretVaultPath = "ingress/github/webhook_secret"

// webhookSecretEnvKey is the in-memory env-var key used when calling
// vault.Fetcher.Fetch. The value never reaches the OS environment — it is
// used only as a map key to retrieve the SecretValue from the Fetch result.
const webhookSecretEnvKey = "GARRISON_GITHUB_WEBHOOK_SECRET"

// Server is the HTTP server lifecycle wrapper for the M10 ingress connector
// framework. It mirrors dashboardapi.Server and health.Server so
// cmd/supervisor can run it side-by-side in the existing errgroup with no
// structural change (plan.md decision 6, SR7).
//
// Construct via NewServer; the zero value is not valid.
type Server struct {
	httpServer    *http.Server
	shutdownGrace time.Duration
	logger        *slog.Logger
}

// NewServer builds the mux, registers POST /webhook/github, fetches the vault
// secret at construction (fail-closed per FR-302), and returns a wired server.
//
// Construction is the only fail-closed point: if the GitHub connector is
// enabled and the vault is unreachable or the secret is absent, NewServer
// returns an error so cmd/supervisor can surface ExitFailure before any
// goroutines are launched.
//
// When cfg.IngressGitHubEnabled is false, callers should not call NewServer
// (buildIngressServer in main.go gates on this flag).
func NewServer(ctx context.Context, cfg *config.Config, deps Deps, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Fail-closed: a nil vault client means we cannot fetch the webhook
	// secret, so we refuse to start (FR-302, plan decision 12).
	if deps.VaultClient == nil {
		return nil, fmt.Errorf("ingress: vault client is required when GitHub ingress is enabled (FR-302)")
	}

	// Fetch the webhook HMAC secret from vault. Fail-closed on any error:
	// the supervisor must not start signature-blind (FR-302).
	secrets, err := deps.VaultClient.Fetch(ctx, []vault.GrantRow{{
		EnvVarName: webhookSecretEnvKey,
		SecretPath: webhookSecretVaultPath,
		CustomerID: deps.CustomerID,
	}})
	if err != nil {
		return nil, fmt.Errorf("ingress: vault fetch for GitHub webhook secret failed (fail-closed, FR-302): %w", err)
	}
	secretSV, ok := secrets[webhookSecretEnvKey]
	if !ok {
		return nil, fmt.Errorf("ingress: vault returned no value for GitHub webhook secret at path %q (fail-closed, FR-302)", webhookSecretVaultPath)
	}
	// UnsafeBytes is one of the two documented production call sites for this
	// method (the other is spawn env injection — AGENTS.md). The raw bytes
	// are held only in the connector config struct, never logged
	// (AGENTS.md §"What agents should not do" — tools/vaultlog enforces this
	// at build time).
	rawSecret := make([]byte, len(secretSV.UnsafeBytes()))
	copy(rawSecret, secretSV.UnsafeBytes())
	secretSV.Zero()

	// Build the per-connector rate cap with production wall-clock (nil → time.Now).
	rateCap := NewRateCap(nil)
	rateCap.AddConnector(cfg.IngressGitHubConnectorID, cfg.IngressGitHubRatePerMin, cfg.IngressGitHubBurst)

	// Build the GitHub connector from deploy-time config + vault-fetched secret.
	ghConn := NewGitHubConnector(GitHubConfig{
		ConnectorID: cfg.IngressGitHubConnectorID,
		Secret:      rawSecret,
		Routes: map[string]Route{
			"issues": {
				DepartmentSlug:     cfg.IngressGitHubDepartment,
				ObjectiveTemplate:  "Triage GitHub issue: {{title}} ({{url}})",
				AcceptanceTemplate: "{{body}}",
			},
			"pull_request": {
				DepartmentSlug:     cfg.IngressGitHubDepartment,
				ObjectiveTemplate:  "Review GitHub pull request: {{title}} ({{url}})",
				AcceptanceTemplate: "{{body}}",
			},
		},
		RatePerMin: cfg.IngressGitHubRatePerMin,
		Burst:      cfg.IngressGitHubBurst,
	})

	handlerDeps := HandlerDeps{
		Pool:             deps.Pool,
		Queries:          deps.Queries,
		Connector:        ghConn,
		Secret:           rawSecret,
		RejectionCounter: deps.RejectionCounter,
		Logger:           logger,
		RateCap:          rateCap,
		CompanyID:        deps.CustomerID,
		RatePerMin:       cfg.IngressGitHubRatePerMin,
		Burst:            cfg.IngressGitHubBurst,
	}

	mux := http.NewServeMux()
	mux.Handle("POST /webhook/github", newWebhookHandler(handlerDeps))

	return &Server{
		httpServer: &http.Server{
			Addr:              fmt.Sprintf("0.0.0.0:%d", cfg.IngressPort),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		shutdownGrace: cfg.ShutdownGrace,
		logger:        logger,
	}, nil
}

// Serve runs the HTTP server until ctx is cancelled, then issues
// http.Server.Shutdown with the configured grace window. Mirrors
// dashboardapi.Server.Serve and health.Server.Serve byte-for-byte
// (concurrency rule 6: shutdown context derived via context.WithoutCancel +
// grace period).
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownGrace)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("ingress: Shutdown: %w", err)
		}
		return nil
	}
}
