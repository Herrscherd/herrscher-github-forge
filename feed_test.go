package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/forgetest"
)

const (
	testFeedSubscription = "sub-1"
	testFeedSilence      = 50 * time.Millisecond
	testFeedWait         = 5 * time.Second
	laggingReaderPause   = 100 * time.Millisecond
	testPreviewRefusal   = `{"message":"Event subscriptions are not enabled for this account"}`
	testPRAPIURL         = "https://api.github.com/repos/acme/widgets/pulls/5"
)

func feedEvent(eventType string) string {
	return fmt.Sprintf("data: {\"type\":%q,\"data\":{\"api_url\":%q}}\n\n", eventType, testPRAPIURL)
}

func feedEnd(outcome, reason string) string {
	return fmt.Sprintf("event: github.stream.end\ndata: {\"outcome\":%q,\"reason\":%q}\n\n", outcome, reason)
}

const feedBeat = ": heartbeat\n\n"

func subscribeCreated(tb testing.TB, seen chan<- subscriptionRequest) http.HandlerFunc {
	tb.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			var sent subscriptionRequest
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				tb.Errorf("decode subscription request: %v", err)
			}
			seen <- sent
		}
		writeJSON(tb, w, http.StatusCreated, map[string]string{"id": testFeedSubscription})
	}
}

func subscribeRefused(tb testing.TB, status int, body string) http.HandlerFunc {
	tb.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		writeBody(tb, w, body)
	}
}

func streamScript(tb testing.TB, frames ...string) http.HandlerFunc {
	tb.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			tb.Error("response writer cannot flush")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, frame := range frames {
			writeBody(tb, w, frame)
			flusher.Flush()
		}
	}
}

func streamThenHold(tb testing.TB, frames ...string) http.HandlerFunc {
	tb.Helper()
	script := streamScript(tb, frames...)
	return func(w http.ResponseWriter, r *http.Request) {
		script(w, r)
		<-r.Context().Done()
	}
}

func feedRoutes(subscribe, stream http.HandlerFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /event-subscriptions", subscribe)
	mux.HandleFunc("GET /event-subscriptions/{id}/events", stream)
	return mux
}

func feedForge(tb testing.TB, subscribe, stream http.HandlerFunc) *forge {
	tb.Helper()
	f, _ := newTestForge(tb, feedRoutes(subscribe, stream))
	return f
}

func feedRef(tb testing.TB) contracts.PullRequestRef {
	tb.Helper()
	return mustPullRef(tb, testRepo(tb))
}

func openFeed(ctx context.Context, tb testing.TB, f *forge) <-chan contracts.PullRequestSignal {
	tb.Helper()
	signals, err := f.PullRequestEvents(ctx, feedRef(tb))
	if err != nil {
		tb.Fatalf("PullRequestEvents: %v", err)
	}
	return signals
}

func nextSignal(tb testing.TB, signals <-chan contracts.PullRequestSignal) contracts.PullRequestSignal {
	tb.Helper()
	select {
	case signal, open := <-signals:
		if !open {
			tb.Fatal("channel closed, want a signal")
		}
		return signal
	case <-time.After(testFeedWait):
		tb.Fatal("no signal within the wait")
	}
	return contracts.PullRequestSignal{}
}

func expectClosed(tb testing.TB, signals <-chan contracts.PullRequestSignal) {
	tb.Helper()
	select {
	case signal, open := <-signals:
		if open {
			tb.Fatalf("got signal %+v, want the channel closed", signal)
		}
	case <-time.After(testFeedWait):
		tb.Fatal("channel still open, want it closed")
	}
}

func TestPullRequestEventsRefusedAtOpen(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		refused bool
		want    error
	}{
		{name: "forbidden with the preview message", status: http.StatusForbidden, body: testPreviewRefusal, refused: true, want: contracts.ErrFeedRefused},
		{name: "not found with the preview message", status: http.StatusNotFound, body: testPreviewRefusal, refused: true, want: contracts.ErrFeedRefused},
		{name: "forbidden with another message", status: http.StatusForbidden, body: `{"message":"Resource not accessible"}`, want: contracts.ErrForgeForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := feedForge(t, subscribeRefused(t, tc.status, tc.body), streamScript(t))
			signals, err := f.PullRequestEvents(t.Context(), feedRef(t))
			if !errors.Is(err, tc.want) {
				t.Fatalf("PullRequestEvents error = %v, want %v", err, tc.want)
			}
			if signals != nil {
				t.Fatal("PullRequestEvents returned a channel with the refusal")
			}
			if tc.refused && errors.Is(err, contracts.ErrForgeForbidden) {
				t.Fatal("preview refusal also matches ErrForgeForbidden")
			}
		})
	}
}

func TestPullRequestEventsRefusedOnGitHubEnterprise(t *testing.T) {
	host, err := contracts.ParseForgeHost("ghe.example.com")
	if err != nil {
		t.Fatal(err)
	}
	repo := mustRepo(t, "ghe.example.com/acme/widgets")
	ref := mustPullRef(t, repo)
	f := newForge(host, &fakeCreds{token: mustToken(t)}, endpoints{}, http.DefaultClient, fixedClock)
	if _, err := f.PullRequestEvents(t.Context(), ref); !errors.Is(err, contracts.ErrFeedRefused) {
		t.Fatalf("PullRequestEvents error = %v, want ErrFeedRefused", err)
	}
}

func TestPullRequestEventsSendsEveryKindAndSkipsUnknownTypes(t *testing.T) {
	sentCh := make(chan subscriptionRequest, 1)
	f := feedForge(t, subscribeCreated(t, sentCh), streamThenHold(t,
		feedEvent("com.github.pull_request.review.submitted.v1"),
		feedEvent("com.github.pull_request.unknown.v9"),
		feedBeat,
		feedEvent("com.github.pull_request.head.changed.v1"),
		feedEvent("com.github.pull_request.state.changed.v1"),
		feedEvent("com.github.pull_request.merge_conflict.v1"),
		feedEvent("com.github.pull_request.check_suite.status.changed.v1"),
	))
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	defer cancel()
	want := []contracts.PullRequestEventKind{
		contracts.PullRequestReviewSubmitted,
		contracts.PullRequestNewCommit,
		contracts.PullRequestStateChanged,
		contracts.PullRequestMergeConflict,
		contracts.PullRequestChecksChanged,
	}
	for _, kind := range want {
		signal := nextSignal(t, signals)
		if signal.Err != nil || signal.Kind != kind {
			t.Fatalf("signal = %+v, want kind %v", signal, kind)
		}
	}
	sent := <-sentCh
	if sent.ResourceURL != "https://github.com/acme/widgets/pull/5" {
		t.Errorf("resource_url = %q, want the pull request web URL", sent.ResourceURL)
	}
	if len(sent.EventTypes) != 5 {
		t.Errorf("event_types = %v, want the five pull request types", sent.EventTypes)
	}
}

func TestPullRequestEventsMergesKindsForLaggingReader(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), streamScript(t,
		feedEvent("com.github.pull_request.review.submitted.v1"),
		feedEvent("com.github.pull_request.review.submitted.v1"),
		feedEvent("com.github.pull_request.state.changed.v1"),
		feedEvent("com.github.pull_request.review.submitted.v1"),
		feedEvent("com.github.pull_request.state.changed.v1"),
	))
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	defer cancel()
	time.Sleep(laggingReaderPause)
	first := nextSignal(t, signals)
	second := nextSignal(t, signals)
	if first.Kind != contracts.PullRequestReviewSubmitted || second.Kind != contracts.PullRequestStateChanged {
		t.Fatalf("signals = %+v, %+v, want review then state once each", first, second)
	}
	last := nextSignal(t, signals)
	if !errors.Is(last.Err, ErrFeedEnded) {
		t.Fatalf("last signal = %+v, want ErrFeedEnded after the merged kinds", last)
	}
	expectClosed(t, signals)
}

func TestPullRequestEventsSilenceEndsWithTypedError(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), streamThenHold(t,
		feedEvent("com.github.pull_request.state.changed.v1"),
	))
	f.feedSilence = testFeedSilence
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	defer cancel()
	first := nextSignal(t, signals)
	if first.Kind != contracts.PullRequestStateChanged {
		t.Fatalf("first signal = %+v, want state changed", first)
	}
	last := nextSignal(t, signals)
	if !errors.Is(last.Err, ErrFeedSilent) || last.Kind != 0 {
		t.Fatalf("last signal = %+v, want ErrFeedSilent with a zero kind", last)
	}
	expectClosed(t, signals)
}

func TestPullRequestEventsServerCloseEndsWithError(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), streamScript(t,
		feedEvent("com.github.pull_request.review.submitted.v1"),
	))
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	defer cancel()
	if signal := nextSignal(t, signals); signal.Kind != contracts.PullRequestReviewSubmitted {
		t.Fatalf("first signal = %+v, want review submitted", signal)
	}
	if last := nextSignal(t, signals); !errors.Is(last.Err, ErrFeedEnded) {
		t.Fatalf("last signal = %+v, want ErrFeedEnded", last)
	}
	expectClosed(t, signals)
}

func TestPullRequestEventsExpiredEndFrameEndsWithError(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), streamScript(t,
		feedEnd("subscription_ended", "expired"),
	))
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	defer cancel()
	if last := nextSignal(t, signals); !errors.Is(last.Err, ErrFeedEnded) || last.Kind != 0 {
		t.Fatalf("last signal = %+v, want ErrFeedEnded with a zero kind", last)
	}
	expectClosed(t, signals)
}

func TestPullRequestEventsRefusedMidStream(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), streamScript(t,
		feedEnd("subscription_ended", "access_revoked"),
	))
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	defer cancel()
	last := nextSignal(t, signals)
	if !errors.Is(last.Err, contracts.ErrFeedRefused) {
		t.Fatalf("last signal = %+v, want ErrFeedRefused", last)
	}
	expectClosed(t, signals)
}

func TestPullRequestEventsCancellationEndsWithoutSignal(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), streamThenHold(t,
		feedEvent("com.github.pull_request.review.submitted.v1"),
	))
	baseline := goroutineBaseline(f)
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	if signal := nextSignal(t, signals); signal.Kind != contracts.PullRequestReviewSubmitted {
		t.Fatalf("first signal = %+v, want review submitted", signal)
	}
	cancel()
	expectClosed(t, signals)
	assertNoLeak(t, f, baseline)
}

func goroutineBaseline(f *forge) int {
	f.client.CloseIdleConnections()
	return runtime.NumGoroutine()
}

func assertNoLeak(tb testing.TB, f *forge, baseline int) {
	tb.Helper()
	deadline := time.Now().Add(testFeedWait)
	for {
		f.client.CloseIdleConnections()
		if runtime.NumGoroutine() <= baseline {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("goroutines = %d after cancellation, baseline %d", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPullRequestEventsRejectsOversizedEvent(t *testing.T) {
	cases := []struct {
		name  string
		frame string
	}{
		{name: "one line over the line limit", frame: "data: " + strings.Repeat("x", feedLineLimit+1) + "\n\n"},
		{name: "lines over the event limit", frame: strings.Repeat("data: "+strings.Repeat("y", 60<<10)+"\n", 5) + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := feedForge(t, subscribeCreated(t, nil), streamThenHold(t, tc.frame))
			ctx, cancel := context.WithCancel(t.Context())
			signals := openFeed(ctx, t, f)
			defer cancel()
			last := nextSignal(t, signals)
			if !errors.Is(last.Err, errFeedEventTooLarge) {
				t.Fatalf("signal = %+v, want errFeedEventTooLarge", last)
			}
			expectClosed(t, signals)
		})
	}
}

func TestPullRequestEventsConformance(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), streamThenHold(t,
		feedEvent("com.github.pull_request.review.submitted.v1"),
	))
	forgetest.CheckPullRequestFeed(t, f, feedRef(t))
}

func TestReadFeedStopsWhenOwnerLeaves(t *testing.T) {
	frames := make(chan feedFrame)
	stop := make(chan struct{})
	done := make(chan struct{})
	body := strings.NewReader(feedEvent("com.github.pull_request.review.submitted.v1") + feedEvent("com.github.pull_request.state.changed.v1"))
	go func() {
		defer close(done)
		readFeed(body, frames, stop)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(testFeedWait):
		t.Fatal("readFeed still blocked after the owner left")
	}
}

func TestPullRequestEventsSilenceReleasesStream(t *testing.T) {
	released := make(chan struct{})
	script := streamScript(t, feedEvent("com.github.pull_request.state.changed.v1"))
	f := feedForge(t, subscribeCreated(t, nil), func(w http.ResponseWriter, r *http.Request) {
		script(w, r)
		<-r.Context().Done()
		close(released)
	})
	f.feedSilence = testFeedSilence
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	signals := openFeed(ctx, t, f)
	nextSignal(t, signals)
	if last := nextSignal(t, signals); !errors.Is(last.Err, ErrFeedSilent) {
		t.Fatalf("last signal = %+v, want ErrFeedSilent", last)
	}
	expectClosed(t, signals)
	select {
	case <-released:
	case <-time.After(testFeedWait):
		t.Fatal("the server still holds the stream after the feed went silent")
	}
}
