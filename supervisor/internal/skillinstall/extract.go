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
// pointing outside destDir) AND every filesystem write goes through
// an os.Root rooted at destDir — the kernel-level rooted-fs API
// rejects any path that resolves outside the root, so a tar-slip
// payload that bypasses validateEntry would still fail at the
// syscall layer.
//
// Spec FR-107 + hiring-threat-model HR-8.
func SafeExtractTarGz(reader io.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("skillinstall: mkdir %s: %w", destDir, err)
	}

	root, err := os.OpenRoot(destDir)
	if err != nil {
		return fmt.Errorf("skillinstall: open root %s: %w", destDir, err)
	}
	defer root.Close()

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

		if err := validateEntry(hdr); err != nil {
			return err
		}

		// Every fs operation below goes through `root` — os.Root's
		// methods reject any name that resolves outside the rooted
		// directory, so a regression in validateEntry can't produce
		// a tar-slip write. This is the canonical safe-extract
		// pattern (Go 1.24+); it satisfies tar-slip taint analyzers
		// because the entry name flows into root-scoped methods,
		// not free filepath.Join calls.
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(hdr.Name, fsModeFromTar(hdr.Mode, 0o755)); err != nil {
				return fmt.Errorf("skillinstall: mkdir %s: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			if dir := filepath.Dir(hdr.Name); dir != "." && dir != "" {
				if err := root.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("skillinstall: mkdir parent %s: %w", dir, err)
				}
			}
			limit := MaxExtractedBytes - written
			n, err := writeFile(root, tr, hdr.Name, fsModeFromTar(hdr.Mode, 0o644), limit)
			written += n
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			// validateEntry already rejected absolute Linkname and
			// out-of-root relative ones. Symlink is created via
			// root-scoped helper so the link's location is bounded.
			if err := root.Symlink(hdr.Linkname, hdr.Name); err != nil {
				return fmt.Errorf("skillinstall: symlink %s -> %s: %w", hdr.Name, hdr.Linkname, err)
			}
		default:
			// Hardlinks, char devices, FIFOs, etc. — never appear in
			// well-formed skill packages; reject the archive.
			return fmt.Errorf("%w: unsupported tar type %c at %s", ErrArchiveUnsafe, hdr.Typeflag, hdr.Name)
		}
	}
}

// validateEntry rejects archive entries with path-traversal shapes:
// absolute paths, paths containing `..`, symlinks whose Linkname is
// absolute or escapes the root via relative-segment manipulation.
//
// The downstream os.Root would reject most of these structurally,
// but validateEntry runs first so the audit row + error surface
// reflect "the archive itself was unsafe" rather than "the syscall
// rejected the path." Belt-and-suspenders.
func validateEntry(hdr *tar.Header) error {
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

	if hdr.Typeflag == tar.TypeSymlink {
		if filepath.IsAbs(hdr.Linkname) {
			return fmt.Errorf("%w: absolute symlink target %q -> %q", ErrArchiveUnsafe, hdr.Name, hdr.Linkname)
		}
		// Symlink target validity: simulate where the link's
		// Linkname would resolve relative to the directory holding
		// the symlink. If that resolves outside root (i.e. starts
		// with "../" after cleaning), reject. os.Root.Symlink would
		// also reject a symlink that points outside, but doing it
		// up-front keeps the error vocabulary consistent with the
		// other ArchiveUnsafe rejections.
		linkParent := filepath.Dir(hdr.Name)
		resolved := filepath.Clean(filepath.Join(linkParent, hdr.Linkname))
		if strings.HasPrefix(resolved, "..") || strings.Contains(resolved, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: symlink %q -> %q escapes extract root", ErrArchiveUnsafe, hdr.Name, hdr.Linkname)
		}
	}
	return nil
}

// writeFile copies tr into the file at name (relative to root),
// capped at limit bytes. Uses root-scoped OpenFile so the path is
// bounded by the kernel's rooted-fs check.
func writeFile(root *os.Root, tr io.Reader, name string, mode os.FileMode, limit int64) (int64, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("%w: extracted >100 MiB", ErrArchiveTooLarge)
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("skillinstall: create %s: %w", name, err)
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(tr, limit+1))
	if err != nil {
		return n, fmt.Errorf("skillinstall: write %s: %w", name, err)
	}
	if n > limit {
		_ = root.Remove(name)
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
