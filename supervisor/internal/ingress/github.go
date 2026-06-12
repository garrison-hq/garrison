package ingress

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// GitHubConfig is the deploy-time configuration for the GitHub connector
// (plan.md decision 9, 10; FR-701). The webhook secret is held as raw bytes
// (fetched from vault at boot — never logged, never in agent context; FR-302,
// AGENTS.md §vault). Routes maps GitHub event type strings (e.g. "issues",
// "pull_request") to per-event routing + render templates.
type GitHubConfig struct {
	// ConnectorID is the stable connector identity string recorded in
	// ingress_deliveries.connector_id and tickets.metadata.ingress_connector.
	// Default: "github-sortie" (DefaultIngressGitHubConnectorID from config).
	ConnectorID string

	// Secret is the raw HMAC-SHA256 webhook secret fetched from vault path
	// ingress/github/webhook_secret. Held at connector construction, not
	// re-fetched per request (M2.3 fetch-at-boot precedent).
	Secret []byte

	// Routes maps GitHub event type to its routing + render configuration.
	// M10-core subscribes to "issues" and "pull_request" (SR5).
	Routes map[string]Route

	// RatePerMin is the token-bucket refill rate (tokens per minute) for
	// this connector (plan.md decision 8). Default: 60.
	RatePerMin int

	// Burst is the token-bucket initial capacity (plan.md decision 8).
	// Default: 30.
	Burst int
}

// Route is the per-event-type routing and render configuration (plan.md
// decision 10; FR-102). Each GitHub event type maps to a single Garrison
// department and a pair of bounded ReplaceAll templates.
type Route struct {
	// DepartmentSlug is the Garrison department the created ticket lands in.
	DepartmentSlug string
	// ObjectiveTemplate is the ticket objective rendered from named payload
	// fields via renderTemplate (bounded substitution: {{title}}, {{url}},
	// {{body}}, {{number}}, {{sender}}).
	ObjectiveTemplate string
	// AcceptanceTemplate is the ticket acceptance-criteria rendered from the
	// same bounded variable set.
	AcceptanceTemplate string
}

// GitHubConnector implements the Connector interface for GitHub repository/org
// webhooks authenticated with a pre-shared HMAC-SHA256 secret (SR3; plan.md
// decision 2, 9). It is the only Connector implementation in M10-core.
type GitHubConnector struct {
	cfg GitHubConfig
}

// NewGitHubConnector constructs a GitHubConnector from its deploy-time config.
// The caller is responsible for fetching the vault secret before construction
// and populating cfg.Secret (FR-302, plan.md decision 12).
func NewGitHubConnector(cfg GitHubConfig) *GitHubConnector {
	return &GitHubConnector{cfg: cfg}
}

// ID returns the stable connector identity (ingress_deliveries.connector_id,
// tickets.metadata.ingress_connector). Satisfies Connector.ID.
func (g *GitHubConnector) ID() string {
	return g.cfg.ConnectorID
}

// EventType extracts the X-GitHub-Event header value. ok=false when the
// header is absent (the handler treats that as discard-200). Satisfies
// Connector.EventType.
func (g *GitHubConnector) EventType(r *http.Request) (string, bool) {
	v := r.Header.Get("X-GitHub-Event")
	if v == "" {
		return "", false
	}
	return v, true
}

// Subscribed reports whether eventType is handled by this connector. The
// subscribed set for M10-core is {"issues", "pull_request", "ping"} (SR5).
// ping is subscribed so it reaches Filter, which discards it cleanly
// (FR-403, SR9). Satisfies Connector.Subscribed.
func (g *GitHubConnector) Subscribed(eventType string) bool {
	switch eventType {
	case "issues", "pull_request", "ping":
		return true
	}
	return false
}

// DeliveryID returns the X-GitHub-Delivery header, which GitHub guarantees is
// stable across manual redeliveries (SR2). An empty return means the header is
// absent; the handler returns 400. Satisfies Connector.DeliveryID.
func (g *GitHubConnector) DeliveryID(r *http.Request) string {
	return r.Header.Get("X-GitHub-Delivery")
}

// VerifySignature checks the HMAC-SHA256 signature in X-Hub-Signature-256 over
// rawBody using the connector's vault-fetched secret (FR-300, SR1). Delegates to
// the package-private verifyGitHubSignature helper. Satisfies Connector.VerifySignature.
func (g *GitHubConnector) VerifySignature(rawBody []byte, r *http.Request, secret []byte) error {
	return verifyGitHubSignature(rawBody, r.Header.Get("X-Hub-Signature-256"), secret)
}

// gitHubEnvelope is the minimal payload shape the Filter and Map methods parse.
// Only the fields actually read by the connector are declared; unknown fields
// are silently ignored by encoding/json (no new dependency, SR4).
type gitHubEnvelope struct {
	Action string `json:"action"`
	Sender struct {
		Type  string `json:"type"`
		Login string `json:"login"`
	} `json:"sender"`
	Issue *struct {
		ID      json.Number `json:"id"`
		Title   string      `json:"title"`
		Body    *string     `json:"body"`
		HTMLURL string      `json:"html_url"`
		Number  int         `json:"number"`
	} `json:"issue"`
	PullRequest *struct {
		ID      json.Number `json:"id"`
		Title   string      `json:"title"`
		Body    *string     `json:"body"`
		HTMLURL string      `json:"html_url"`
		Number  int         `json:"number"`
	} `json:"pull_request"`
}

// Filter applies the SR6 step 3–4 noise rules in binding order (FR-401):
//
//  1. ping → FilterDiscard (FR-403, SR9): GitHub sends ping on registration;
//     returning 200 without a ticket lets registration succeed.
//  2. sender.type == "Bot" or sender.login ending "[bot]" → FilterDiscard
//     (FR-401, spike F7): automated bot traffic is mechanical noise.
//  3. issues action not in {"opened", "reopened"} → FilterDiscard (spike F6.1).
//  4. pull_request action not "review_requested" → FilterDiscard (spike F6.2).
//  5. All others → FilterAccept.
//
// A JSON parse error returns FilterDiscard and the error (the handler logs it
// and returns 200 — an unparseable payload after signature verification should
// not 500, and a 200-with-discard is the safe default for mechanical noise).
// Satisfies Connector.Filter.
func (g *GitHubConnector) Filter(eventType string, body []byte) (FilterDecision, error) {
	// Rule 1: ping is always discarded regardless of body content (FR-403, SR9).
	if eventType == "ping" {
		return FilterDiscard, nil
	}

	var env gitHubEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return FilterDiscard, err
	}

	// Rule 2: bot-sender filter (FR-401, spike F7).
	// Matches sender.type == "Bot" (GitHub API canonical value) or
	// sender.login ending with "[bot]" (GitHub Apps convention).
	if env.Sender.Type == "Bot" || strings.HasSuffix(env.Sender.Login, "[bot]") {
		return FilterDiscard, nil
	}

	// Rule 3: issues action gate — accept only "opened" and "reopened" (spike F6.1).
	if eventType == "issues" {
		switch env.Action {
		case "opened", "reopened":
			return FilterAccept, nil
		default:
			return FilterDiscard, nil
		}
	}

	// Rule 4: pull_request action gate — accept only "review_requested" (spike F6.2).
	if eventType == "pull_request" {
		if env.Action == "review_requested" {
			return FilterAccept, nil
		}
		return FilterDiscard, nil
	}

	// Rule 5: any subscribed event type not handled above (shouldn't happen
	// in M10-core since the subscribed set is exactly {issues, pull_request,
	// ping}, but be safe).
	return FilterAccept, nil
}

// Map renders an accepted event into a TicketDraft using the connector's
// configured Routes + bounded renderTemplate substitution (FR-102, plan.md
// decision 10). It returns ErrNoMapping when eventType has no configured
// route. Satisfies Connector.Map.
//
// issue.id and pull_request.id are JSON integers; they are coerced to string
// via json.Number.String() before assignment to ExternalID — assigning an int64
// directly would be a compile error (plan.md §GitHub connector, tasks.md T005
// completion condition note).
func (g *GitHubConnector) Map(eventType string, body []byte) (TicketDraft, error) {
	route, ok := g.cfg.Routes[eventType]
	if !ok {
		return TicketDraft{}, ErrNoMapping
	}

	var env gitHubEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return TicketDraft{}, err
	}

	switch eventType {
	case "issues":
		if env.Issue == nil {
			return TicketDraft{}, ErrMalformedDelivery
		}
		bodyText := ""
		if env.Issue.Body != nil {
			bodyText = *env.Issue.Body
		}
		vars := map[string]string{
			"title":  env.Issue.Title,
			"url":    env.Issue.HTMLURL,
			"body":   bodyText,
			"number": strconv.Itoa(env.Issue.Number),
			"sender": env.Sender.Login,
		}
		return TicketDraft{
			DepartmentSlug: route.DepartmentSlug,
			Objective:      renderTemplate(route.ObjectiveTemplate, vars),
			Acceptance:     renderTemplate(route.AcceptanceTemplate, vars),
			ExternalID:     env.Issue.ID.String(),
			ExternalURL:    env.Issue.HTMLURL,
		}, nil

	case "pull_request":
		if env.PullRequest == nil {
			return TicketDraft{}, ErrMalformedDelivery
		}
		bodyText := ""
		if env.PullRequest.Body != nil {
			bodyText = *env.PullRequest.Body
		}
		vars := map[string]string{
			"title":  env.PullRequest.Title,
			"url":    env.PullRequest.HTMLURL,
			"body":   bodyText,
			"number": strconv.Itoa(env.PullRequest.Number),
			"sender": env.Sender.Login,
		}
		return TicketDraft{
			DepartmentSlug: route.DepartmentSlug,
			Objective:      renderTemplate(route.ObjectiveTemplate, vars),
			Acceptance:     renderTemplate(route.AcceptanceTemplate, vars),
			ExternalID:     env.PullRequest.ID.String(),
			ExternalURL:    env.PullRequest.HTMLURL,
		}, nil

	default:
		return TicketDraft{}, ErrNoMapping
	}
}
