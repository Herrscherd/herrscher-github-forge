package github

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	lowQuotaDivisor     = 10
	headerRetryAfter    = "Retry-After"
	headerRateLimit     = "X-Ratelimit-Limit"
	headerRateRemaining = "X-Ratelimit-Remaining"
	headerRateReset     = "X-Ratelimit-Reset"
)

type quota struct {
	mu        sync.Mutex
	known     bool
	limit     int
	remaining int
	reset     time.Time
}

func (q *quota) observe(header http.Header) {
	limit, hasLimit := parseCount(header.Get(headerRateLimit))
	remaining, hasRemaining := parseCount(header.Get(headerRateRemaining))
	if !hasLimit || !hasRemaining {
		return
	}
	reset := resetTime(header)
	q.mu.Lock()
	defer q.mu.Unlock()
	newerWindow := !q.known || reset.After(q.reset)
	lowerInSameWindow := q.known && reset.Equal(q.reset) && remaining < q.remaining
	if newerWindow || lowerInSameWindow {
		q.known = true
		q.limit = limit
		q.remaining = remaining
		q.reset = reset
	}
}

func (q *quota) refusal(now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.known || !q.reset.After(now) || q.remaining*lowQuotaDivisor >= q.limit {
		return nil
	}
	return &contracts.ForgeRateLimitedError{Reset: q.reset}
}

func readRateLimit(header http.Header) contracts.ForgeRateLimit {
	remaining, known := parseCount(header.Get(headerRateRemaining))
	if !known {
		return contracts.ForgeRateLimit{}
	}
	return contracts.ForgeRateLimit{Remaining: remaining, Reset: resetTime(header)}
}

func throttleReset(header http.Header, now time.Time) (time.Time, bool) {
	if wait, ok := parseCount(header.Get(headerRetryAfter)); ok {
		return now.Add(time.Duration(wait) * time.Second), true
	}
	if after, err := http.ParseTime(header.Get(headerRetryAfter)); err == nil {
		return after, true
	}
	if header.Get(headerRateRemaining) == "0" {
		return resetTime(header), true
	}
	return time.Time{}, false
}

func resetTime(header http.Header) time.Time {
	seconds, ok := parseCount(header.Get(headerRateReset))
	if !ok {
		return time.Time{}
	}
	return time.Unix(int64(seconds), 0)
}

func parseCount(text string) (int, bool) {
	count, err := strconv.Atoi(text)
	return count, err == nil && count >= 0
}
