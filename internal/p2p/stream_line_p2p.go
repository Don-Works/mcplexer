//go:build p2p

package p2p

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// ErrShareLineTooLarge is returned when an inbound share-stream line exceeds
// the protocol's byte cap.
var ErrShareLineTooLarge = errors.New("p2p share: stream line exceeds cap")

// maxShareControlLineBytes bounds control/metadata frames (registry entries,
// hub index/search responses, request headers) that carry no large blob body.
const maxShareControlLineBytes int64 = 16 * 1024 * 1024

// shareLineCap returns the maximum NDJSON line length for a protocol whose raw
// payload cap is rawMax. A line carries the raw payload base64-encoded (~4/3x)
// inside a JSON envelope, so the wire line is larger than the raw cap; this
// adds that expansion (generously, 1.5x) plus fixed envelope headroom so a
// legitimately max-sized payload still fits while receive-side allocation
// stays bounded.
func shareLineCap(rawMax int64) int64 {
	return rawMax + rawMax/2 + 256*1024
}

// readLimitedLine reads one '\n'-terminated line from r, refusing to buffer
// more than maxBytes.
//
// A raw bufio.Reader.ReadBytes('\n') on a libp2p stream accumulates the whole
// line in memory before any length check, so a paired peer could stream a
// newline-free multi-GB "line" and OOM the daemon. Wrapping in
// io.LimitReader(maxBytes+1) caps the allocation; a full maxBytes+1 read with
// no delimiter is rejected as ErrShareLineTooLarge. Returns the line WITHOUT
// the trailing newline. An empty stream surfaces as io.EOF.
func readLimitedLine(r io.Reader, maxBytes int64) ([]byte, error) {
	br := bufio.NewReader(io.LimitReader(r, maxBytes+1))
	line, err := br.ReadBytes('\n')
	if int64(len(line)) > maxBytes {
		return nil, fmt.Errorf("%w: > %d bytes", ErrShareLineTooLarge, maxBytes)
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			// EOF after a partial, un-terminated final line: return what we
			// have (the caller's JSON decode validates it).
		} else {
			return nil, err
		}
	}
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	return line, nil
}
