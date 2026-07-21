package downstream

import (
	"bufio"
	"context"
)

type scanResult struct {
	line []byte
	err  error
}

type responseStream struct {
	lines <-chan scanResult
	done  <-chan struct{}
}

// pumpResponses is the sole reader of a downstream's stdout after the
// initialize handshake. A pipe itself has no portable read-deadline API, so
// cancellation is implemented by Instance closing the read end and cancelling
// the child process. That makes Scanner.Scan return and guarantees this one
// per-process goroutine can exit; no per-request reader goroutine is needed.
func pumpResponses(ctx context.Context, scanner *bufio.Scanner) responseStream {
	lines := make(chan scanResult)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(lines)
		for scanner.Scan() {
			// Scanner reuses its buffer on the next Scan, so transfer ownership
			// of an immutable copy to the response consumer.
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- scanResult{line: line}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case lines <- scanResult{err: err}:
			case <-ctx.Done():
			}
		}
	}()
	return responseStream{lines: lines, done: done}
}
