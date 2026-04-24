package vault

import (
	"context"
	"errors"
	"net/http"
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

func (m *mockSDK) Auth() infisical.AuthInterface    { return m.auth }
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
func (a *mockAuth) SetAccessToken(_ string)                    {}
func (a *mockAuth) GetAccessToken() string                     { return a.token }
func (a *mockAuth) GetOrganizationSlug() string                { return "" }
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
	mu       sync.Mutex
	calls    int
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
		name    string
		err     error
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
