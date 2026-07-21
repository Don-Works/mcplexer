package downstream

import (
	"context"
	"errors"
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

func TestRequestDeliverDoesNotBlockAbandonedCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	delivered := make(chan struct{})
	go func() {
		request{
			Context: ctx,
			Result:  make(chan response), // deliberately unbuffered
		}.deliver(response{Err: errors.New("late response")})
		close(delivered)
	}()

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("delivery blocked after the caller context was cancelled")
	}
}

func TestRequestDeliverBufferedResultSurvivesCallerDelay(t *testing.T) {
	result := make(chan response, 1)
	want := errors.New("completed")
	request{Result: result}.deliver(response{Err: want})

	select {
	case got := <-result:
		if !errors.Is(got.Err, want) {
			t.Fatalf("delivered error = %v, want %v", got.Err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("buffered result was not retained for the delayed caller")
	}
}
