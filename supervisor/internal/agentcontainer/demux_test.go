package agentcontainer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// rawFrame encodes one [stream(1) pad(3) size(4)] frame around the
// payload, matching the docker raw-stream wire shape from spike F2.
func rawFrame(stream byte, payload string) []byte {
	buf := make([]byte, demuxHeaderSize+len(payload))
	buf[0] = stream
	binary.BigEndian.PutUint32(buf[4:demuxHeaderSize], uint32(len(payload)))
	copy(buf[demuxHeaderSize:], payload)
	return buf
}

// readBoth drains stdout and stderr concurrently. The demux goroutine
// writes both pipes from a single loop, so a sequential ReadAll on
// one side would deadlock against interleaved frames for the other.
func readBoth(t *testing.T, stdout, stderr io.Reader) (outData, errData []byte, outErr, errErr error) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		outData, outErr = io.ReadAll(stdout)
	}()
	go func() {
		defer wg.Done()
		errData, errErr = io.ReadAll(stderr)
	}()
	wg.Wait()
	return outData, errData, outErr, errErr
}

// chunkedReader yields at most chunk bytes per Read, forcing frame
// headers and payloads to straddle read boundaries.
type chunkedReader struct {
	data  []byte
	chunk int
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := c.chunk
	if n > len(c.data) {
		n = len(c.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

// blockingRaw blocks every Read until Close, and records that Close
// happened — the observable end of the stdout-Close teardown chain.
type blockingRaw struct {
	closed    chan struct{}
	closeOnce sync.Once
}

func newBlockingRaw() *blockingRaw {
	return &blockingRaw{closed: make(chan struct{})}
}

func (b *blockingRaw) Read(p []byte) (int, error) {
	<-b.closed
	return 0, errors.New("raw stream closed")
}

func (b *blockingRaw) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestDemuxSplitsStdoutAndStderrFrames(t *testing.T) {
	var raw bytes.Buffer
	raw.Write(rawFrame(1, "hello "))
	raw.Write(rawFrame(2, "oops: "))
	raw.Write(rawFrame(1, "world"))
	raw.Write(rawFrame(2, "broken pipe"))
	raw.Write(rawFrame(0, "!")) // stream 0 (stdin echo) lands on stdout

	stdout, stderr := demuxRawStream(io.NopCloser(&raw))
	outData, errData, outErr, errErr := readBoth(t, stdout, stderr)

	if outErr != nil || errErr != nil {
		t.Fatalf("ReadAll errors: stdout=%v stderr=%v", outErr, errErr)
	}
	if got, want := string(outData), "hello world!"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := string(errData), "oops: broken pipe"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestDemuxHandlesFrameSplitAcrossReads(t *testing.T) {
	var wire bytes.Buffer
	wire.Write(rawFrame(1, "a long stdout payload that spans many tiny reads"))
	wire.Write(rawFrame(2, "stderr too"))
	wire.Write(rawFrame(1, "tail"))

	// 3-byte reads guarantee every 8-byte header and every payload is
	// split across multiple Read calls.
	raw := io.NopCloser(&chunkedReader{data: wire.Bytes(), chunk: 3})
	stdout, stderr := demuxRawStream(raw)
	outData, errData, outErr, errErr := readBoth(t, stdout, stderr)

	if outErr != nil || errErr != nil {
		t.Fatalf("ReadAll errors: stdout=%v stderr=%v", outErr, errErr)
	}
	if got, want := string(outData), "a long stdout payload that spans many tiny readstail"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := string(errData), "stderr too"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestDemuxPropagatesEOFAndClose(t *testing.T) {
	t.Run("raw EOF closes both pipes cleanly", func(t *testing.T) {
		var raw bytes.Buffer
		raw.Write(rawFrame(1, "done"))

		stdout, stderr := demuxRawStream(io.NopCloser(&raw))
		outData, errData, outErr, errErr := readBoth(t, stdout, stderr)

		if outErr != nil || errErr != nil {
			t.Fatalf("clean EOF must surface as io.EOF (nil from ReadAll): stdout=%v stderr=%v", outErr, errErr)
		}
		if got := string(outData); got != "done" {
			t.Errorf("stdout = %q, want %q", got, "done")
		}
		if len(errData) != 0 {
			t.Errorf("stderr = %q, want empty", errData)
		}
	})

	t.Run("closing stdout tears down the raw body", func(t *testing.T) {
		raw := newBlockingRaw()
		stdout, stderr := demuxRawStream(raw)

		if err := stdout.Close(); err != nil {
			t.Fatalf("stdout.Close() = %v", err)
		}
		select {
		case <-raw.closed:
			// raw body closed — the demux goroutine's blocked Read
			// has been released.
		case <-time.After(2 * time.Second):
			t.Fatal("closing the stdout reader did not close the raw stream")
		}

		// The released goroutine sees the raw read error and
		// propagates it to the surviving stderr side — proof nothing
		// is left running against the dead stream.
		done := make(chan error, 1)
		go func() {
			_, err := io.ReadAll(stderr)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("stderr ReadAll after teardown = nil error, want raw-stream error")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("stderr reader still blocked after raw teardown")
		}
	})
}

func TestDemuxRejectsMalformedHeader(t *testing.T) {
	var raw bytes.Buffer
	raw.Write(rawFrame(1, "ok"))
	raw.Write(rawFrame(7, "garbage")) // stream byte 7 is not a docker stream

	stdout, stderr := demuxRawStream(io.NopCloser(&raw))
	outData, _, outErr, errErr := readBoth(t, stdout, stderr)

	if outErr == nil || errErr == nil {
		t.Fatalf("malformed header must error both readers: stdout=%v stderr=%v", outErr, errErr)
	}
	if !strings.Contains(outErr.Error(), "malformed raw-stream header") {
		t.Errorf("stdout error = %q, want malformed-header error", outErr)
	}
	if !strings.Contains(errErr.Error(), "malformed raw-stream header") {
		t.Errorf("stderr error = %q, want malformed-header error", errErr)
	}
	// Frames before the malformed header were already delivered.
	if got := string(outData); got != "ok" {
		t.Errorf("stdout before failure = %q, want %q", got, "ok")
	}
}
