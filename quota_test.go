package github

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const testRateLimit = 5000

func rateHeader(remaining int, reset time.Time) http.Header {
	header := http.Header{}
	header.Set(headerRateLimit, strconv.Itoa(testRateLimit))
	header.Set(headerRateRemaining, strconv.Itoa(remaining))
	header.Set(headerRateReset, strconv.FormatInt(reset.Unix(), 10))
	return header
}

func TestQuotaRefusesWhenBelowTenPercentBeforeReset(t *testing.T) {
	var q quota
	reset := testNow().Add(time.Hour)
	q.observe(rateHeader(499, reset))
	var limited *contracts.ForgeRateLimitedError
	if err := q.refusal(testNow()); !errors.As(err, &limited) {
		t.Fatalf("refusal = %v, want ForgeRateLimitedError", err)
	}
	if !limited.Reset.Equal(reset) {
		t.Errorf("reset = %v, want %v", limited.Reset, reset)
	}
}

func TestQuotaAllowsAtTenPercent(t *testing.T) {
	var q quota
	q.observe(rateHeader(500, testNow().Add(time.Hour)))
	if err := q.refusal(testNow()); err != nil {
		t.Errorf("refusal at exactly 10%% = %v, want nil", err)
	}
}

func TestQuotaAllowsAfterResetPassed(t *testing.T) {
	var q quota
	q.observe(rateHeader(10, testNow().Add(-time.Second)))
	if err := q.refusal(testNow()); err != nil {
		t.Errorf("refusal after reset = %v, want nil", err)
	}
}

func TestQuotaAllowsWhenNothingObserved(t *testing.T) {
	var q quota
	if err := q.refusal(testNow()); err != nil {
		t.Errorf("refusal before any response = %v, want nil", err)
	}
	q.observe(http.Header{})
	if err := q.refusal(testNow()); err != nil {
		t.Errorf("refusal with no quota headers = %v, want nil", err)
	}
}

func TestQuotaRefusalSkipsTheRequest(t *testing.T) {
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		for key, values := range rateHeader(1, testNow().Add(time.Hour)) {
			w.Header()[key] = values
		}
		writeJSON(t, w, http.StatusOK, map[string]string{"login": "octocat"})
	})
	f, _ := newTestForge(t, handler)
	if _, err := f.Connection(t.Context()); err != nil {
		t.Fatal(err)
	}
	var limited *contracts.ForgeRateLimitedError
	if _, err := f.Connection(t.Context()); !errors.As(err, &limited) {
		t.Fatalf("second call err = %v, want ForgeRateLimitedError", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1: the refused call must not be sent", got)
	}
}

func TestThrottleResetFromRetryAfter(t *testing.T) {
	header := http.Header{}
	header.Set(headerRetryAfter, "120")
	reset, limited := throttleReset(header, testNow())
	if !limited || !reset.Equal(testNow().Add(2*time.Minute)) {
		t.Errorf("throttleReset = %v, %v; want reset in 2 minutes and limited", reset, limited)
	}
}

func TestThrottleResetFromRetryAfterHTTPDate(t *testing.T) {
	after := testNow().Add(90 * time.Second).Truncate(time.Second)
	header := http.Header{}
	header.Set(headerRetryAfter, after.UTC().Format(http.TimeFormat))
	reset, limited := throttleReset(header, testNow())
	if !limited || !reset.Equal(after) {
		t.Errorf("throttleReset = %v, %v; want reset at %v and limited", reset, limited, after)
	}
}

func TestReadRateLimitWithoutRemainingSaysNothing(t *testing.T) {
	header := http.Header{}
	header.Set(headerRateReset, strconv.FormatInt(testNow().Add(time.Hour).Unix(), 10))
	got := readRateLimit(header)
	if got.Remaining != 0 || !got.Reset.IsZero() {
		t.Errorf("rate limit without remaining = %+v, want the zero value", got)
	}
}

func TestReadRateLimitReportsRemainingAndReset(t *testing.T) {
	reset := testNow().Add(time.Hour)
	got := readRateLimit(rateHeader(7, reset))
	if got.Remaining != 7 || !got.Reset.Equal(reset) {
		t.Errorf("rate limit = %+v, want remaining 7 and reset %v", got, reset)
	}
}

func TestQuotaKeepsLowerRemainingWithinOneWindow(t *testing.T) {
	var q quota
	reset := testNow().Add(time.Hour)
	q.observe(rateHeader(499, reset))
	q.observe(rateHeader(4000, reset))
	var limited *contracts.ForgeRateLimitedError
	if err := q.refusal(testNow()); !errors.As(err, &limited) {
		t.Fatalf("refusal after a late, higher count in the same window = %v, want ForgeRateLimitedError", err)
	}
}

func TestQuotaTakesLowerCountOfSameWindowWhenItArrivesLast(t *testing.T) {
	var q quota
	reset := testNow().Add(time.Hour)
	q.observe(rateHeader(4000, reset))
	q.observe(rateHeader(499, reset))
	var limited *contracts.ForgeRateLimitedError
	if err := q.refusal(testNow()); !errors.As(err, &limited) {
		t.Fatalf("refusal after a lower count in the same window = %v, want ForgeRateLimitedError", err)
	}
}

func TestQuotaNewerWindowReplacesOlder(t *testing.T) {
	var q quota
	q.observe(rateHeader(499, testNow().Add(time.Hour)))
	q.observe(rateHeader(4000, testNow().Add(2*time.Hour)))
	if err := q.refusal(testNow()); err != nil {
		t.Errorf("refusal after a newer window = %v, want nil", err)
	}
}

func TestQuotaIgnoresOlderWindowArrivingLast(t *testing.T) {
	var q quota
	q.observe(rateHeader(4000, testNow().Add(2*time.Hour)))
	q.observe(rateHeader(499, testNow().Add(time.Hour)))
	if err := q.refusal(testNow()); err != nil {
		t.Errorf("refusal after an older window arrived last = %v, want nil", err)
	}
}

func zeroRemainingHeader() http.Header {
	header := http.Header{}
	header.Set(headerRateLimit, strconv.Itoa(testRateLimit))
	header.Set(headerRateRemaining, "0")
	return header
}

func TestQuotaZeroRemainingWithoutResetDoesNotRefuse(t *testing.T) {
	var q quota
	q.observe(zeroRemainingHeader())
	if err := q.refusal(testNow()); err != nil {
		t.Errorf("refusal with remaining 0 and no reset = %v, want nil: the forge did not say when the budget refills", err)
	}
}

func TestReadRateLimitWithZeroRemainingAndNoResetKeepsZeroReset(t *testing.T) {
	got := readRateLimit(zeroRemainingHeader())
	if got.Remaining != 0 || !got.Reset.IsZero() {
		t.Errorf("rate limit = %+v, want remaining 0 and the zero reset", got)
	}
}

func TestQuotaConcurrentObserversKeepNewestWindowAndLowestCount(t *testing.T) {
	var q quota
	base := testNow()
	newest := base.Add(3 * time.Hour)
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Go(func() { observeWindowsAndRefuse(t, &q, base, worker) })
	}
	wg.Wait()
	if !q.reset.Equal(newest) || q.remaining != 499 {
		t.Fatalf("quota = reset %v remaining %d, want reset %v remaining 499", q.reset, q.remaining, newest)
	}
	var limited *contracts.ForgeRateLimitedError
	if err := q.refusal(base); !errors.As(err, &limited) || !limited.Reset.Equal(newest) {
		t.Errorf("refusal = %v, want ForgeRateLimitedError resetting at %v", err, newest)
	}
}

func observeWindowsAndRefuse(t *testing.T, q *quota, base time.Time, worker int) {
	t.Helper()
	for step := range 50 {
		q.observe(rateHeader(1, base.Add(time.Hour)))
		q.observe(rateHeader(4000, base.Add(2*time.Hour)))
		q.observe(rateHeader(499+worker*100+step, base.Add(3*time.Hour)))
		assertRateLimitOrNoRefusal(t, q.refusal(base))
	}
}

func assertRateLimitOrNoRefusal(t *testing.T, err error) {
	t.Helper()
	var limited *contracts.ForgeRateLimitedError
	if err != nil && !errors.As(err, &limited) {
		t.Errorf("refusal during observation = %v, want ForgeRateLimitedError or nil", err)
	}
}

func TestParseCount(t *testing.T) {
	tests := map[string]bool{"0": true, "42": true, "": false, "-1": false, "abc": false}
	for text, want := range tests {
		if _, ok := parseCount(text); ok != want {
			t.Errorf("parseCount(%q) ok = %v, want %v", text, ok, want)
		}
	}
}
