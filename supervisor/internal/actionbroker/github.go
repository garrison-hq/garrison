package actionbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrRecoverable is returned by PostComment when the provider call
// fails with a transient error (HTTP 429/5xx, network failure) that
// may succeed on a future attempt. The dispatcher marks the row 'failed'
// without auto-retrying (FR-022/D12) because GitHub comment-create is
// not natively idempotent — a retry of an in-doubt POST could double-post
// (plan Phase 0 idempotency note). The row surfaces in the Outbox for
// operator-initiated re-request.
var ErrRecoverable = errors.New("actionbroker: recoverable provider failure")

// Target is the JSON shape stored in pending_actions.target for the
// "github_issue_comment" action type (plan Phase 0 contract, FR-020).
// The dispatcher unmarshals the JSONB column into this struct at dispatch
// time; the verb handler stores whatever JSON blob the agent provides.
type Target struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
}

// PostCommentClient implements GitHubPoster using stdlib net/http. An
// injected *http.Client allows tests to swap in an httptest.Server
// without any network I/O. The zero value is safe to use with a nil
// HTTPClient — it defaults to a 10-second-timeout client.
//
// PAT handling:
//   - The PAT is accepted as a plain string parameter and used only to
//     set the Authorization header.
//   - The PAT is never assigned to any struct field, never passed to
//     any slog/fmt call (vaultlog discipline, SC-005).
//   - The caller (dispatcher.go Handle) calls SecretValue.Zero() after
//     converting the vault value to the string passed here.
type PostCommentClient struct {
	// HTTPClient is the injected transport. Defaults to a
	// 10-second-timeout client when nil.
	HTTPClient *http.Client
	// BaseURL overrides the GitHub REST API root. When empty (production)
	// it defaults to "https://api.github.com". Tests set this to an
	// httptest.Server URL.
	BaseURL string
}

// PostComment posts body as a new comment on the given GitHub issue
// using the PAT for authentication (FR-020, plan Phase 0 contract).
//
// API: POST /repos/{owner}/{repo}/issues/{issue_number}/comments
// Auth: Authorization: token <PAT>
// Accept: application/vnd.github+json
// Body: {"body":"<text>"}
// Success: 201 Created → extracts html_url from the response body.
//
// Error classification (plan Phase 0):
//   - 429, 5xx → ErrRecoverable (transient; no auto-retry per FR-022).
//   - 403       → ErrRecoverable (rate-limited or temporarily blocked).
//   - 404, 422  → plain error (terminal failure: repo/issue gone or
//     validation error; operator must re-request with corrected args).
func (c *PostCommentClient) PostComment(ctx context.Context, target Target, body, pat string) (string, error) {
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	url := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments",
		baseURL, target.Owner, target.Repo, target.IssueNumber)

	requestBody, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", fmt.Errorf("actionbroker: marshal comment body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(requestBody))
	if err != nil {
		return "", fmt.Errorf("actionbroker: build request: %w", err)
	}
	// PAT is used only here — sent as the Authorization header value.
	// It is never assigned to a log field or a struct field. Zero() is
	// called by the dispatcher after this call returns (vaultlog discipline).
	req.Header.Set("Authorization", "token "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		// Network-level failure — treat as recoverable (transient).
		return "", fmt.Errorf("%w: network error: %v", ErrRecoverable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		respBody = nil
	}

	switch {
	case resp.StatusCode == http.StatusCreated: // 201
		// Success: extract html_url from the response JSON.
		var result struct {
			HTMLURL string `json:"html_url"`
		}
		if jsonErr := json.Unmarshal(respBody, &result); jsonErr != nil {
			return "", fmt.Errorf("actionbroker: parse 201 response: %w", jsonErr)
		}
		return result.HTMLURL, nil

	case resp.StatusCode == http.StatusForbidden, // 403
		resp.StatusCode == http.StatusTooManyRequests: // 429
		// Rate-limited or temporarily blocked — recoverable.
		return "", fmt.Errorf("%w: HTTP %d", ErrRecoverable, resp.StatusCode)

	case resp.StatusCode >= 500: // 5xx
		// Server error — recoverable (transient provider failure).
		return "", fmt.Errorf("%w: HTTP %d", ErrRecoverable, resp.StatusCode)

	case resp.StatusCode == http.StatusNotFound, // 404
		resp.StatusCode == http.StatusUnprocessableEntity: // 422
		// Terminal: repo/issue not found or validation error.
		return "", fmt.Errorf("actionbroker: terminal provider error: HTTP %d", resp.StatusCode)

	default:
		// Any other non-2xx — treat as terminal to avoid silent retries
		// on unknown status codes.
		return "", fmt.Errorf("actionbroker: unexpected HTTP %d from GitHub", resp.StatusCode)
	}
}
