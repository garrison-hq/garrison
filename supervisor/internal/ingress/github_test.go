package ingress

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestGitHubConnector returns a GitHubConnector wired with the standard
// M10-core routes so tests can exercise Filter and Map without repeating
// boilerplate route configuration.
func newTestGitHubConnector() *GitHubConnector {
	return NewGitHubConnector(GitHubConfig{
		ConnectorID: "github-test",
		Secret:      []byte("test-secret"),
		Routes: map[string]Route{
			"issues": {
				DepartmentSlug:     "engineering",
				ObjectiveTemplate:  "Issue: {{title}}",
				AcceptanceTemplate: "{{body}}",
			},
			"pull_request": {
				DepartmentSlug:     "engineering",
				ObjectiveTemplate:  "PR review: {{title}}",
				AcceptanceTemplate: "{{body}}",
			},
		},
		RatePerMin: 60,
		Burst:      30,
	})
}

// buildBody is a test helper that serialises the given value to a JSON byte
// slice; it fatally fails the test on serialisation error.
func buildBody(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("buildBody: json.Marshal failed: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// Filter tests
// ---------------------------------------------------------------------------

// TestGitHubFilter_PingDiscarded — the "ping" event type must always be
// discarded regardless of body content (FR-403, SR9). GitHub sends ping on
// webhook registration; returning 200 without a ticket lets registration
// succeed.
func TestGitHubFilter_PingDiscarded(t *testing.T) {
	conn := newTestGitHubConnector()
	decision, err := conn.Filter("ping", []byte(`{}`))
	if err != nil {
		t.Errorf("Filter(ping) error = %v; want nil", err)
	}
	if decision != FilterDiscard {
		t.Errorf("Filter(ping) = %v; want FilterDiscard", decision)
	}
}

// TestGitHubFilter_BotSenderTypeDiscarded — a payload where sender.type equals
// "Bot" must be discarded (FR-401, spike F7). This matches the GitHub API
// canonical value for bot actors.
func TestGitHubFilter_BotSenderTypeDiscarded(t *testing.T) {
	conn := newTestGitHubConnector()
	body := buildBody(t, map[string]any{
		"action": "opened",
		"sender": map[string]any{
			"type":  "Bot",
			"login": "some-app",
		},
	})
	decision, err := conn.Filter("issues", body)
	if err != nil {
		t.Errorf("Filter with Bot sender.type error = %v; want nil", err)
	}
	if decision != FilterDiscard {
		t.Errorf("Filter with Bot sender.type = %v; want FilterDiscard", decision)
	}
}

// TestGitHubFilter_BotSenderLoginSuffixDiscarded — a payload where
// sender.login ends with "[bot]" must be discarded (FR-401, spike F7).
// GitHub Apps convention: the login for a bot is "app-name[bot]".
func TestGitHubFilter_BotSenderLoginSuffixDiscarded(t *testing.T) {
	conn := newTestGitHubConnector()
	body := buildBody(t, map[string]any{
		"action": "opened",
		"sender": map[string]any{
			"type":  "User", // type is User but login ends with [bot]
			"login": "dependabot[bot]",
		},
	})
	decision, err := conn.Filter("issues", body)
	if err != nil {
		t.Errorf("Filter with [bot] login suffix error = %v; want nil", err)
	}
	if decision != FilterDiscard {
		t.Errorf("Filter with [bot] login suffix = %v; want FilterDiscard", decision)
	}
}

// TestGitHubFilter_IssuesActionGate — for the "issues" event type, only
// "opened" and "reopened" actions must be accepted; all other action values
// must be discarded (spike F6.1).
func TestGitHubFilter_IssuesActionGate(t *testing.T) {
	conn := newTestGitHubConnector()

	sender := map[string]any{"type": "User", "login": "alice"}

	accept := []string{"opened", "reopened"}
	for _, action := range accept {
		body := buildBody(t, map[string]any{"action": action, "sender": sender})
		decision, err := conn.Filter("issues", body)
		if err != nil {
			t.Errorf("Filter(issues, %q) error = %v; want nil", action, err)
		}
		if decision != FilterAccept {
			t.Errorf("Filter(issues, %q) = %v; want FilterAccept", action, decision)
		}
	}

	discard := []string{"labeled", "assigned", "closed", "edited"}
	for _, action := range discard {
		body := buildBody(t, map[string]any{"action": action, "sender": sender})
		decision, err := conn.Filter("issues", body)
		if err != nil {
			t.Errorf("Filter(issues, %q) error = %v; want nil", action, err)
		}
		if decision != FilterDiscard {
			t.Errorf("Filter(issues, %q) = %v; want FilterDiscard", action, decision)
		}
	}
}

// TestGitHubFilter_PullRequestActionGate — for the "pull_request" event type,
// only "review_requested" must be accepted; "opened", "closed", and
// "synchronize" must be discarded (spike F6.2).
func TestGitHubFilter_PullRequestActionGate(t *testing.T) {
	conn := newTestGitHubConnector()

	sender := map[string]any{"type": "User", "login": "bob"}

	acceptActions := []string{"review_requested"}
	for _, action := range acceptActions {
		body := buildBody(t, map[string]any{"action": action, "sender": sender})
		decision, err := conn.Filter("pull_request", body)
		if err != nil {
			t.Errorf("Filter(pull_request, %q) error = %v; want nil", action, err)
		}
		if decision != FilterAccept {
			t.Errorf("Filter(pull_request, %q) = %v; want FilterAccept", action, decision)
		}
	}

	discardActions := []string{"opened", "closed", "synchronize"}
	for _, action := range discardActions {
		body := buildBody(t, map[string]any{"action": action, "sender": sender})
		decision, err := conn.Filter("pull_request", body)
		if err != nil {
			t.Errorf("Filter(pull_request, %q) error = %v; want nil", action, err)
		}
		if decision != FilterDiscard {
			t.Errorf("Filter(pull_request, %q) = %v; want FilterDiscard", action, decision)
		}
	}
}

// ---------------------------------------------------------------------------
// Map tests
// ---------------------------------------------------------------------------

// TestGitHubMap_IssueOpened — a populated issues payload must produce a
// TicketDraft whose fields are rendered from the route templates, with
// ExternalID set to the string coercion of issue.id and ExternalURL set to
// issue.html_url (FR-102, plan.md §GitHub connector).
//
// issue.id is a JSON integer; the implementation coerces it to string via
// json.Number.String() — this test asserts the coercion is correct.
func TestGitHubMap_IssueOpened(t *testing.T) {
	conn := NewGitHubConnector(GitHubConfig{
		ConnectorID: "github-test",
		Secret:      []byte("secret"),
		Routes: map[string]Route{
			"issues": {
				DepartmentSlug:     "concierge",
				ObjectiveTemplate:  "Issue #{{number}}: {{title}} ({{url}})",
				AcceptanceTemplate: "From {{sender}}: {{body}}",
			},
		},
	})

	issueBody := "Please fix the crash on startup."
	body := buildBody(t, map[string]any{
		"action": "opened",
		"sender": map[string]any{"type": "User", "login": "charlie"},
		"issue": map[string]any{
			"id":       9876543210, // large integer to test numeric coercion
			"title":    "App crashes on startup",
			"body":     issueBody,
			"html_url": "https://github.com/example/repo/issues/42",
			"number":   42,
		},
	})

	draft, err := conn.Map("issues", body)
	if err != nil {
		t.Fatalf("Map(issues) error = %v; want nil", err)
	}

	if draft.DepartmentSlug != "concierge" {
		t.Errorf("DepartmentSlug = %q; want %q", draft.DepartmentSlug, "concierge")
	}

	wantObjective := "Issue #42: App crashes on startup (https://github.com/example/repo/issues/42)"
	if draft.Objective != wantObjective {
		t.Errorf("Objective = %q; want %q", draft.Objective, wantObjective)
	}

	wantAcceptance := "From charlie: " + issueBody
	if draft.Acceptance != wantAcceptance {
		t.Errorf("Acceptance = %q; want %q", draft.Acceptance, wantAcceptance)
	}

	// issue.id = 9876543210 as a JSON number must become the string "9876543210".
	wantExternalID := "9876543210"
	if draft.ExternalID != wantExternalID {
		t.Errorf("ExternalID = %q; want %q (string coercion of issue.id JSON integer)", draft.ExternalID, wantExternalID)
	}

	wantExternalURL := "https://github.com/example/repo/issues/42"
	if draft.ExternalURL != wantExternalURL {
		t.Errorf("ExternalURL = %q; want %q", draft.ExternalURL, wantExternalURL)
	}
}

// TestGitHubMap_NullIssueBody — when issue.body is JSON null, the acceptance
// field must contain the nullBodyFallback literal "(no description provided)"
// and no error must be returned (FR-102, spike QS4).
func TestGitHubMap_NullIssueBody(t *testing.T) {
	conn := NewGitHubConnector(GitHubConfig{
		ConnectorID: "github-test",
		Secret:      []byte("secret"),
		Routes: map[string]Route{
			"issues": {
				DepartmentSlug:     "engineering",
				ObjectiveTemplate:  "{{title}}",
				AcceptanceTemplate: "{{body}}",
			},
		},
	})

	// Construct the payload with an explicit null body field.
	payload := `{"action":"opened","sender":{"type":"User","login":"dave"},"issue":{"id":1,"title":"Null body issue","body":null,"html_url":"https://github.com/example/repo/issues/1","number":1}}`
	body := []byte(payload)

	draft, err := conn.Map("issues", body)
	if err != nil {
		t.Fatalf("Map(issues) with null body error = %v; want nil", err)
	}

	if draft.Acceptance != nullBodyFallback {
		t.Errorf("Acceptance = %q; want %q (null issue body fallback)", draft.Acceptance, nullBodyFallback)
	}
}

// TestGitHubMap_NoRoute — mapping an event type with no configured route must
// return ErrNoMapping (plan.md decision 10, FR-102).
func TestGitHubMap_NoRoute(t *testing.T) {
	// Connector with no "push" route configured.
	conn := NewGitHubConnector(GitHubConfig{
		ConnectorID: "github-test",
		Secret:      []byte("secret"),
		Routes:      map[string]Route{
			// "push" is intentionally absent.
		},
	})

	_, err := conn.Map("push", []byte(`{"action":"push"}`))
	if !errors.Is(err, ErrNoMapping) {
		t.Errorf("Map(push) error = %v; want ErrNoMapping", err)
	}
}

// ---------------------------------------------------------------------------
// Header extraction tests
// ---------------------------------------------------------------------------

// TestGitHubEventType_MissingHeader — when the X-GitHub-Event header is absent
// from the request, EventType must return ok=false (connector.go interface
// contract; the handler treats ok=false as a discard-200).
func TestGitHubEventType_MissingHeader(t *testing.T) {
	conn := newTestGitHubConnector()
	r := httptest.NewRequest(http.MethodPost, "/webhook/github", nil)
	// X-GitHub-Event header deliberately omitted.
	_, ok := conn.EventType(r)
	if ok {
		t.Error("EventType() ok = true; want false when X-GitHub-Event header is absent")
	}
}

// TestGitHubDeliveryID_MissingHeader — when the X-GitHub-Delivery header is
// absent from the request, DeliveryID must return an empty string (connector.go
// interface contract; the handler returns 400 on empty delivery ID).
func TestGitHubDeliveryID_MissingHeader(t *testing.T) {
	conn := newTestGitHubConnector()
	r := httptest.NewRequest(http.MethodPost, "/webhook/github", nil)
	// X-GitHub-Delivery header deliberately omitted.
	got := conn.DeliveryID(r)
	if got != "" {
		t.Errorf("DeliveryID() = %q; want empty string when X-GitHub-Delivery header is absent", got)
	}
}
