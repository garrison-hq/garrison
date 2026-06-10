package agentcontainer

import (
	"encoding/binary"
	"fmt"
	"io"
)

// demuxHeaderSize is the width of one docker raw-stream frame header:
// [stream(1) pad(3) size(4, big-endian)] (spike F2). The exec-start
// response body is a normal chunked application/vnd.docker.raw-stream
// — no connection hijacking (FR-004).
const demuxHeaderSize = 8

// demuxRawStream splits a raw-stream-framed body into demuxed stdout
// (stream bytes 0 and 1) and stderr (stream byte 2) readers.
//
// Lifecycle (concurrency rule 1 — no goroutine outlives the raw
// stream): clean EOF on raw closes both pipe writers, so consumers
// see io.EOF exactly like subprocess-stdout EOF today; a malformed
// header or mid-frame truncation closes both via CloseWithError; and
// closing the returned stdout reader closes raw itself, which
// unblocks any in-flight raw read and terminates the demux goroutine.
func demuxRawStream(raw io.ReadCloser) (stdout, stderr io.ReadCloser) {
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	go func() {
		header := make([]byte, demuxHeaderSize)
		for {
			if _, err := io.ReadFull(raw, header); err != nil {
				if err == io.EOF {
					// Clean frame boundary: consumers get io.EOF.
					_ = outW.Close()
					_ = errW.Close()
				} else {
					// Truncated header (io.ErrUnexpectedEOF) or a
					// torn-down raw stream: propagate to both sides.
					outW.CloseWithError(err)
					errW.CloseWithError(err)
				}
				return
			}
			var dst *io.PipeWriter
			switch header[0] {
			case 0, 1: // stdin-echo and stdout both land on stdout
				dst = outW
			case 2:
				dst = errW
			default:
				err := fmt.Errorf("agentcontainer: malformed raw-stream header: unknown stream byte 0x%02x", header[0])
				outW.CloseWithError(err)
				errW.CloseWithError(err)
				return
			}
			size := int64(binary.BigEndian.Uint32(header[4:demuxHeaderSize]))
			if _, err := io.CopyN(dst, raw, size); err != nil {
				// Either the raw stream died mid-payload or a pipe
				// reader closed under us; the session is over for
				// both streams either way.
				outW.CloseWithError(err)
				errW.CloseWithError(err)
				return
			}
		}
	}()
	return &demuxReader{pipe: outR, raw: raw}, &demuxReader{pipe: errR}
}

// demuxReader is one demuxed pipe end. On the stdout side raw is the
// underlying response body: closing the stdout reader closes raw so
// the demux goroutine's read loop terminates (rule 1). The stderr
// side carries no raw handle — its Close only releases its own pipe.
type demuxReader struct {
	pipe *io.PipeReader
	raw  io.Closer
}

func (r *demuxReader) Read(p []byte) (int, error) {
	return r.pipe.Read(p)
}

func (r *demuxReader) Close() error {
	// Close the pipe end first so a demux goroutine blocked writing
	// to it unblocks (ErrClosedPipe), then tear down the raw stream
	// to unblock a goroutine parked in raw.Read.
	pipeErr := r.pipe.Close()
	if r.raw != nil {
		if err := r.raw.Close(); err != nil {
			return err
		}
	}
	return pipeErr
}
