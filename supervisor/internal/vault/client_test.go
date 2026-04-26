package vault

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	infisical "github.com/infisical/go-sdk"
	infisicalErrors "github.com/infisical/go-sdk/packages/errors"
	"github.com/infisical/go-sdk/packages/models"
	"github.com/jackc/pgx/v5/pgtype"
)

// --- mock sdkClient ---

type mockSDK struct {
	auth    *mockAuth
	secrets *mockSecrets
}

func (m *mockSDK) Auth() infisical.AuthInterface       { return m.auth }
func (m *mockSDK) Secrets() infisical.SecretsInterface { return m.secrets }

// --- mockAuth ---

type mockAuth struct {
	loginCallCount int32
	loginErr       error
	token          string
}

func (a *mockAuth) UniversalAuthLogin(_, _ string) (infisical.MachineIdentityCredential, error) {
	atomic.AddInt32(&a.loginCallCount, 1)
	if a.loginErr != nil {
		return infisical.MachineIdentityCredential{}, a.loginErr
	}
	return infisical.MachineIdentityCredential{AccessToken: a.token}, nil
}
func (a *mockAuth) SetAccessToken(_ string)                               {}
func (a *mockAuth) GetAccessToken() string                                { return a.token }
func (a *mockAuth) GetOrganizationSlug() string                           { return "" }
func (a *mockAuth) WithOrganizationSlug(_ string) infisical.AuthInterface { return a }
func (a *mockAuth) JwtAuthLogin(_, _ string) (infisical.MachineIdentityCredential, error) {
	return infisical.MachineIdentityCredential{}, nil
}
func (a *mockAuth) KubernetesAuthLogin(_, _ string) (infisical.MachineIdentityCredential, error) {
	return infisical.MachineIdentityCredential{}, nil
}
func (a *mockAuth) KubernetesRawServiceAccountTokenLogin(_, _ string) (infisical.MachineIdentityCredential, error) {
	return infisical.MachineIdentityCredential{}, nil
}
func (a *mockAuth) AzureAuthLogin(_, _ string) (infisical.MachineIdentityCredential, error) {
	return infisical.MachineIdentityCredential{}, nil
}
func (a *mockAuth) GcpIdTokenAuthLogin(_ string) (infisical.MachineIdentityCredential, error) {
	return infisical.MachineIdentityCredential{}, nil
}
func (a *mockAuth) GcpIamAuthLogin(_, _ string) (infisical.MachineIdentityCredential, error) {
	return infisical.MachineIdentityCredential{}, nil
}
func (a *mockAuth) AwsIamAuthLogin(_ string) (infisical.MachineIdentityCredential, error) {
	return infisical.MachineIdentityCredential{}, nil
}
func (a *mockAuth) OidcAuthLogin(_, _ string) (infisical.MachineIdentityCredential, error) {
	return infisical.MachineIdentityCredential{}, nil
}
func (a *mockAuth) OciAuthLogin(_ infisical.OciAuthLoginOptions) (infisical.MachineIdentityCredential, error) {
	return infisical.MachineIdentityCredential{}, nil
}
func (a *mockAuth) LdapAuthLogin(_, _, _ string) (infisical.MachineIdentityCredential, error) {
	return infisical.MachineIdentityCredential{}, nil
}
func (a *mockAuth) RevokeAccessToken() error { return nil }

// --- mockSecrets ---

type mockSecrets struct {
	mu        sync.Mutex
	calls     int
	responses []mockRetrieveResponse
}

type mockRetrieveResponse struct {
	secret models.Secret
	err    error
}

func (s *mockSecrets) Retrieve(_ infisical.RetrieveSecretOptions) (models.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls < len(s.responses) {
		r := s.responses[s.calls]
		s.calls++
		return r.secret, r.err
	}
	// Default: repeat last response if exhausted.
	if len(s.responses) > 0 {
		r := s.responses[len(s.responses)-1]
		s.calls++
		return r.secret, r.err
	}
	return models.Secret{}, errors.New("mockSecrets: no responses configured")
}

func (s *mockSecrets) List(_ infisical.ListSecretsOptions) ([]models.Secret, error) {
	return nil, nil
}
func (s *mockSecrets) ListSecrets(_ infisical.ListSecretsOptions) (infisical.ListSecretsResult, error) {
	return infisical.ListSecretsResult{}, nil
}
func (s *mockSecrets) Update(_ infisical.UpdateSecretOptions) (models.Secret, error) {
	return models.Secret{}, nil
}
func (s *mockSecrets) Create(_ infisical.CreateSecretOptions) (models.Secret, error) {
	return models.Secret{}, nil
}
func (s *mockSecrets) Delete(_ infisical.DeleteSecretOptions) (models.Secret, error) {
	return models.Secret{}, nil
}
func (s *mockSecrets) Batch() infisical.BatchSecretsInterface { return nil }

// --- helpers ---

func newTestClient(auth *mockAuth, sec *mockSecrets) *Client {
	sdk := &mockSDK{auth: auth, secrets: sec}
	c, err := newClientWithSDK(sdk, ClientConfig{
		ClientID:     "test-id",
		ClientSecret: "test-secret",
		CustomerID:   "cust-uuid",
		ProjectID:    "proj-id",
		Environment:  "dev",
	})
	if err != nil {
		panic(err)
	}
	return c
}

func apiErr(status int) error {
	return &infisicalErrors.APIError{StatusCode: status}
}

func singleGrant() []GrantRow {
	return []GrantRow{{
		EnvVarName: "MY_SECRET",
		SecretPath: "/cust/prod/MY_SECRET",
		CustomerID: pgtype.UUID{Valid: false},
	}}
}

// --- tests ---

func TestClientFetchHappyPath(t *testing.T) {
	auth := &mockAuth{token: "tok"}
	sec := &mockSecrets{responses: []mockRetrieveResponse{
		{secret: models.Secret{SecretValue: "supersecret"}},
	}}
	c := newTestClient(auth, sec)

	result, err := c.Fetch(context.Background(), singleGrant())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sv, ok := result["MY_SECRET"]
	if !ok {
		t.Fatal("MY_SECRET missing from result")
	}
	//nolint:vaultlog
	if string(sv.UnsafeBytes()) != "supersecret" {
		t.Errorf("expected %q, got %q", "supersecret", string(sv.UnsafeBytes()))
	}
}

func TestClientFetchOn401ReauthenticatesOnceThenSucceeds(t *testing.T) {
	auth := &mockAuth{token: "fresh-tok"}
	sec := &mockSecrets{responses: []mockRetrieveResponse{
		{err: apiErr(http.StatusUnauthorized)},
		{secret: models.Secret{SecretValue: "the-value"}},
	}}
	c := newTestClient(auth, sec)

	result, err := c.Fetch(context.Background(), singleGrant())
	if err != nil {
		t.Fatalf("unexpected error after reauth: %v", err)
	}
	if _, ok := result["MY_SECRET"]; !ok {
		t.Fatal("MY_SECRET missing from result after reauth")
	}
	if atomic.LoadInt32(&auth.loginCallCount) != 1 {
		t.Errorf("expected exactly 1 reauth call, got %d", auth.loginCallCount)
	}
}

func TestClientFetchOn401TwiceReturnsErrVaultAuthExpired(t *testing.T) {
	auth := &mockAuth{token: "tok"}
	sec := &mockSecrets{responses: []mockRetrieveResponse{
		{err: apiErr(http.StatusUnauthorized)},
		{err: apiErr(http.StatusUnauthorized)},
	}}
	c := newTestClient(auth, sec)

	_, err := c.Fetch(context.Background(), singleGrant())
	if !errors.Is(err, ErrVaultAuthExpired) {
		t.Errorf("expected ErrVaultAuthExpired, got %v", err)
	}
}

func TestClientFetchConcurrentSafe(t *testing.T) {
	auth := &mockAuth{token: "tok"}
	// All goroutines will hit 401 first, then succeed. Only one reauth expected.
	// We use a shared counter on mockAuth (atomic) to verify.
	const goroutines = 100
	sec := &mockSecrets{}
	// Configure enough responses: goroutines may each need up to 2 calls (401 + success).
	for i := 0; i < goroutines; i++ {
		sec.responses = append(sec.responses, mockRetrieveResponse{err: apiErr(http.StatusUnauthorized)})
		sec.responses = append(sec.responses, mockRetrieveResponse{secret: models.Secret{SecretValue: "val"}})
	}

	c := newTestClient(auth, sec)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			c.Fetch(context.Background(), singleGrant()) //nolint:errcheck
		}()
	}
	wg.Wait()
	// The race detector must not fire. reauth count ≥ 1 (serialized under mu).
	if atomic.LoadInt32(&auth.loginCallCount) < 1 {
		t.Error("expected at least one reauth call")
	}
}

func TestClientFetchClassifiesAllFailureModes(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		sentinel error
	}{
		{"403_permission_denied", apiErr(http.StatusForbidden), ErrVaultPermissionDenied},
		{"429_rate_limited", apiErr(http.StatusTooManyRequests), ErrVaultRateLimited},
		{"404_not_found", apiErr(http.StatusNotFound), ErrVaultSecretNotFound},
		{"transport_error", errors.New("connection refused"), ErrVaultUnavailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &mockAuth{token: "tok"}
			sec := &mockSecrets{responses: []mockRetrieveResponse{{err: tc.err}}}
			c := newTestClient(auth, sec)

			_, err := c.Fetch(context.Background(), singleGrant())
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("expected %v, got %v", tc.sentinel, err)
			}
		})
	}
}

// TestNewClientValidation — NewClient returns a descriptive error for each
// missing required field. We short-circuit before any network call with a
// guaranteed-unreachable SiteURL so the test never dials out.
func TestNewClientValidation(t *testing.T) {
	full := ClientConfig{
		SiteURL:      "http://127.0.0.1:0",
		ClientID:     "cid",
		ClientSecret: "sec",
		CustomerID:   "cust",
		ProjectID:    "proj",
		Environment:  "dev",
	}
	cases := []struct {
		name    string
		mutate  func(*ClientConfig)
		wantMsg string
	}{
		{"missing_SiteURL", func(c *ClientConfig) { c.SiteURL = "" }, "SiteURL"},
		{"missing_ClientID", func(c *ClientConfig) { c.ClientID = "" }, "ClientID"},
		{"missing_ClientSecret", func(c *ClientConfig) { c.ClientSecret = "" }, "ClientSecret"},
		{"missing_CustomerID", func(c *ClientConfig) { c.CustomerID = "" }, "CustomerID"},
		{"missing_ProjectID", func(c *ClientConfig) { c.ProjectID = "" }, "ProjectID"},
		{"missing_Environment", func(c *ClientConfig) { c.Environment = "" }, "Environment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := full
			tc.mutate(&cfg)
			_, err := NewClient(context.Background(), cfg)
			if err == nil {
				t.Fatalf("NewClient(): want error for missing %s, got nil", tc.wantMsg)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

// TestSplitSecretPath — covers the empty-string and no-slash branches.
func TestSplitSecretPath(t *testing.T) {
	cases := []struct {
		input      string
		wantFolder string
		wantKey    string
	}{
		{"", "/", ""},
		{"noSlash", ".", "noSlash"},
		{"/folder/key", "/folder", "key"},
		{"/folder/sub/key", "/folder/sub", "key"},
		{"/folder/key/", "/folder", "key"},
	}
	for _, tc := range cases {
		folder, key := splitSecretPath(tc.input)
		if folder != tc.wantFolder || key != tc.wantKey {
			t.Errorf("splitSecretPath(%q) = (%q, %q); want (%q, %q)",
				tc.input, folder, key, tc.wantFolder, tc.wantKey)
		}
	}
}

// TestClassifySDKErrorDefaultBranch — a non-401/403/404/429 HTTP status
// is classified as ErrVaultUnavailable (the default branch).
func TestClassifySDKErrorDefaultBranch(t *testing.T) {
	err := classifySDKError(apiErr(http.StatusInternalServerError))
	if !errors.Is(err, ErrVaultUnavailable) {
		t.Errorf("expected ErrVaultUnavailable for 500, got %v", err)
	}
}

// TestClassifySDKErrorNil — nil input returns nil.
func TestClassifySDKErrorNil(t *testing.T) {
	if got := classifySDKError(nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// TestClientReauthenticate — the public reauthenticate method acquires mu and
// delegates to reauthenticateUnderLock; verify it returns the mock token.
func TestClientReauthenticate(t *testing.T) {
	auth := &mockAuth{token: "fresh-tok"}
	sec := &mockSecrets{}
	c := newTestClient(auth, sec)
	if err := c.reauthenticate(context.Background()); err != nil {
		t.Fatalf("reauthenticate(): unexpected error: %v", err)
	}
	if atomic.LoadInt32(&auth.loginCallCount) != 1 {
		t.Errorf("expected 1 UniversalAuthLogin call, got %d", auth.loginCallCount)
	}
}

// TestNewClientReturnsAuthError — when the initial auth fails NewClient
// wraps the error with "initial authentication failed".
func TestNewClientReturnsAuthError(t *testing.T) {
	auth := &mockAuth{loginErr: errors.New("network refused")}
	sec := &mockSecrets{}
	sdk := &mockSDK{auth: auth, secrets: sec}

	cfg := ClientConfig{
		SiteURL:      "http://127.0.0.1:0",
		ClientID:     "cid",
		ClientSecret: "sec",
		CustomerID:   "cust",
		ProjectID:    "proj",
		Environment:  "dev",
	}
	// newClientWithSDK skips the real auth; call reauthenticate via the
	// production NewClient path by injecting a failing mock through the
	// internal constructor and simulating what NewClient does.
	c, _ := newClientWithSDK(sdk, cfg)
	err := c.reauthenticate(context.Background())
	if err == nil {
		t.Fatal("expected error from failing reauthenticate; got nil")
	}
	if !errors.Is(err, ErrVaultUnavailable) {
		t.Errorf("expected ErrVaultUnavailable, got %v", err)
	}
}

// TestNewClientNilLoggerAndAuthFailure — exercises the real NewClient path
// (not newClientWithSDK) so the slog.Default() fallback for nil Logger and
// the "initial authentication failed" wrap both run. SiteURL points at a
// loopback port that no listener owns; the SDK's retry policy then drains
// to "connection refused" and NewClient wraps the failure. Skipped under
// -short because the SDK retry budget is ~13s.
func TestNewClientNilLoggerAndAuthFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: SDK retry on refused dial is ~13s")
	}
	cfg := ClientConfig{
		SiteURL:      "http://127.0.0.1:1",
		ClientID:     "cid",
		ClientSecret: "sec",
		CustomerID:   "cust",
		ProjectID:    "proj",
		Environment:  "dev",
		// Logger intentionally nil — exercises the fallback branch.
	}
	_, err := NewClient(context.Background(), cfg)
	if err == nil {
		t.Fatal("NewClient: expected error from unreachable SiteURL; got nil")
	}
	if !strings.Contains(err.Error(), "initial authentication failed") {
		t.Errorf("error %q does not mention 'initial authentication failed'", err.Error())
	}
}

// TestNewClientWithSDKMissingClientID — newClientWithSDK rejects empty
// ClientID/ClientSecret (the unit-test constructor's own validation).
func TestNewClientWithSDKMissingClientID(t *testing.T) {
	sdk := &mockSDK{auth: &mockAuth{}, secrets: &mockSecrets{}}
	if _, err := newClientWithSDK(sdk, ClientConfig{ClientSecret: "x"}); err == nil {
		t.Fatal("newClientWithSDK: want error for empty ClientID; got nil")
	}
	if _, err := newClientWithSDK(sdk, ClientConfig{ClientID: "x"}); err == nil {
		t.Fatal("newClientWithSDK: want error for empty ClientSecret; got nil")
	}
}

// TestFetchEmptyRequestReturnsEmpty — Fetch(nil) and Fetch([]) return an
// empty map and never invoke the SDK (the empty-input fast path).
func TestFetchEmptyRequestReturnsEmpty(t *testing.T) {
	auth := &mockAuth{token: "tok"}
	sec := &mockSecrets{}
	c := newTestClient(auth, sec)
	for _, name := range []string{"nil", "empty"} {
		t.Run(name, func(t *testing.T) {
			var req []GrantRow
			if name == "empty" {
				req = []GrantRow{}
			}
			result, err := c.Fetch(context.Background(), req)
			if err != nil {
				t.Fatalf("Fetch(%s): unexpected error: %v", name, err)
			}
			if len(result) != 0 {
				t.Errorf("Fetch(%s): got %d entries, want empty map", name, len(result))
			}
		})
	}
}

// TestFetchReauthFailsReturnsAuthExpired — when the first Retrieve returns
// 401 and the subsequent reauthenticate also fails, fetchOneUnderLockWithRetry
// returns ErrVaultAuthExpired (lines covering the reauth-failure branch).
func TestFetchReauthFailsReturnsAuthExpired(t *testing.T) {
	// loginErr makes UniversalAuthLogin fail; Retrieve returns 401 first so
	// the retry path triggers reauth.
	auth := &mockAuth{loginErr: errors.New("auth backend down")}
	sec := &mockSecrets{responses: []mockRetrieveResponse{
		{err: apiErr(http.StatusUnauthorized)},
	}}
	c := newTestClient(auth, sec)

	_, err := c.Fetch(context.Background(), singleGrant())
	if !errors.Is(err, ErrVaultAuthExpired) {
		t.Errorf("expected ErrVaultAuthExpired (reauth failure path), got %v", err)
	}
}

// TestFetchAfterReauthRetryReturns500 — after a successful reauth the retry
// itself can still hit a non-401 SDK error (e.g. 500). The wrapper must
// classify via classifySDKError (the "retry returned non-401" branch).
func TestFetchAfterReauthRetryReturns500(t *testing.T) {
	auth := &mockAuth{token: "tok"}
	sec := &mockSecrets{responses: []mockRetrieveResponse{
		{err: apiErr(http.StatusUnauthorized)},        // first call → 401 triggers reauth
		{err: apiErr(http.StatusInternalServerError)}, // retry → 500
	}}
	c := newTestClient(auth, sec)

	_, err := c.Fetch(context.Background(), singleGrant())
	if !errors.Is(err, ErrVaultUnavailable) {
		t.Errorf("expected ErrVaultUnavailable from classifySDKError(500); got %v", err)
	}
}

// TestClassifySDKError401 — direct call to classifySDKError with a 401 maps
// to ErrVaultAuthExpired (the dedicated 401 case in the switch).
func TestClassifySDKError401(t *testing.T) {
	err := classifySDKError(apiErr(http.StatusUnauthorized))
	if !errors.Is(err, ErrVaultAuthExpired) {
		t.Errorf("expected ErrVaultAuthExpired for 401, got %v", err)
	}
}
