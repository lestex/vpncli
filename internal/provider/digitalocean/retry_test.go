package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

func TestBackoff(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		resp    *godo.Response
		want    time.Duration
	}{
		{"first wait", 1, nil, 1 * time.Second},
		{"doubles", 2, nil, 2 * time.Second},
		{"doubles again", 3, nil, 4 * time.Second},
		{"capped", 9, nil, maxBackoff},
		{"no overflow far past the cap", 64, nil, maxBackoff},
		{"retry-after wins over the schedule", 1, withRetryAfter("7"), 7 * time.Second},
		{"retry-after is capped too", 1, withRetryAfter("3600"), maxBackoff},
		{"a date is not a number of seconds", 1, withRetryAfter("Wed, 26 Aug 2026 12:00:00 GMT"), 1 * time.Second},
		{"negative header ignored", 2, withRetryAfter("-5"), 2 * time.Second},
		{"no header", 2, withRetryAfter(""), 2 * time.Second},
		{"response without an http response", 2, &godo.Response{}, 2 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backoff(tt.attempt, tt.resp); got != tt.want {
				t.Errorf("backoff(%d) = %v, want %v", tt.attempt, got, tt.want)
			}
		})
	}
}

func withRetryAfter(value string) *godo.Response {
	header := http.Header{}
	if value != "" {
		header.Set("Retry-After", value)
	}
	return &godo.Response{Response: &http.Response{StatusCode: http.StatusTooManyRequests, Header: header}}
}

func TestStatus(t *testing.T) {
	httpResp := &http.Response{StatusCode: http.StatusNotFound}

	tests := []struct {
		name string
		resp *godo.Response
		err  error
		want int
	}{
		{"from the response", &godo.Response{Response: httpResp}, nil, http.StatusNotFound},
		{"from the error when the response is gone", nil, &godo.ErrorResponse{Response: httpResp}, http.StatusNotFound},
		{"a transport failure has no status", nil, errors.New("connection reset"), 0},
		{"neither", nil, nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status(tt.resp, tt.err); got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRetryPolicies(t *testing.T) {
	tests := []struct {
		status        int
		rateLimited   bool
		alsoTransient bool
	}{
		{http.StatusTooManyRequests, true, true},
		{http.StatusInternalServerError, false, true},
		{http.StatusBadGateway, false, true},
		{http.StatusUnauthorized, false, false},
		{http.StatusUnprocessableEntity, false, false},
		{http.StatusNotFound, false, false},
		{0, false, false},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			if got := rateLimited(tt.status); got != tt.rateLimited {
				t.Errorf("rateLimited(%d) = %v, want %v", tt.status, got, tt.rateLimited)
			}
			if got := transient(tt.status); got != tt.alsoTransient {
				t.Errorf("transient(%d) = %v, want %v", tt.status, got, tt.alsoTransient)
			}
		})
	}
}

// A rate-limited call keeps trying, waiting longer each time.
func TestCallRetriesUntilItSucceeds(t *testing.T) {
	d := droplet(1001, "vpncli-fra1-a1b2", "active")
	f := &fakeDroplets{gets: []reply{
		failure(http.StatusTooManyRequests, ""),
		failure(http.StatusServiceUnavailable, ""),
		ok(&d),
	}}

	if _, err := newTestProvider(f).GetInstance(context.Background(), "1001"); err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if len(f.getIDs) != 3 {
		t.Errorf("made %d attempts, want 3", len(f.getIDs))
	}

	want := []time.Duration{1 * time.Second, 2 * time.Second}
	if len(f.slept) != len(want) {
		t.Fatalf("slept %v, want %v", f.slept, want)
	}
	for i, d := range want {
		if f.slept[i] != d {
			t.Errorf("wait %d was %v, want %v", i, f.slept[i], d)
		}
	}
}

func TestCallGivesUpAtMaxAttempts(t *testing.T) {
	f := &fakeDroplets{}
	for range defaultMaxAttempts {
		f.gets = append(f.gets, failure(http.StatusTooManyRequests, ""))
	}

	if _, err := newTestProvider(f).GetInstance(context.Background(), "1001"); err == nil {
		t.Fatal("expected an error")
	}
	if len(f.getIDs) != defaultMaxAttempts {
		t.Errorf("made %d attempts, want %d", len(f.getIDs), defaultMaxAttempts)
	}
	// The last failure is reported, not slept on.
	if len(f.slept) != defaultMaxAttempts-1 {
		t.Errorf("slept %d times, want %d", len(f.slept), defaultMaxAttempts-1)
	}
}

// Errors a retry cannot fix come straight back.
func TestCallDoesNotRetryClientErrors(t *testing.T) {
	f := &fakeDroplets{gets: []reply{failure(http.StatusUnauthorized, "")}}

	if _, err := newTestProvider(f).GetInstance(context.Background(), "1001"); err == nil {
		t.Fatal("expected an error")
	}
	if len(f.getIDs) != 1 {
		t.Errorf("made %d attempts, want 1", len(f.getIDs))
	}
	if len(f.slept) != 0 {
		t.Error("waited before giving up on a rejected token")
	}
}

// A context that ends mid-backoff must report both what the API said and why
// we stopped waiting on it.
func TestCallReportsBothErrorsWhenTheContextEnds(t *testing.T) {
	f := &fakeDroplets{
		gets:     []reply{failure(http.StatusTooManyRequests, "")},
		sleepErr: context.Canceled,
	}

	_, err := newTestProvider(f).GetInstance(context.Background(), "1001")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want it to wrap context.Canceled", err)
	}

	var apiErr *godo.ErrorResponse
	if !errors.As(err, &apiErr) {
		t.Errorf("got %v, want it to carry the API error too", err)
	}
}

func TestSleepStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestSleepWaits(t *testing.T) {
	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleep: %v", err)
	}
}
