package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	infisical "github.com/infisical/go-sdk"
	infisicalErrors "github.com/infisical/go-sdk/packages/errors"
	"github.com/jackc/pgx/v5/pgtype"
)

// GrantRow is a single row from the ListGrantsForRole query.
// Matches the sqlc-generated ListGrantsForRoleRow shape so callers
// can pass the query results directly.
type GrantRow struct {
	EnvVarName string
	SecretPath string
	CustomerID pgtype.UUID
}

// ClientConfig holds the configuration for vault.Client. All fields are
// required unless marked optional.
type ClientConfig struct {
	SiteURL      string // Infisical server URL, e.g. "http://garrison-infisical:8080"
	ClientID     string // Machine Identity client_id (GARRISON_INFISICAL_CLIENT_ID)
	ClientSecret string // Machine Identity client_secret (GARRISON_INFISICAL_CLIENT_SECRET)
	CustomerID   string // Garrison customer UUID string (from companies.id)
	ProjectID    string // Infisical project ID (GARRISON_INFISICAL_PROJECT_ID)
	Environment  string // Infisical environment slug, e.g. "prod" (GARRISON_INFISICAL_ENVIRONMENT)
	Logger       *slog.Logger
}

// Fetcher is the interface the spawn path uses. *Client implements it.
// Tests inject a mock Fetcher.
type Fetcher interface {
	Fetch(ctx context.Context, req []GrantRow) (map[string]SecretValue, error)
}

// sdkClient is the interface we use from the Infisical SDK so unit tests
// can inject a mock without needing a live Infisical server.
type sdkClient interface {
	Auth() infisical.AuthInterface
	Secrets() infisical.SecretsInterface
}

// Client wraps the Infisical Go SDK and provides Garrison-specific auth,
// retry, and error-classification semantics. Safe for concurrent use.
type Client struct {
	sdk          sdkClient
	siteURL      string
	clientID     string
	clientSecret string
	customerID   string
	projectID    string
	environment  string
	logger       *slog.Logger

	mu        sync.Mutex
	authToken string
	authAt    time.Time
}

// NewClient authenticates immediately and returns a ready-to-use Client.
// Returns an error if any required config field is missing or if the
// initial authentication fails. Callers should surface the error at
// supervisor startup (fail-fast per D4.1).
func NewClient(ctx context.Context, cfg ClientConfig) (*Client, error) {
	if cfg.SiteURL == "" {
		return nil, errors.New("vault: SiteURL required")
	}
	if cfg.ClientID == "" {
		return nil, errors.New("vault: ClientID required")
	}
	if cfg.ClientSecret == "" {
		return nil, errors.New("vault: ClientSecret required")
	}
	if cfg.CustomerID == "" {
		return nil, errors.New("vault: CustomerID required")
	}
	if cfg.ProjectID == "" {
		return nil, errors.New("vault: ProjectID required")
	}
	if cfg.Environment == "" {
		return nil, errors.New("vault: Environment required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	sdk := infisical.NewInfisicalClient(ctx, infisical.Config{
		SiteUrl:          cfg.SiteURL,
		AutoTokenRefresh: false,
		SilentMode:       true,
	})

	c := &Client{
		sdk:          sdk,
		siteURL:      cfg.SiteURL,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		customerID:   cfg.CustomerID,
		projectID:    cfg.ProjectID,
		environment:  cfg.Environment,
		logger:       logger,
	}

	if err := c.reauthenticate(ctx); err != nil {
		return nil, fmt.Errorf("vault: initial authentication failed: %w", err)
	}
	return c, nil
}

// newClientWithSDK is the test constructor that accepts an injected sdkClient.
func newClientWithSDK(sdk sdkClient, cfg ClientConfig) (*Client, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("vault: ClientID and ClientSecret required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		sdk:          sdk,
		siteURL:      cfg.SiteURL,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		customerID:   cfg.CustomerID,
		projectID:    cfg.ProjectID,
		environment:  cfg.Environment,
		logger:       logger,
	}, nil
}

// Fetch retrieves the secret value for each GrantRow. Returns a map from
// env_var_name to SecretValue. On HTTP 401, re-authenticates exactly once
// and retries only the failed fetch; if re-auth also fails, returns
// ErrVaultAuthExpired. On any other error, classifies via the six sentinels.
//
// The returned SecretValues are owned by the caller; call Zero() on each
// after the values have been injected into the subprocess environment.
func (c *Client) Fetch(ctx context.Context, req []GrantRow) (map[string]SecretValue, error) {
	if len(req) == 0 {
		return map[string]SecretValue{}, nil
	}
	result := make(map[string]SecretValue, len(req))
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, grant := range req {
		val, err := c.fetchOneUnderLockWithRetry(ctx, grant)
		if err != nil {
			return nil, err
		}
		result[grant.EnvVarName] = val
	}
	return result, nil
}

// fetchOneUnderLockWithRetry fetches one grant, re-authenticating once on 401.
// Must be called with c.mu held.
func (c *Client) fetchOneUnderLockWithRetry(ctx context.Context, grant GrantRow) (SecretValue, error) {
	val, err := c.fetchOneUnderLock(ctx, grant)
	if err == nil {
		return val, nil
	}
	var apiErr *infisicalErrors.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		return SecretValue{}, classifySDKError(err)
	}
	if reauthErr := c.reauthenticateUnderLock(ctx); reauthErr != nil {
		return SecretValue{}, ErrVaultAuthExpired
	}
	val, err = c.fetchOneUnderLock(ctx, grant)
	if err == nil {
		return val, nil
	}
	var retryErr *infisicalErrors.APIError
	if errors.As(err, &retryErr) && retryErr.StatusCode == http.StatusUnauthorized {
		return SecretValue{}, ErrVaultAuthExpired
	}
	return SecretValue{}, classifySDKError(err)
}

// fetchOneUnderLock fetches a single secret. Must be called with c.mu held.
func (c *Client) fetchOneUnderLock(ctx context.Context, grant GrantRow) (SecretValue, error) {
	folderPath, secretKey := splitSecretPath(grant.SecretPath)

	secret, err := c.sdk.Secrets().Retrieve(infisical.RetrieveSecretOptions{
		SecretKey:   secretKey,
		Environment: c.environment,
		ProjectID:   c.projectID,
		SecretPath:  folderPath,
	})
	if err != nil {
		return SecretValue{}, err
	}
	return New([]byte(secret.SecretValue)), nil
}

// reauthenticate obtains a fresh token. Safe for external callers; acquires
// mu. reauthenticateUnderLock is the mu-held variant for internal use.
func (c *Client) reauthenticate(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reauthenticateUnderLock(ctx)
}

func (c *Client) reauthenticateUnderLock(ctx context.Context) error {
	cred, err := c.sdk.Auth().UniversalAuthLogin(c.clientID, c.clientSecret)
	if err != nil {
		return classifySDKError(err)
	}
	c.authToken = cred.AccessToken
	c.authAt = time.Now()
	c.sdk.Auth().SetAccessToken(cred.AccessToken)
	return nil
}

// splitSecretPath splits a full secret path like "/<customer_id>/operator/key"
// into folder path "/<customer_id>/operator" and key name "key".
// If there is no "/" separator, returns "/" as the folder and the full string
// as the key.
func splitSecretPath(fullPath string) (folderPath, secretKey string) {
	// Normalize: strip trailing slash.
	fullPath = strings.TrimRight(fullPath, "/")
	if fullPath == "" {
		return "/", ""
	}
	folderPath = path.Dir(fullPath)
	secretKey = path.Base(fullPath)
	if folderPath == "" {
		folderPath = "/"
	}
	return folderPath, secretKey
}

// classifySDKError maps Infisical SDK errors to vault sentinels.
func classifySDKError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *infisicalErrors.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized: // 401
			return ErrVaultAuthExpired
		case http.StatusForbidden: // 403
			return ErrVaultPermissionDenied
		case http.StatusTooManyRequests: // 429
			return ErrVaultRateLimited
		case http.StatusNotFound: // 404
			return ErrVaultSecretNotFound
		default:
			return fmt.Errorf("%w: HTTP %d: %s", ErrVaultUnavailable, apiErr.StatusCode, apiErr.Error())
		}
	}
	// RequestError or any other transport-level error → unavailable.
	return fmt.Errorf("%w: %s", ErrVaultUnavailable, err.Error())
}
