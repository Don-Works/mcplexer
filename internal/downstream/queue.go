package downstream

import (
	"encoding/json"
	"sync"
)

// request represents a JSON-RPC request to send to a downstream process.
type request struct {
	ID     int
	Method string
	Params json.RawMessage // raw JSON-RPC params
	Result chan response
}

// response is the result of a downstream tool call.
type response struct {
	Data json.RawMessage
	Err  error
}

// requestQueue is a buffered channel of pending requests with safe shutdown.
type requestQueue struct {
	ch        chan request
	done      chan struct{}
	closeOnce sync.Once
}

func newRequestQueue(size int) *requestQueue {
	return &requestQueue{
		ch:   make(chan request, size),
		done: make(chan struct{}),
	}
}

// enqueue sends a request to the queue. Returns false if the queue is closed.
func (q *requestQueue) enqueue(r request) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	select {
	case q.ch <- r:
		return true
	case <-q.done:
		return false
	}
}

func (q *requestQueue) dequeue() (request, bool) {
	select {
	case r, ok := <-q.ch:
		return r, ok
	case <-q.done:
		return request{}, false
	}
}

func (q *requestQueue) close() {
	q.closeOnce.Do(func() {
		close(q.done)
	})
}
