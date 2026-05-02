package skillregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// skillhubClient is the SkillHub registry client. SkillHub is the
// self-hosted skill registry from github.com/iflytek/skillhub —
// Garrison runs an instance alongside the supervisor on the same
// Hetzner host (RATIONALE §12 self-hosted posture preserved).
//
// Endpoint shape (iflytek/skillhub repo as of M7 spike):
//
//	GET <base>/api/skills/<package>/versions/<version>/tarball
//	GET <base>/api/skills/<package>/versions/<version>
//
// Auth: a static admin token minted by the operator at SkillHub
// deployment time, passed via GARRISON_SKILLHUB_TOKEN (T008 config).
// SkillHub's own RBAC controls who can publish; Garrison's supervisor
// only consumes (read-only) so the token grants read access only.
//
// The API shape above mirrors skillsh.go's client deliberately. If
// the iflytek/skillhub instance the operator deploys exposes a
// different shape, adjust this client OR run SkillHub behind a tiny
// reverse-proxy that translates. The supervisor doesn't constrain
// the choice.
type skillhubClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewSkillHubClient constructs a SkillHub client. baseURL points at
// the operator's self-hosted instance (e.g. http://skillhub:8080
// inside the Compose network). token is the read-scope admin token
// minted at SkillHub deploy time; an empty token causes auth-required
// endpoints to return ErrRegistryAuthFailed without an HTTP roundtrip.
func NewSkillHubClient(baseURL, token string, httpClient *http.Client) Registry {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &skillhubClient{baseURL: baseURL, token: token, http: httpClient}
}

func (c *skillhubClient) Name() string { return "skillhub" }

func (c *skillhubClient) Fetch(ctx context.Context, pkg, version string) ([]byte, string, error) {
	if c.token == "" {
		return nil, "", fmt.Errorf("%w: GARRISON_SKILLHUB_TOKEN not configured", ErrRegistryAuthFailed)
	}
	u := c.baseURL + "/api/skills/" + url.PathEscape(pkg) +
		"/versions/" + url.PathEscape(version) + "/tarball"
	body, err := c.get(ctx, u, "*/*")
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

func (c *skillhubClient) Describe(ctx context.Context, pkg, version string) (Metadata, error) {
	if c.token == "" {
		return Metadata{}, fmt.Errorf("%w: GARRISON_SKILLHUB_TOKEN not configured", ErrRegistryAuthFailed)
	}
	u := c.baseURL + "/api/skills/" + url.PathEscape(pkg) +
		"/versions/" + url.PathEscape(version)
	body, err := c.get(ctx, u, "application/json")
	if err != nil {
		return Metadata{}, err
	}
	var raw struct {
		Author      string `json:"author"`
		Description string `json:"description"`
		SHA256      string `json:"sha256"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Metadata{}, fmt.Errorf("skillhub: parse describe: %w", err)
	}
	return Metadata{
		Package: pkg, Version: version,
		Author: raw.Author, Description: raw.Description, SHA256: raw.SHA256,
	}, nil
}

// get mirrors skillsh.go's transport with the bearer-token Authorization
// header added. Retry-after handling identical to the public client.
func (c *skillhubClient) get(ctx context.Context, urlStr, accept string) ([]byte, error) {
	const retryBudget = 30 * time.Second
	deadline := time.Now().Add(retryBudget)
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: build request: %v", ErrRegistryUnreachable, err)
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("User-Agent", "garrison-supervisor")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRegistryUnreachable, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("%w: read body: %v", ErrRegistryUnreachable, readErr)
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			return body, nil
		case resp.StatusCode == http.StatusNotFound:
			return nil, fmt.Errorf("%w: %s", ErrPackageNotFound, urlStr)
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("%w: status %d (rotate GARRISON_SKILLHUB_TOKEN)", ErrRegistryAuthFailed, resp.StatusCode)
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			if wait <= 0 || time.Now().Add(wait).After(deadline) {
				return nil, fmt.Errorf("%w: retry-after %s exceeds budget", ErrRegistryRateLimited, wait)
			}
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("%w: ctx cancel during retry-after", ErrRegistryUnreachable)
			}
		case resp.StatusCode >= 500:
			return nil, fmt.Errorf("%w: status %d", ErrRegistryServerError, resp.StatusCode)
		default:
			return nil, fmt.Errorf("%w: unexpected status %d", ErrRegistryUnreachable, resp.StatusCode)
		}
	}
	return nil, fmt.Errorf("%w: retry budget exhausted", ErrRegistryUnreachable)
}
