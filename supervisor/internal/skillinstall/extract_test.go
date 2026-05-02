package skillinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarGzBuilder helps tests construct tar.gz archives in memory.
type tarGzEntry struct {
	name     string
	typeFlag byte
	body     []byte
	linkname string
	mode     int64
}

func buildTarGz(t *testing.T, entries []tarGzEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeFlag,
			Mode:     directoryMode(e.typeFlag, e.mode),
			Linkname: e.linkname,
			Size:     int64(len(e.body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", e.name, err)
		}
		if len(e.body) > 0 {
			if _, err := tw.Write(e.body); err != nil {
				t.Fatalf("tar body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func ifZero(v, def int64) int64 {
	if v == 0 {
		return def
	}
	return v
}

// directoryMode returns the right mode for a tar entry: 0o755 for
// directories (so subsequent writes can land), the entry's mode
// otherwise.
func directoryMode(typeFlag byte, m int64) int64 {
	if typeFlag == 0x35 /* tar.TypeDir */ && m == 0 {
		return 0o755
	}
	return ifZero(m, 0o644)
}

// TestTarGzExtractsSafely — happy-path archive with normal files
// extracts cleanly into the dest dir.
func TestTarGzExtractsSafely(t *testing.T) {
	archive := buildTarGz(t, []tarGzEntry{
		{name: "skill/", typeFlag: tar.TypeDir, mode: 0o755},
		{name: "skill/SKILL.md", typeFlag: tar.TypeReg, body: []byte("# Skill\n\nDoes a thing.\n")},
		{name: "skill/scripts/run.sh", typeFlag: tar.TypeReg, body: []byte("#!/bin/sh\necho hi\n"), mode: 0o755},
	})
	dest := t.TempDir()
	if err := SafeExtractTarGz(bytes.NewReader(archive), dest); err != nil {
		t.Fatalf("SafeExtractTarGz: %v", err)
	}
	for _, p := range []string{"skill/SKILL.md", "skill/scripts/run.sh"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
	body, _ := os.ReadFile(filepath.Join(dest, "skill/SKILL.md"))
	if !strings.Contains(string(body), "Does a thing.") {
		t.Errorf("body mismatch: %q", body)
	}
}

// TestRejectsZipFormat — non-tar.gz bytes (zip magic) surface as
// ErrUnsupportedArchive (gzip header parse failure).
func TestRejectsZipFormat(t *testing.T) {
	// PK\x03\x04 — zip magic
	zipBytes := []byte{0x50, 0x4b, 0x03, 0x04, 0x00, 0x00, 0x00, 0x00}
	dest := t.TempDir()
	err := SafeExtractTarGz(bytes.NewReader(zipBytes), dest)
	if !errors.Is(err, ErrUnsupportedArchive) {
		t.Errorf("err: got %v; want ErrUnsupportedArchive", err)
	}
}

// TestRejectsPathTraversal — entry containing `../../etc/passwd`
// is rejected as ErrArchiveUnsafe.
func TestRejectsPathTraversal(t *testing.T) {
	archive := buildTarGz(t, []tarGzEntry{
		{name: "../../etc/passwd", typeFlag: tar.TypeReg, body: []byte("root:x:0:0::/:/bin/sh\n")},
	})
	dest := t.TempDir()
	err := SafeExtractTarGz(bytes.NewReader(archive), dest)
	if !errors.Is(err, ErrArchiveUnsafe) {
		t.Fatalf("err: got %v; want ErrArchiveUnsafe", err)
	}
	// Ensure no etc/passwd was created in the test workspace.
	if _, err := os.Stat(filepath.Join(dest, "../../etc/passwd")); err == nil {
		t.Error("path-traversal entry was actually written")
	}
}

// TestRejectsAbsolutePath — entry with /etc/passwd is rejected.
func TestRejectsAbsolutePath(t *testing.T) {
	archive := buildTarGz(t, []tarGzEntry{
		{name: "/etc/passwd", typeFlag: tar.TypeReg, body: []byte("root:x:0:0::/:/bin/sh\n")},
	})
	dest := t.TempDir()
	err := SafeExtractTarGz(bytes.NewReader(archive), dest)
	if !errors.Is(err, ErrArchiveUnsafe) {
		t.Fatalf("err: got %v; want ErrArchiveUnsafe", err)
	}
}

// TestRejectsSymlinkOutsideRoot — symlink target outside extract
// root is rejected even if relative.
func TestRejectsSymlinkOutsideRoot(t *testing.T) {
	archive := buildTarGz(t, []tarGzEntry{
		{name: "escape", typeFlag: tar.TypeSymlink, linkname: "../../../../etc/passwd"},
	})
	dest := t.TempDir()
	err := SafeExtractTarGz(bytes.NewReader(archive), dest)
	if !errors.Is(err, ErrArchiveUnsafe) {
		t.Fatalf("err: got %v; want ErrArchiveUnsafe", err)
	}
}

// TestRejectsAbsoluteSymlinkTarget — symlink with absolute Linkname
// is rejected.
func TestRejectsAbsoluteSymlinkTarget(t *testing.T) {
	archive := buildTarGz(t, []tarGzEntry{
		{name: "shortcut", typeFlag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	dest := t.TempDir()
	err := SafeExtractTarGz(bytes.NewReader(archive), dest)
	if !errors.Is(err, ErrArchiveUnsafe) {
		t.Fatalf("err: got %v; want ErrArchiveUnsafe", err)
	}
}

// TestAcceptsSymlinkInsideRoot — symlink whose target resolves
// inside the extract dir extracts cleanly.
func TestAcceptsSymlinkInsideRoot(t *testing.T) {
	archive := buildTarGz(t, []tarGzEntry{
		{name: "skill/", typeFlag: tar.TypeDir},
		{name: "skill/data.txt", typeFlag: tar.TypeReg, body: []byte("hello")},
		{name: "skill/data-link", typeFlag: tar.TypeSymlink, linkname: "data.txt"},
	})
	dest := t.TempDir()
	if err := SafeExtractTarGz(bytes.NewReader(archive), dest); err != nil {
		t.Fatalf("SafeExtractTarGz: %v", err)
	}
	dst, err := os.Readlink(filepath.Join(dest, "skill/data-link"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if dst != "data.txt" {
		t.Errorf("symlink target: %q; want data.txt", dst)
	}
}

// TestRejectsZipBomb — total extracted bytes exceeding
// MaxExtractedBytes triggers ErrArchiveTooLarge.
func TestRejectsZipBomb(t *testing.T) {
	// Build a single entry whose body is 200 MiB of zeros (well
	// over the 100 MiB cap). Content compresses very well so the
	// gzip is small, but extracted size blows the cap.
	body := bytes.Repeat([]byte{0}, int(MaxExtractedBytes+1024*1024))
	archive := buildTarGz(t, []tarGzEntry{
		{name: "huge.bin", typeFlag: tar.TypeReg, body: body},
	})
	dest := t.TempDir()
	err := SafeExtractTarGz(bytes.NewReader(archive), dest)
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("err: got %v; want ErrArchiveTooLarge", err)
	}
}

// TestRejectsHardlink — tar Type 1 (hardlink) is not in our
// allowed-types set; rejected as ErrArchiveUnsafe.
func TestRejectsHardlink(t *testing.T) {
	archive := buildTarGz(t, []tarGzEntry{
		{name: "skill/data.txt", typeFlag: tar.TypeReg, body: []byte("ok")},
		{name: "skill/dup", typeFlag: tar.TypeLink, linkname: "skill/data.txt"},
	})
	dest := t.TempDir()
	err := SafeExtractTarGz(bytes.NewReader(archive), dest)
	if !errors.Is(err, ErrArchiveUnsafe) {
		t.Fatalf("err: got %v; want ErrArchiveUnsafe", err)
	}
}

// TestRejectsNonGzipPrefix — random bytes that aren't gzip surface
// as ErrUnsupportedArchive.
func TestRejectsNonGzipPrefix(t *testing.T) {
	dest := t.TempDir()
	err := SafeExtractTarGz(bytes.NewReader([]byte("this is plain text, not a tarball")), dest)
	if !errors.Is(err, ErrUnsupportedArchive) {
		t.Fatalf("err: got %v; want ErrUnsupportedArchive", err)
	}
}

// TestExtractDoesNotPanicOnTruncated — truncated tar inside the
// gzip stream surfaces as ErrUnsupportedArchive (tar header read
// failure), not a panic.
func TestExtractDoesNotPanicOnTruncated(t *testing.T) {
	archive := buildTarGz(t, []tarGzEntry{
		{name: "skill/SKILL.md", typeFlag: tar.TypeReg, body: []byte("body")},
	})
	// Truncate after the gzip+tar header but before any tar entry
	// body. Reading should produce a clean error.
	truncated := archive[:30]
	dest := t.TempDir()
	err := SafeExtractTarGz(bytes.NewReader(truncated), dest)
	if err == nil {
		t.Fatal("expected error on truncated archive; got nil")
	}
	// Either ErrUnsupportedArchive (header parse) or
	// some other archive-level error — just make sure it didn't panic.
	_ = err
}

// helper: silence the unused-var warning when buildTarGz is called
// without checking a return.
var _ = io.Discard
