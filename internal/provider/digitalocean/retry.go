package digitalocean

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/digitalocean/godo"
)

const (
	// baseBackoff is the wait before the second attempt; it doubles from there.
	baseBackoff = 1 * time.Second

	// maxBackoff caps a single wait. DigitalOcean does send RateLimit-Reset,
	// but it marks the end of the hourly window and can be most of an hour
	// out, which is far too long to block a CLI on. What a short burst of
	// calls actually trips is the per-minute limit, and that clears well
	// inside this.
	maxBackoff = 30 * time.Second

	// maxShift keeps the doubling from overflowing if maxAttempts is ever
	// raised well past its default.
	maxShift = 10
)

// rateLimited retries only a 429. That response means the request was rejected
// before it reached anything, so repeating it is safe no matter what it does.
func rateLimited(status int) bool { return status == http.StatusTooManyRequests }

// transient also retries 5xx. Only for requests that can be repeated without
// consequence: a 500 on a create may mean the droplet exists and the reply was
// lost on the way back, and a second attempt would leave one running unbilled
// to any state row.
func transient(status int) bool { return rateLimited(status) || status >= 500 }

// call runs one API request, retrying while retryOn accepts the status it came
// back with. Failures another attempt cannot fix - a rejected token, an
// unknown size slug - are returned immediately. The last response is returned
// alongside the error so callers can pick a 404 out of it.
func call[T any](ctx context.Context, p *Provider, retryOn func(int) bool, do func() (T, *godo.Response, error)) (T, *godo.Response, error) {
	var zero T
	for attempt := 1; ; attempt++ {
		result, resp, err := do()
		if err == nil {
			return result, resp, nil
		}
		if attempt >= p.maxAttempts || !retryOn(status(resp, err)) {
			return zero, resp, err
		}
		if waitErr := p.sleep(ctx, backoff(attempt, resp)); waitErr != nil {
			// The context ended mid-backoff. Both halves matter: what the API
			// said, and that we stopped waiting for it to change its mind.
			return zero, resp, errors.Join(err, waitErr)
		}
	}
}

// backoff returns how long to wait before the attempt after this one.
// Retry-After is authoritative when DigitalOcean sends it; otherwise the wait
// doubles from baseBackoff. No jitter - one CLI on one machine is not a
// thundering herd.
func backoff(attempt int, resp *godo.Response) time.Duration {
	if wait, ok := retryAfter(resp); ok {
		return min(wait, maxBackoff)
	}

	shift := min(attempt-1, maxShift)
	return min(baseBackoff<<shift, maxBackoff)
}

// retryAfter reads the Retry-After header, which DigitalOcean sends as a
// number of seconds rather than a date.
func retryAfter(resp *godo.Response) (time.Duration, bool) {
	if resp == nil || resp.Response == nil {
		return 0, false
	}

	seconds, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

// status pulls the HTTP status out of a failed call. godo hands back both a
// response and an error for an API failure, but the error carries the status
// on its own when the response is lost, so both are checked. Zero means the
// request never got a status at all.
func status(resp *godo.Response, err error) int {
	if resp != nil && resp.Response != nil {
		return resp.StatusCode
	}

	var apiErr *godo.ErrorResponse
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		return apiErr.Response.StatusCode
	}
	return 0
}

// sleep waits for d, or gives up early if ctx ends first.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
