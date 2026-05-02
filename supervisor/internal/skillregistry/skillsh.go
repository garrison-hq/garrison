package skillregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// skillshClient is the public skills.sh registry client. Anonymous
// HTTPS GET; the registry is read-only from Garrison's perspective
// (we never publish skills via this client).
//
// Endpoint shape (skills.sh public docs as of 2026-04-24):
//
//	GET <base>/api/v1/packages/<package>/versions/<version>/tarball
//	GET <base>/api/v1/packages/<package>/versions/<version>
//
// The Describe call returns a JSON body with fields used by the
// dashboard approval surface; field names follow the public schema.
type skillshClient struct {
	baseURL string
	http    *http.Client
}

// NewSkillsShClient constructs a skills.sh client. baseURL must be a
// valid HTTPS URL (typically the public registry); httpClient is
// optional and defaults to a 30s-timeout client.
func NewSkillsShClient(baseURL string, httpClient *http.Client) Registry {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &skillshClient{baseURL: baseURL, http: httpClient}
}

func (c *skillshClient) Name() string { return "skills.sh" }

func (c *skillshClient) Fetch(ctx context.Context, pkg, version string) ([]byte, string, error) {
	u := c.baseURL + "/api/v1/packages/" + url.PathEscape(pkg) +
		"/versions/" + url.PathEscape(version) + "/tarball"
	body, err := c.get(ctx, u, "*/*")
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

func (c *skillshClient) Describe(ctx context.Context, pkg, version string) (Metadata, error) {
	u := c.baseURL + "/api/v1/packages/" + url.PathEscape(pkg) +
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
		return Metadata{}, fmt.Errorf("skills.sh: parse describe: %w", err)
	}
	return Metadata{
		Package: pkg, Version: version,
		Author: raw.Author, Description: raw.Description, SHA256: raw.SHA256,
	}, nil
}

// get issues an HTTP GET with retry-after honoured up to a 30s budget
// across one retry. Maps HTTP statuses to the package's sentinel errors.
func (c *skillshClient) get(ctx context.Context, urlStr, accept string) ([]byte, error) {
	const retryBudget = 30 * time.Second
	deadline := time.Now().Add(retryBudget)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: build request: %v", ErrRegistryUnreachable, err)
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("User-Agent", "garrison-supervisor")
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%w: %v", ErrRegistryUnreachable, err)
			return nil, lastErr
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
			return nil, fmt.Errorf("%w: status %d", ErrRegistryAuthFailed, resp.StatusCode)
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
	if lastErr == nil {
		lastErr = errors.New("registry: retry budget exhausted without resolution")
	}
	return nil, lastErr
}

// parseRetryAfter accepts either a delta-seconds integer or an
// HTTP-date. Delta-seconds is the common shape from skills.sh.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
