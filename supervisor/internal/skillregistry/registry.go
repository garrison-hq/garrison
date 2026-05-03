// Package skillregistry contains HTTPS clients for the two skill
// registries Garrison consumes at M7: skills.sh (the public feed) and
// SkillHub (self-hosted, github.com/iflytek/skillhub, deployed on
// Garrison's own Hetzner host alongside the supervisor).
//
// Both registries serve tar.gz packages keyed by (package, version).
// This package owns the HTTP boundary; the install actuator
// (internal/skillinstall) sequences fetch → digest verify → extract →
// mount on top.
//
// Registries live behind the Registry interface so the actuator is
// registry-agnostic — adding a third registry is a new client + a
// registration line in cmd/supervisor/main.go's Deps construction.
//
// Endpoints are pinned via supervisor config (GARRISON_SKILLS_SH_URL,
// GARRISON_SKILLHUB_URL) so both can be redirected at deploy time.
// The skills.sh URL has a public default; the SkillHub URL has a
// loopback default suitable for the dev-stack and is overridden in
// production to the in-cluster service hostname.
package skillregistry

import (
	"context"
	"errors"
)

// Registry is the consumer-side surface every M7 skill registry
// satisfies. Both clients return raw bytes + a computed sha256; the
// caller compares the computed hash against the propose-time digest
// (HR-7) before trusting the bytes.
type Registry interface {
	// Name returns a stable identifier ("skills.sh", "skillhub") used
	// in agents.skills[].registry and chat_mutation_audit payloads.
	Name() string

	// Fetch downloads the (pkg, version) tarball. Returns the raw
	// body bytes plus the computed SHA-256 (hex). The caller is
	// responsible for digest comparison; this method does not error
	// on a digest mismatch — it reports what it received.
	Fetch(ctx context.Context, pkg, version string) (body []byte, sha256Hex string, err error)

	// Describe returns operator-facing metadata for the dashboard
	// approval surface (FR-106a, FR-108). Implementations may serve
	// this from a separate endpoint or compute it from the tarball.
	Describe(ctx context.Context, pkg, version string) (Metadata, error)
}

// Metadata is the operator-readable surface displayed in the approval
// UX. Author + Description are best-effort (registry-dependent);
// SHA-256 is always populated and is what the digest pin (HR-7) tracks.
type Metadata struct {
	Package     string
	Version     string
	Author      string
	Description string
	SHA256      string // hex; matches the body's actual hash at Describe-time
}

// Sentinel errors. Wrapped via fmt.Errorf("%w: …") at call sites so
// the actuator can route on errors.Is.
var (
	// ErrRegistryUnreachable surfaces when the registry's HTTPS
	// endpoint is not reachable (DNS failure, TLS handshake failure,
	// connection refused). Operator action: check network /
	// configuration; SkillHub instance health.
	ErrRegistryUnreachable = errors.New("registry: unreachable")

	// ErrPackageNotFound surfaces on HTTP 404. Operator action: check
	// the proposal's package + version strings.
	ErrPackageNotFound = errors.New("registry: package not found")

	// ErrRegistryAuthFailed surfaces on HTTP 401/403. Operator action:
	// rotate the SkillHub admin token (skills.sh is anonymous so this
	// shape only fires against SkillHub).
	ErrRegistryAuthFailed = errors.New("registry: auth failed")

	// ErrRegistryRateLimited surfaces on HTTP 429 after the retry-
	// after budget is exhausted. Operator action: wait, retry the
	// install via the dashboard's retry button.
	ErrRegistryRateLimited = errors.New("registry: rate limited")

	// ErrRegistryServerError surfaces on HTTP 5xx. Distinct from
	// Unreachable so the operator can distinguish "they're down"
	// from "we can't reach them".
	ErrRegistryServerError = errors.New("registry: server error")
)
