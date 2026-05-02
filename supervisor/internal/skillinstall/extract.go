// Package skillinstall is the M7 install actuator. Sequences
// download → digest verify → extract → mount → container_create →
// container_start with per-step audit rows in agent_install_journal
// (FR-214a). On supervisor restart, Resume reads the journal and
// either continues from the last successful step or rolls back any
// partial side effects (deleting partially-extracted skill dirs,
// removing created-but-not-started containers).
//
// This file (extract.go) covers the tar.gz extract surface and the
// path-traversal validation that closes hiring-threat-model Rule 8.
// digest.go covers the sha256 capture + verify pair.
package skillinstall

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxExtractedBytes caps the total bytes the extractor will write
// before bailing — defeats zip-bomb-style payloads that decompress
// to gigabytes. Sized at 100 MiB which is generous for skill
// packages (the spec assumption pins skills under 50 MB; 100 MiB
// gives 2× headroom).
const MaxExtractedBytes int64 = 100 * 1024 * 1024

// Sentinel errors. errors.Is-friendly.
var (
	ErrUnsupportedArchive           = errors.New("skillinstall: unsupported archive format (tar.gz only)")
	ErrArchiveUnsafe                = errors.New("skillinstall: archive contains unsafe paths")
	ErrArchiveTooLarge              = errors.New("skillinstall: archive exceeds MaxExtractedBytes")
	ErrDigestMismatch               = errors.New("skillinstall: digest mismatch")
	ErrInterruptedBySupervisorCrash = errors.New("skillinstall: interrupted by supervisor crash")
)

// SafeExtractTarGz decompresses a tar.gz archive into destDir. Each
// entry's path is validated (no `..`, no absolute, no symlinks
// pointing outside destDir). On any violation the entire extract is
// aborted and partial files are NOT cleaned up — the caller's
// rollback logic is responsible for cleanup (see actuator.go's
// rollback helper).
//
// Spec FR-107 + hiring-threat-model HR-8.
func SafeExtractTarGz(reader io.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("skillinstall: mkdir %s: %w", destDir, err)
	}
	abs, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("skillinstall: abs %s: %w", destDir, err)
	}

	gz, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("%w: not gzip: %v", ErrUnsupportedArchive, err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var written int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: tar header: %v", ErrUnsupportedArchive, err)
		}

		if err := validateEntry(hdr, abs); err != nil {
			return err
		}

		target := filepath.Join(abs, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, fsModeFromTar(hdr.Mode, 0o755)); err != nil {
				return fmt.Errorf("skillinstall: mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("skillinstall: mkdir parent %s: %w", target, err)
			}
			limit := MaxExtractedBytes - written
			n, err := writeFile(tr, target, fsModeFromTar(hdr.Mode, 0o644), limit)
			written += n
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Symlinks are validated by validateEntry above; if we
			// got here, the target is inside destDir. Recreate.
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return fmt.Errorf("skillinstall: symlink %s -> %s: %w", target, hdr.Linkname, err)
			}
		default:
			// Hardlinks, char devices, FIFOs, etc. — never appear in
			// well-formed skill packages; reject the archive.
			return fmt.Errorf("%w: unsupported tar type %c at %s", ErrArchiveUnsafe, hdr.Typeflag, hdr.Name)
		}
	}
}

// validateEntry rejects archive entries with path-traversal shapes:
// absolute paths, paths containing `..`, symlinks whose Linkname
// resolves outside the extract root.
func validateEntry(hdr *tar.Header, absRoot string) error {
	if hdr.Name == "" {
		return fmt.Errorf("%w: empty entry name", ErrArchiveUnsafe)
	}
	if filepath.IsAbs(hdr.Name) {
		return fmt.Errorf("%w: absolute path %q", ErrArchiveUnsafe, hdr.Name)
	}
	clean := filepath.Clean(hdr.Name)
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: path-traversal %q", ErrArchiveUnsafe, hdr.Name)
	}

	// For symlinks, the Linkname must resolve to a path inside the
	// extract root. An absolute Linkname or one whose
	// filepath.Join(parent, Linkname) escapes is rejected.
	if hdr.Typeflag == tar.TypeSymlink {
		if filepath.IsAbs(hdr.Linkname) {
			return fmt.Errorf("%w: absolute symlink target %q -> %q", ErrArchiveUnsafe, hdr.Name, hdr.Linkname)
		}
		// Compute the symlink's resolved location: the directory
		// containing the symlink + the link target.
		linkParent := filepath.Dir(filepath.Join(absRoot, hdr.Name))
		resolved := filepath.Clean(filepath.Join(linkParent, hdr.Linkname))
		if !strings.HasPrefix(resolved, absRoot+string(filepath.Separator)) && resolved != absRoot {
			return fmt.Errorf("%w: symlink %q -> %q escapes extract root", ErrArchiveUnsafe, hdr.Name, hdr.Linkname)
		}
	}
	return nil
}

// writeFile copies tr into target, capped at limit bytes. Returns
// the bytes written + an error if the cap was exceeded.
func writeFile(tr io.Reader, target string, mode os.FileMode, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("%w: extracted >100 MiB", ErrArchiveTooLarge)
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("skillinstall: create %s: %w", target, err)
	}
	defer f.Close()
	// LimitReader ensures we never write more than the remaining budget.
	n, err := io.Copy(f, io.LimitReader(tr, limit+1))
	if err != nil {
		return n, fmt.Errorf("skillinstall: write %s: %w", target, err)
	}
	if n > limit {
		_ = os.Remove(target)
		return n, fmt.Errorf("%w: archive contains >100 MiB total", ErrArchiveTooLarge)
	}
	return n, nil
}

// fsModeFromTar converts a tar mode (POSIX with extra bits) to
// os.FileMode, falling back to def for zero or unrepresentable
// values.
func fsModeFromTar(m int64, def os.FileMode) os.FileMode {
	if m <= 0 {
		return def
	}
	return os.FileMode(m & 0o777)
}
