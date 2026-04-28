package mempalace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/dockerexec"
)

// BootstrapConfig wires Bootstrap's collaborators and tunables. DockerBin
// is optional (empty means "docker" via $PATH); MempalaceContainer is the
// sidecar's container name; PalacePath is the path inside the sidecar
// where the palace volume is mounted (Compose default: /palace). Logger
// and InitTimeout are both optional (defaults applied inside Bootstrap).
type BootstrapConfig struct {
	DockerBin          string
	MempalaceContainer string
	PalacePath         string
	Logger             *slog.Logger
	InitTimeout        time.Duration         // default 30s
	Exec               dockerexec.DockerExec // injection seam; nil → dockerexec.RealDockerExec{DockerBin}
}

// ErrPalaceInitFailed wraps a non-zero exit from `mempalace init --yes`.
// Callers that need to distinguish init failure from other errors (e.g.
// docker daemon unreachable) inspect errors.Is(err, ErrPalaceInitFailed).
var ErrPalaceInitFailed = errors.New("mempalace: init failed")

// Bootstrap runs `mempalace init --yes <palace_path>` unconditionally
// through docker exec against the sidecar container. Per T001 finding F1,
// `mempalace init --yes` is idempotent in 3.3.2 — a second call against
// an already-initialized palace exits 0 and re-writes the identical
// `mempalace.yaml`. The supervisor therefore does not need a detect-first
// marker-file check; unconditional invocation is simpler and correct.
//
// Returns nil on successful init. Returns a wrapped error on any failure
// mode: docker command not found, container not reachable, mempalace
// binary missing in the container, permission denied on the palace path,
// or the init itself returning non-zero. The error message names the
// palace path and the stderr snippet so operators can diagnose quickly.
func Bootstrap(ctx context.Context, cfg BootstrapConfig) error {
	if cfg.MempalaceContainer == "" {
		return errors.New("mempalace.Bootstrap: MempalaceContainer is empty")
	}
	if cfg.PalacePath == "" {
		return errors.New("mempalace.Bootstrap: PalacePath is empty")
	}
	timeout := cfg.InitTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	exe := cfg.Exec
	if exe == nil {
		exe = dockerexec.RealDockerExec{DockerBin: cfg.DockerBin}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	initCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"exec", cfg.MempalaceContainer,
		"mempalace", "init", "--yes", cfg.PalacePath,
	}
	_, stderr, err := exe.Run(initCtx, args, nil)
	if err != nil {
		return fmt.Errorf("%w: path=%s: %v: stderr=%s",
			ErrPalaceInitFailed, cfg.PalacePath, err, snippet(stderr))
	}
	logger.Info("palace_initialized",
		"palace_initialized", true,
		"container", cfg.MempalaceContainer,
		"path", cfg.PalacePath,
	)
	return nil
}

// snippet returns at most the first 200 bytes of b as a string, trimming
// trailing newlines. Used to keep error messages small when `mempalace
// init` produces verbose output.
func snippet(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	s := string(b)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
