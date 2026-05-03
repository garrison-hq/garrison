package skillinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
	"github.com/garrison-hq/garrison/supervisor/internal/skillregistry"
	"github.com/jackc/pgx/v5/pgtype"
)

// SkillRef identifies one skill at a (registry, package, version)
// triple plus the propose-time digest the actuator must verify
// against (HR-7).
type SkillRef struct {
	Registry        string // matches Registry.Name(): "skills.sh" | "skillhub"
	Package         string
	Version         string
	DigestAtPropose string // hex sha256
}

// Actuator orchestrates the 6-step install pipeline. One Actuator
// per supervisor process; safe for concurrent Install calls
// targeting different proposal IDs (each call is independent).
type Actuator struct {
	Registries  map[string]skillregistry.Registry
	Container   agentcontainer.Controller
	Journaler   *Journaler
	SkillsDir   string // /var/lib/garrison/skills
	Logger      *slog.Logger
	BuildSpec   ContainerSpecBuilder
	OnInstalled func(ctx context.Context, proposalID pgtype.UUID, agentID pgtype.UUID, containerID string) error
}

// ContainerSpecBuilder lets the caller customise the per-agent
// container spec at install time. The actuator passes proposalID +
// targetAgentID + the post-mount skills dir; the builder returns
// the final spec.
type ContainerSpecBuilder func(ctx context.Context, proposalID, agentID pgtype.UUID, skillsDir string) (agentcontainer.ContainerSpec, error)

// Install runs the 6-step pipeline end-to-end. Each step writes a
// success/failed/interrupted row to agent_install_journal; on any
// failure, partial side-effects are rolled back and the proposal
// is marked install_failed by the caller (we don't write that
// status here — the caller's Server Action transaction already
// owns the proposals row's status).
func (a *Actuator) Install(ctx context.Context, proposalID, agentID pgtype.UUID, skill SkillRef) error {
	a.Logger.Info("install: start",
		"proposal_id", uuidString(proposalID),
		"agent_id", uuidString(agentID),
		"registry", skill.Registry, "package", skill.Package, "version", skill.Version)

	// Step 1: download
	body, err := a.stepDownload(ctx, proposalID, skill)
	if err != nil {
		return err
	}
	// Step 2: verify_digest
	if err := a.stepVerifyDigest(ctx, proposalID, body, skill.DigestAtPropose); err != nil {
		return err
	}
	// Step 3: extract
	skillsDir, err := a.stepExtract(ctx, proposalID, agentID, skill, body)
	if err != nil {
		return err
	}
	// Step 4: mount (atomic write-then-rename happened in extract;
	// the "mount" step here is symbolic + records the final path).
	if err := a.stepMount(ctx, proposalID, skillsDir); err != nil {
		return err
	}
	// Step 5: container_create
	containerID, err := a.stepContainerCreate(ctx, proposalID, agentID, skillsDir)
	if err != nil {
		return err
	}
	// Step 6: container_start
	if err := a.stepContainerStart(ctx, proposalID, containerID); err != nil {
		return err
	}
	if a.OnInstalled != nil {
		if err := a.OnInstalled(ctx, proposalID, agentID, containerID); err != nil {
			a.Logger.Warn("install: post-install hook failed",
				"proposal_id", uuidString(proposalID), "err", err)
		}
	}
	a.Logger.Info("install: complete",
		"proposal_id", uuidString(proposalID),
		"agent_id", uuidString(agentID),
		"container_id", containerID)
	return nil
}

func (a *Actuator) stepDownload(ctx context.Context, proposalID pgtype.UUID, skill SkillRef) ([]byte, error) {
	reg, ok := a.Registries[skill.Registry]
	if !ok {
		return nil, a.recordFailed(ctx, proposalID, StepDownload, "unknown_registry",
			fmt.Errorf("unknown registry %q (configured: %v)", skill.Registry, mapKeys(a.Registries)))
	}
	body, sha, err := reg.Fetch(ctx, skill.Package, skill.Version)
	if err != nil {
		return nil, a.recordFailed(ctx, proposalID, StepDownload, errorKindFromRegistryErr(err), err)
	}
	if recErr := a.Journaler.RecordStep(ctx, proposalID, StepDownload, OutcomeSuccess, "",
		map[string]any{"sha256_actual": sha, "size_bytes": len(body)}); recErr != nil {
		return nil, recErr
	}
	return body, nil
}

func (a *Actuator) stepVerifyDigest(ctx context.Context, proposalID pgtype.UUID, body []byte, expected string) error {
	if err := VerifyDigest(body, expected); err != nil {
		return a.recordFailed(ctx, proposalID, StepVerifyDigest, "digest_mismatch", err)
	}
	return a.Journaler.RecordStep(ctx, proposalID, StepVerifyDigest, OutcomeSuccess, "", nil)
}

func (a *Actuator) stepExtract(ctx context.Context, proposalID, agentID pgtype.UUID, skill SkillRef, body []byte) (string, error) {
	skillsDir := filepath.Join(a.SkillsDir, uuidString(agentID), skill.Package)
	tmpDir := skillsDir + ".tmp"
	// Wipe stale tmp from a prior interrupted run.
	_ = os.RemoveAll(tmpDir)

	if err := SafeExtractTarGz(bytes.NewReader(body), tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", a.recordFailed(ctx, proposalID, StepExtract, errorKindFromExtractErr(err), err)
	}
	if err := a.Journaler.RecordStep(ctx, proposalID, StepExtract, OutcomeSuccess, "",
		map[string]any{"tmp_dir": tmpDir}); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", err
	}
	return tmpDir, nil
}

func (a *Actuator) stepMount(ctx context.Context, proposalID pgtype.UUID, tmpDir string) error {
	finalDir := tmpDir[:len(tmpDir)-len(".tmp")]
	// Atomic rename: tmpDir → finalDir. If finalDir already exists
	// (a previous successful install of an older version), wipe it
	// first — version-bump replacement is intentional.
	if _, err := os.Stat(finalDir); err == nil {
		if err := os.RemoveAll(finalDir); err != nil {
			return a.recordFailed(ctx, proposalID, StepMount, "remove_old_failed", err)
		}
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return a.recordFailed(ctx, proposalID, StepMount, "rename_failed", err)
	}
	return a.Journaler.RecordStep(ctx, proposalID, StepMount, OutcomeSuccess, "",
		map[string]any{"mounted_at": finalDir})
}

func (a *Actuator) stepContainerCreate(ctx context.Context, proposalID, agentID pgtype.UUID, skillsDir string) (string, error) {
	if a.BuildSpec == nil {
		return "", a.recordFailed(ctx, proposalID, StepContainerCreate, "no_spec_builder",
			errors.New("Actuator.BuildSpec not configured"))
	}
	spec, err := a.BuildSpec(ctx, proposalID, agentID, skillsDir)
	if err != nil {
		return "", a.recordFailed(ctx, proposalID, StepContainerCreate, "build_spec_failed", err)
	}
	containerID, err := a.Container.Create(ctx, spec)
	if err != nil {
		return "", a.recordFailed(ctx, proposalID, StepContainerCreate, "create_failed", err)
	}
	if err := a.Journaler.RecordStep(ctx, proposalID, StepContainerCreate, OutcomeSuccess, "",
		map[string]any{"container_id": containerID, "image": spec.Image}); err != nil {
		return "", err
	}
	return containerID, nil
}

func (a *Actuator) stepContainerStart(ctx context.Context, proposalID pgtype.UUID, containerID string) error {
	if err := a.Container.Start(ctx, containerID); err != nil {
		return a.recordFailed(ctx, proposalID, StepContainerStart, "start_failed", err)
	}
	return a.Journaler.RecordStep(ctx, proposalID, StepContainerStart, OutcomeSuccess, "",
		map[string]any{"container_id": containerID})
}

// recordFailed writes a 'failed' journal row + returns the wrapped
// error. Used by every step's error path to keep the
// audit-write-then-return shape one-line per call site.
func (a *Actuator) recordFailed(ctx context.Context, proposalID pgtype.UUID, step Step, errKind string, cause error) error {
	a.Logger.Warn("install: step failed",
		"proposal_id", uuidString(proposalID),
		"step", string(step),
		"error_kind", errKind,
		"err", cause)
	if recErr := a.Journaler.RecordStep(ctx, proposalID, step, OutcomeFailed, errKind,
		map[string]any{"err": cause.Error()}); recErr != nil {
		// Log but return the original cause — the audit failure is
		// secondary to the install failure.
		a.Logger.Error("install: journal write failed",
			"proposal_id", uuidString(proposalID), "step", string(step), "err", recErr)
	}
	return cause
}

func errorKindFromRegistryErr(err error) string {
	switch {
	case errors.Is(err, skillregistry.ErrPackageNotFound):
		return "package_not_found"
	case errors.Is(err, skillregistry.ErrRegistryAuthFailed):
		return "registry_auth_failed"
	case errors.Is(err, skillregistry.ErrRegistryRateLimited):
		return "registry_rate_limited"
	case errors.Is(err, skillregistry.ErrRegistryServerError):
		return "registry_server_error"
	default:
		return "registry_unreachable"
	}
}

func errorKindFromExtractErr(err error) string {
	switch {
	case errors.Is(err, ErrUnsupportedArchive):
		return "unsupported_archive_format"
	case errors.Is(err, ErrArchiveUnsafe):
		return "archive_unsafe"
	case errors.Is(err, ErrArchiveTooLarge):
		return "archive_too_large"
	default:
		return "extract_failed"
	}
}

func mapKeys(m map[string]skillregistry.Registry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}
