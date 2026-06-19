package downstream

import (
	"testing"
	"time"
)

func TestRequestQueueEnqueueReturnsFalseWhenClosedDuringSend(t *testing.T) {
	q := newRequestQueue(0)
	done := make(chan bool, 1)

	go func() {
		done <- q.enqueue(request{})
	}()

	time.Sleep(10 * time.Millisecond)
	q.close()

	select {
	case ok := <-done:
		if ok {
			t.Fatal("enqueue returned true after queue close")
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue did not unblock after queue close")
	}
}
