// capture.go — bounded concurrent capture of one exec session's
// stdout and stderr under a single shared byte budget. Docker
// preserves stream separation (docker logs writes container-stdout
// lines to its own stdout, container-stderr lines to its own
// stderr), so both must be drained concurrently: reading only one
// while the other's SSH channel window fills lets the remote writer
// block forever, hanging the pull.
package sshx

import (
	"bytes"
	"io"
	"sync"
)

// streamCap is one byte budget shared by two concurrently-read
// streams so neither can exceed the combined cap on its own.
type streamCap struct {
	mu        sync.Mutex
	remaining int64
}

// take returns how many of the n just-read bytes still fit under the
// remaining budget and debits them. 0 means the budget is spent.
func (c *streamCap) take(n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if int64(n) > c.remaining {
		n = int(c.remaining)
	}
	c.remaining -= int64(n)
	return n
}

// captureStreams drains stdout and stderr concurrently into two
// buffers whose combined size never exceeds maxBytes+1 (the extra
// byte makes truncation detectable, mirroring the single-stream
// design this replaces). The instant the shared budget is spent,
// stop is called (at most once) so the caller can tear down the
// session and unblock whichever stream is still writing — without
// that, a stream sitting at its cap would otherwise leave the other
// goroutine (and the remote process) blocked indefinitely.
//
// A read error is only surfaced when the run was NOT truncated: once
// truncated, closing the session to stop the remote writer routinely
// makes the sibling stream's in-flight Read fail too, and that
// failure is a side effect of our own shutdown, not a real error.
func captureStreams(stdout, stderr io.Reader, maxBytes int64, stop func()) (stdoutOut, stderrOut []byte, truncated bool, err error) {
	budget := &streamCap{remaining: maxBytes + 1}
	var stopOnce sync.Once
	stopFn := func() { stopOnce.Do(stop) }

	var so, se []byte
	var errs [2]error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); so, errs[0] = drainOne(stdout, budget, stopFn) }()
	go func() { defer wg.Done(); se, errs[1] = drainOne(stderr, budget, stopFn) }()
	wg.Wait()

	if truncated = int64(len(so)+len(se)) > maxBytes; truncated {
		so, se = trimCombined(so, se, maxBytes)
		return so, se, true, nil
	}
	if errs[0] != nil {
		return so, se, false, errs[0]
	}
	return so, se, false, errs[1]
}

// drainOne reads r to EOF (or until the shared budget runs out),
// debiting every chunk from budget before buffering it.
func drainOne(r io.Reader, budget *streamCap, stop func()) ([]byte, error) {
	var buf bytes.Buffer
	chunk := make([]byte, 32*1024)
	for {
		n, rerr := r.Read(chunk)
		if n > 0 {
			take := budget.take(n)
			buf.Write(chunk[:take])
			if take < n {
				stop()
				break
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				return buf.Bytes(), rerr
			}
			break
		}
	}
	return buf.Bytes(), nil
}

// trimCombined drops the (at most one, by construction of the shared
// budget) byte over maxBytes, preferring to trim stderr first.
func trimCombined(so, se []byte, maxBytes int64) ([]byte, []byte) {
	over := int64(len(so)+len(se)) - maxBytes
	for over > 0 && len(se) > 0 {
		se = se[:len(se)-1]
		over--
	}
	for over > 0 && len(so) > 0 {
		so = so[:len(so)-1]
		over--
	}
	return so, se
}
