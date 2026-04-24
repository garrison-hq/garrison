//go:build integration

// Package vault — testutil.go provides InfisicalTestHarness: a testcontainers-go
// harness that boots Postgres 15 + Redis 7 + Infisical and exposes helper
// methods for seeding secrets and creating machine identities in tests.
//
// All three containers share a private Docker bridge network so Infisical can
// reach its Postgres and Redis siblings by DNS alias without exposing their
// ports on the host. Only Infisical's port 8080 is mapped to a random host
// port for test code to call.
//
// Admin bootstrap in Infisical v0.159.22 uses a two-step flow:
//  1. POST /api/v1/admin/signup  → returns a user-level (no-org) JWT
//  2. POST /api/v3/auth/select-organization → returns an org-scoped JWT
//
// Subsequent API calls use the org-scoped JWT.
package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// InfisicalImage is the pinned Infisical Docker image for integration tests.
// The original spec referenced v0.82.21 which does not exist on Docker Hub;
// the first available series starts at v0.40.x-postgres. v0.159.22 is the
// latest stable non-postgres-variant release as of implementation (2026-04-24).
// Recorded in acceptance-evidence.md (T019) as the ground-truth version for
// SC-411 dep-pin verification.
const InfisicalImage = "infisical/infisical:v0.159.22"

// network alias constants for intra-container DNS within the shared bridge.
const (
	infisicalPGAlias    = "infisical-postgres"
	infisicalRedisAlias = "infisical-redis"
)

// InfisicalTestHarness bundles three containers and bootstrapped admin credentials.
type InfisicalTestHarness struct {
	url      string // host-mapped URL, e.g. "http://localhost:XXXX"
	orgToken string // org-scoped JWT for API management calls
	orgID    string // Infisical organization UUID

	// Workspace (Infisical "project") created during bootstrap.
	projectID string

	// Container handles for cleanup.
	infisicalC testcontainers.Container
	pgC        *postgres.PostgresContainer
	redisC     testcontainers.Container
	net        *testcontainers.DockerNetwork
}

// URL returns the Infisical server URL reachable from the host (test code).
func (h *InfisicalTestHarness) URL() string { return h.url }

// ProjectID returns the test workspace ID required by vault.ClientConfig.ProjectID.
func (h *InfisicalTestHarness) ProjectID() string { return h.projectID }

// Environment returns the environment slug used by the test workspace (always "dev").
func (h *InfisicalTestHarness) Environment() string { return "dev" }

// CreateMachineIdentity creates a machine identity with Universal Auth, grants it
// admin access to the test project, and returns (clientID, clientSecret).
func (h *InfisicalTestHarness) CreateMachineIdentity(name string) (clientID, clientSecret string, err error) {
	// Step 1: create identity in the organisation.
	identityID, err := h.createIdentity(name)
	if err != nil {
		return "", "", fmt.Errorf("create identity: %w", err)
	}

	// Step 2: configure Universal Auth and get client ID.
	clientID, err = h.configureUniversalAuth(identityID)
	if err != nil {
		return "", "", fmt.Errorf("configure universal auth: %w", err)
	}

	// Step 3: create a client secret.
	clientSecret, err = h.createClientSecret(identityID)
	if err != nil {
		return "", "", fmt.Errorf("create client secret: %w", err)
	}

	// Step 4: add identity to the project with admin role so it can read secrets.
	if err := h.addIdentityToProject(identityID); err != nil {
		return "", "", fmt.Errorf("add identity to project: %w", err)
	}

	return clientID, clientSecret, nil
}

// SeedSecret seeds a secret at the given folder path (e.g. "/operator/stripe") with
// the given key name and value. Uses the admin JWT — suitable for test setup only.
// The secretPath argument is the FOLDER path; secretKey is the leaf key name.
// vault.Client.Fetch expects grant.SecretPath = secretPath + "/" + secretKey.
// Folder segments are created automatically if they do not exist.
func (h *InfisicalTestHarness) SeedSecret(secretPath, secretKey, secretValue string) error {
	if err := h.ensureFolders(secretPath); err != nil {
		return fmt.Errorf("ensure folders for %s: %w", secretPath, err)
	}
	body := map[string]interface{}{
		"workspaceId": h.projectID,
		"environment": "dev",
		"secretPath":  secretPath,
		"secretValue": secretValue,
	}
	resp, err := h.apiCall("POST", "/api/v3/secrets/raw/"+secretKey, body)
	if err != nil {
		return fmt.Errorf("seed secret %s/%s: %w", secretPath, secretKey, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("seed secret %s/%s: HTTP %d: %s", secretPath, secretKey, resp.StatusCode, b)
	}
	return nil
}

// ensureFolders creates each segment of folderPath under the project's "dev"
// environment. Already-existing folders (HTTP 400 with "already exists") are
// silently ignored so the call is idempotent.
func (h *InfisicalTestHarness) ensureFolders(folderPath string) error {
	// Normalise: trim leading/trailing slashes, then split.
	trimmed := strings.Trim(folderPath, "/")
	if trimmed == "" {
		return nil // root always exists
	}
	segments := strings.Split(trimmed, "/")
	parentPath := "/"
	for _, seg := range segments {
		body := map[string]interface{}{
			"workspaceId": h.projectID,
			"environment": "dev",
			"name":        seg,
			"path":        parentPath,
		}
		resp, err := h.apiCall("POST", "/api/v1/folders", body)
		if err != nil {
			return fmt.Errorf("create folder %s in %s: %w", seg, parentPath, err)
		}
		rawBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// 200/201 = created; 400 with "already exists" = idempotent ok.
		if resp.StatusCode != 200 && resp.StatusCode != 201 {
			msg := string(rawBody)
			if resp.StatusCode == 400 && strings.Contains(strings.ToLower(msg), "already exist") {
				// folder already present — continue
			} else {
				return fmt.Errorf("create folder %s in %s: HTTP %d: %s", seg, parentPath, resp.StatusCode, msg)
			}
		}
		if parentPath == "/" {
			parentPath = "/" + seg
		} else {
			parentPath = parentPath + "/" + seg
		}
	}
	return nil
}

// Cleanup terminates all containers and removes the Docker network.
func (h *InfisicalTestHarness) Cleanup() {
	ctx := context.Background()
	if h.infisicalC != nil {
		_ = h.infisicalC.Terminate(ctx)
	}
	if h.redisC != nil {
		_ = h.redisC.Terminate(ctx)
	}
	if h.pgC != nil {
		_ = h.pgC.Terminate(ctx)
	}
	if h.net != nil {
		_ = h.net.Remove(ctx)
	}
}

// StartInfisical boots the three-container Infisical stack on a shared Docker
// bridge network, runs the admin bootstrap, and returns a ready-to-use harness.
// t.Cleanup is registered automatically.
func StartInfisical(t *testing.T) *InfisicalTestHarness {
	t.Helper()
	ctx := context.Background()

	// Shared bridge network so containers resolve each other by alias.
	net, err := tcnetwork.New(ctx)
	if err != nil {
		t.Fatalf("StartInfisical: create docker network: %v", err)
	}

	// Postgres 15 for Infisical's data store.
	pgC, err := postgres.Run(ctx,
		"postgres:15-alpine",
		postgres.WithDatabase("infisical"),
		postgres.WithUsername("infisical"),
		postgres.WithPassword("infisical-test-pw"),
		tcnetwork.WithNetwork([]string{infisicalPGAlias}, net),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		_ = net.Remove(ctx)
		t.Fatalf("StartInfisical: start postgres: %v", err)
	}

	// Redis 7 for Infisical cache/queue.
	redisC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:          "redis:7-alpine",
			ExposedPorts:   []string{"6379/tcp"},
			Networks:       []string{net.Name},
			NetworkAliases: map[string][]string{net.Name: {infisicalRedisAlias}},
			WaitingFor:     wait.ForLog("Ready to accept connections").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		_ = pgC.Terminate(ctx)
		_ = net.Remove(ctx)
		t.Fatalf("StartInfisical: start redis: %v", err)
	}

	// Infisical server on the same network.  DB_CONNECTION_URI and REDIS_URL use
	// intra-network aliases so Infisical never needs the host-mapped ports.
	infisicalC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:          InfisicalImage,
			ExposedPorts:   []string{"8080/tcp"},
			Networks:       []string{net.Name},
			NetworkAliases: map[string][]string{net.Name: {"infisical-server"}},
			Env: map[string]string{
				"ENCRYPTION_KEY":          "6c1fe4e407b8911c104518103505b218",
				"AUTH_SECRET":             "JpRi1OB18JFjFlNXj+j9USjFiMPXBimW7EJNzS4/b8s=",
				"DB_CONNECTION_URI":       "postgresql://infisical:infisical-test-pw@" + infisicalPGAlias + ":5432/infisical",
				"REDIS_URL":               "redis://" + infisicalRedisAlias + ":6379",
				"HTTPS_ENABLED":           "false",
				"TELEMETRY_ENABLED":       "false",
				"SITE_URL":                "http://localhost:8080",
				"DISABLE_SECRET_SCANNING": "true",
				"NODE_ENV":                "development",
			},
			WaitingFor: wait.ForHTTP("/api/status").
				WithPort("8080/tcp").
				WithStatusCodeMatcher(func(status int) bool { return status < 500 }).
				WithStartupTimeout(5 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		_ = pgC.Terminate(ctx)
		_ = redisC.Terminate(ctx)
		_ = net.Remove(ctx)
		t.Fatalf("StartInfisical: start infisical: %v", err)
	}

	infisicalHost, err := infisicalC.Host(ctx)
	if err != nil {
		_ = pgC.Terminate(ctx)
		_ = redisC.Terminate(ctx)
		_ = infisicalC.Terminate(ctx)
		_ = net.Remove(ctx)
		t.Fatalf("StartInfisical: infisical host: %v", err)
	}
	infisicalPort, err := infisicalC.MappedPort(ctx, "8080")
	if err != nil {
		_ = pgC.Terminate(ctx)
		_ = redisC.Terminate(ctx)
		_ = infisicalC.Terminate(ctx)
		_ = net.Remove(ctx)
		t.Fatalf("StartInfisical: infisical port: %v", err)
	}

	h := &InfisicalTestHarness{
		url:        fmt.Sprintf("http://%s:%s", infisicalHost, infisicalPort.Port()),
		infisicalC: infisicalC,
		pgC:        pgC,
		redisC:     redisC,
		net:        net,
	}

	if err := h.bootstrap(); err != nil {
		h.Cleanup()
		t.Fatalf("StartInfisical: bootstrap: %v", err)
	}

	t.Cleanup(h.Cleanup)
	return h
}

// bootstrap performs the two-step Infisical v0.159.22 admin setup:
//  1. POST /api/v1/admin/signup  → user-level JWT
//  2. POST /api/v3/auth/select-organization → org-scoped JWT
//
// Then creates the test project.
func (h *InfisicalTestHarness) bootstrap() error {
	// Step 1: create admin user (no auth required; only works on uninitialized instances).
	signupBody := map[string]interface{}{
		"firstName": "Test",
		"lastName":  "Admin",
		"email":     "admin@garrison.test",
		"password":  "Garrison-Test-Password-2026!",
	}
	resp, err := h.unauthCall("POST", "/api/v1/admin/signup", signupBody)
	if err != nil {
		return fmt.Errorf("admin signup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("admin signup: HTTP %d: %s", resp.StatusCode, b)
	}
	var signupResp struct {
		Token        string `json:"token"`
		Organization struct {
			ID string `json:"id"`
		} `json:"organization"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&signupResp); err != nil {
		return fmt.Errorf("decode admin signup: %w", err)
	}
	userToken := signupResp.Token
	h.orgID = signupResp.Organization.ID

	// Step 2: exchange user-level JWT for org-scoped JWT.
	orgTokenBody := map[string]interface{}{
		"organizationId": h.orgID,
	}
	resp2, err := h.call("POST", "/api/v3/auth/select-organization", orgTokenBody, userToken)
	if err != nil {
		return fmt.Errorf("select organization: %w", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		b, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("select organization: HTTP %d: %s", resp2.StatusCode, b)
	}
	var selectOrgResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&selectOrgResp); err != nil {
		return fmt.Errorf("decode select-organization: %w", err)
	}
	h.orgToken = selectOrgResp.Token

	// Step 3: create the test project (workspace).
	wsBody := map[string]interface{}{
		"projectName": "garrison-test",
	}
	resp3, err := h.apiCall("POST", "/api/v1/projects", wsBody)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != 200 && resp3.StatusCode != 201 {
		b, _ := io.ReadAll(resp3.Body)
		return fmt.Errorf("create project: HTTP %d: %s", resp3.StatusCode, b)
	}
	var wsJSON struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&wsJSON); err != nil {
		return fmt.Errorf("decode create-project: %w", err)
	}
	h.projectID = wsJSON.Project.ID

	return nil
}

func (h *InfisicalTestHarness) createIdentity(name string) (string, error) {
	body := map[string]interface{}{
		"name":           name,
		"organizationId": h.orgID,
		"role":           "member",
	}
	resp, err := h.apiCall("POST", "/api/v1/identities", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create identity HTTP %d: %s", resp.StatusCode, b)
	}
	var result struct {
		Identity struct {
			ID string `json:"id"`
		} `json:"identity"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Identity.ID, nil
}

func (h *InfisicalTestHarness) configureUniversalAuth(identityID string) (string, error) {
	body := map[string]interface{}{
		"clientSecretTrustedIps":  []map[string]string{{"ipAddress": "0.0.0.0/0"}},
		"accessTokenTrustedIps":   []map[string]string{{"ipAddress": "0.0.0.0/0"}},
		"accessTokenTTL":          86400,
		"accessTokenMaxTTL":       2592000,
		"accessTokenNumUsesLimit": 0,
	}
	resp, err := h.apiCall("POST", "/api/v1/auth/universal-auth/identities/"+identityID, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("configure UA HTTP %d: %s", resp.StatusCode, b)
	}
	var result struct {
		IdentityUniversalAuth struct {
			ClientID string `json:"clientId"`
		} `json:"identityUniversalAuth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.IdentityUniversalAuth.ClientID, nil
}

func (h *InfisicalTestHarness) createClientSecret(identityID string) (string, error) {
	body := map[string]interface{}{
		"description":  "garrison-test",
		"numUsesLimit": 0,
		"ttl":          0,
	}
	resp, err := h.apiCall("POST", "/api/v1/auth/universal-auth/identities/"+identityID+"/client-secrets", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create client secret HTTP %d: %s", resp.StatusCode, b)
	}
	var result struct {
		ClientSecret string `json:"clientSecret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.ClientSecret, nil
}

// addIdentityToProject grants the identity admin access to the test project.
func (h *InfisicalTestHarness) addIdentityToProject(identityID string) error {
	path := fmt.Sprintf("/api/v1/projects/%s/memberships/identities/%s", h.projectID, identityID)
	resp, err := h.apiCall("POST", path, map[string]interface{}{"role": "admin"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add identity to project HTTP %d: %s", resp.StatusCode, b)
	}
	return nil
}

func (h *InfisicalTestHarness) apiCall(method, path string, body interface{}) (*http.Response, error) {
	return h.call(method, path, body, h.orgToken)
}

func (h *InfisicalTestHarness) unauthCall(method, path string, body interface{}) (*http.Response, error) {
	return h.call(method, path, body, "")
}

func (h *InfisicalTestHarness) call(method, path string, body interface{}, token string) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, h.url+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(req)
}

// StopInfisical terminates the Infisical container, simulating a server-down
// failure. The harness remains usable for test assertions; call Cleanup to
// also stop postgres and redis.
func (h *InfisicalTestHarness) StopInfisical(ctx context.Context) error {
	if h.infisicalC != nil {
		return h.infisicalC.Terminate(ctx)
	}
	return nil
}

// CreateMachineIdentityNoProjectAccess creates an ML in the organisation but
// does NOT add it to the test project. The ML can authenticate (and get an
// access token) but cannot read secrets from the project → Infisical returns
// 401/403 on secret fetch, which the vault client classifies as a permission
// error or auth-expired depending on Infisical's version behaviour.
func (h *InfisicalTestHarness) CreateMachineIdentityNoProjectAccess(name string) (clientID, clientSecret string, err error) {
	identityID, err := h.createIdentity(name)
	if err != nil {
		return "", "", fmt.Errorf("create identity: %w", err)
	}
	clientID, err = h.configureUniversalAuth(identityID)
	if err != nil {
		return "", "", fmt.Errorf("configure universal auth: %w", err)
	}
	clientSecret, err = h.createClientSecret(identityID)
	if err != nil {
		return "", "", fmt.Errorf("create client secret: %w", err)
	}
	// Intentionally NOT calling addIdentityToProject: the ML has no project access.
	return clientID, clientSecret, nil
}

// CreateShortLivedMachineIdentity creates an ML whose access token has a 1-second
// TTL and whose client secret can be used only once (numUsesLimit=1). After the
// token expires (~1s after creation), the supervisor's re-auth will fail because
// the client secret's use limit is exhausted. Use this to force vault_auth_expired.
func (h *InfisicalTestHarness) CreateShortLivedMachineIdentity(name string) (clientID, clientSecret string, err error) {
	identityID, err := h.createIdentity(name)
	if err != nil {
		return "", "", fmt.Errorf("create identity: %w", err)
	}
	// accessTokenTTL=1 means the access token expires after 1 second.
	clientID, err = h.configureUniversalAuthWithTTL(identityID, 1)
	if err != nil {
		return "", "", fmt.Errorf("configure universal auth: %w", err)
	}
	// numUsesLimit=1: client secret can only authenticate once (the initial
	// vault.NewClient call). Any subsequent re-auth attempt will be rejected.
	clientSecret, err = h.createClientSecretWithLimit(identityID, 1)
	if err != nil {
		return "", "", fmt.Errorf("create client secret: %w", err)
	}
	if err := h.addIdentityToProject(identityID); err != nil {
		return "", "", fmt.Errorf("add identity to project: %w", err)
	}
	return clientID, clientSecret, nil
}

func (h *InfisicalTestHarness) configureUniversalAuthWithTTL(identityID string, accessTokenTTL int) (string, error) {
	body := map[string]interface{}{
		"clientSecretTrustedIps":  []map[string]string{{"ipAddress": "0.0.0.0/0"}},
		"accessTokenTrustedIps":   []map[string]string{{"ipAddress": "0.0.0.0/0"}},
		"accessTokenTTL":          accessTokenTTL,
		"accessTokenMaxTTL":       2592000,
		"accessTokenNumUsesLimit": 0,
	}
	resp, err := h.apiCall("POST", "/api/v1/auth/universal-auth/identities/"+identityID, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("configure UA HTTP %d: %s", resp.StatusCode, b)
	}
	var result struct {
		IdentityUniversalAuth struct {
			ClientID string `json:"clientId"`
		} `json:"identityUniversalAuth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.IdentityUniversalAuth.ClientID, nil
}

func (h *InfisicalTestHarness) createClientSecretWithLimit(identityID string, numUsesLimit int) (string, error) {
	body := map[string]interface{}{
		"description":  "garrison-test-limited",
		"numUsesLimit": numUsesLimit,
		"ttl":          0,
	}
	resp, err := h.apiCall("POST", "/api/v1/auth/universal-auth/identities/"+identityID+"/client-secrets", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("create client secret HTTP %d: %s", resp.StatusCode, b)
	}
	var result struct {
		ClientSecret string `json:"clientSecret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.ClientSecret, nil
}

// infisicalPathJoin joins path parts into a normalized absolute path.
func infisicalPathJoin(parts ...string) string {
	result := strings.Join(parts, "/")
	for strings.Contains(result, "//") {
		result = strings.ReplaceAll(result, "//", "/")
	}
	if !strings.HasPrefix(result, "/") {
		result = "/" + result
	}
	return result
}
