package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/forgetest"
)

const (
	testFeedSubscription = "sub-1"
	testFeedWait         = 5 * time.Second
	testHeaderWait       = 20 * time.Millisecond
	testRoundsCancel     = 200
	testPreviewRefusal   = `{"message":"Event subscriptions are not enabled for this account"}`
	testPRAPIURL         = "https://api.github.com/repos/acme/widgets/pulls/5"
)

const (
	testReviewEvent = "com.github.pull_request.review.submitted.v1"
	testStateEvent  = "com.github.pull_request.state.changed.v1"
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

type scriptedBody struct {
	script    []byte
	endless   bool
	hold      bool
	pos       int
	exhausted chan struct{}
	exhaust   sync.Once
	release   chan struct{}
	unblock   sync.Once
	closes    atomic.Int32
}

func newScriptedBody(script string, endless, hold bool) *scriptedBody {
	return &scriptedBody{
		script:    []byte(script),
		endless:   endless,
		hold:      hold,
		exhausted: make(chan struct{}),
		release:   make(chan struct{}),
	}
}

func (b *scriptedBody) Read(p []byte) (int, error) {
	if b.pos == len(b.script) {
		if !b.endless {
			b.exhaust.Do(func() { close(b.exhausted) })
			if !b.hold {
				return 0, io.EOF
			}
			<-b.release
			return 0, io.ErrClosedPipe
		}
		b.pos = 0
	}
	n := copy(p, b.script[b.pos:])
	b.pos += n
	return n, nil
}

func (b *scriptedBody) Close() error {
	b.closes.Add(1)
	b.unblock.Do(func() { close(b.release) })
	return nil
}

type scriptedTransport struct {
	base http.RoundTripper
	body *scriptedBody
}

func (t *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.base.RoundTrip(req)
	}
	header := http.Header{"Content-Type": []string{mediaTypeEventStream}}
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: header, Body: t.body, Request: req}, nil
}

func feedWithScript(tb testing.TB, body *scriptedBody) (*forge, *scriptedTransport) {
	tb.Helper()
	f := feedForge(tb, subscribeCreated(tb, nil), streamScript(tb))
	base := f.client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	tr := &scriptedTransport{base: base, body: body}
	f.client.Transport = tr
	return f, tr
}

type fakeSilence struct {
	fire   chan time.Time
	resets chan struct{}
}

func (s *fakeSilence) C() <-chan time.Time {
	return s.fire
}

func (s *fakeSilence) Reset(time.Duration) bool {
	select {
	case s.resets <- struct{}{}:
	default:
	}
	return true
}

func (s *fakeSilence) Stop() bool {
	return true
}

func silenceFrom(f *forge) *fakeSilence {
	s := &fakeSilence{fire: make(chan time.Time, 1), resets: make(chan struct{}, 16)}
	f.newSilenceTimer = func(time.Duration) silenceTimer { return s }
	return s
}

func waitResets(tb testing.TB, resets <-chan struct{}, n int) {
	tb.Helper()
	for range n {
		select {
		case <-resets:
		case <-time.After(testFeedWait):
			tb.Fatal("the feed did not process the frames in time")
		}
	}
}

func drainAfterCancel(tb testing.TB, signals <-chan contracts.PullRequestSignal) int {
	tb.Helper()
	late := 0
	deadline := time.After(testFeedWait)
	for {
		select {
		case _, open := <-signals:
			if !open {
				return late
			}
			late++
		case <-deadline:
			tb.Fatal("channel still open after cancellation")
		}
	}
}

func waitBarrier(tb testing.TB, ready <-chan struct{}) {
	tb.Helper()
	select {
	case <-ready:
	case <-time.After(testFeedWait):
		tb.Fatal("stream never reached the barrier")
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
	body := newScriptedBody(
		feedEvent(testReviewEvent)+feedEvent(testReviewEvent)+feedEvent(testStateEvent)+feedEvent(testReviewEvent)+feedEvent(testStateEvent),
		false, false,
	)
	f, _ := feedWithScript(t, body)
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	defer cancel()
	waitBarrier(t, body.exhausted)
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
	body := newScriptedBody(feedEvent(testStateEvent), false, true)
	f, _ := feedWithScript(t, body)
	fired := silenceFrom(f)
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	defer cancel()
	first := nextSignal(t, signals)
	if first.Kind != contracts.PullRequestStateChanged {
		t.Fatalf("first signal = %+v, want state changed", first)
	}
	fired.fire <- time.Time{}
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

type feedEndCase struct {
	name      string
	script    string
	hold      bool
	wantFirst bool
	silent    bool
	cancel    bool
	wantLast  error
}

func TestPullRequestEventsClosesBodyOnEveryEnd(t *testing.T) {
	cases := []feedEndCase{
		{name: "server end", script: feedEvent(testReviewEvent), wantFirst: true, wantLast: ErrFeedEnded},
		{name: "silence", script: feedEvent(testReviewEvent), hold: true, wantFirst: true, silent: true, wantLast: ErrFeedSilent},
		{name: "cancel", script: feedEvent(testReviewEvent), hold: true, wantFirst: true, cancel: true},
		{name: "error", script: "data: " + strings.Repeat("x", feedLineLimit+1) + "\n\n", hold: true, wantLast: errFeedEventTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runFeedEndCase(t, tc)
		})
	}
}

func runFeedEndCase(t *testing.T, tc feedEndCase) {
	t.Helper()
	body := newScriptedBody(tc.script, false, tc.hold)
	f, _ := feedWithScript(t, body)
	fired := silenceFrom(f)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	signals := openFeed(ctx, t, f)
	if tc.wantFirst {
		nextSignal(t, signals)
	}
	if tc.silent {
		fired.fire <- time.Time{}
	}
	if tc.cancel {
		cancel()
	}
	if tc.wantLast != nil {
		if last := nextSignal(t, signals); !errors.Is(last.Err, tc.wantLast) {
			t.Fatalf("last signal = %+v, want %v", last, tc.wantLast)
		}
	}
	expectClosed(t, signals)
	if closes := body.closes.Load(); closes != 1 {
		t.Fatalf("body closed %d times, want 1", closes)
	}
}

func TestPullRequestEventsCancelClosesIdleStream(t *testing.T) {
	body := newScriptedBody(feedEvent(testReviewEvent), false, true)
	f, _ := feedWithScript(t, body)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	signals := openFeed(ctx, t, f)
	nextSignal(t, signals)
	cancel()
	expectClosed(t, signals)
	if closes := body.closes.Load(); closes != 1 {
		t.Fatalf("body closed %d times, want 1", closes)
	}
}

func TestPullRequestEventsCancelRoundsStatistical(t *testing.T) {
	f, tr := feedWithScript(t, newScriptedBody(feedEvent(testStateEvent), true, false))
	late := 0
	for range testRoundsCancel {
		tr.body = newScriptedBody(feedEvent(testStateEvent), true, false)
		ctx, cancel := context.WithCancel(t.Context())
		signals := openFeed(ctx, t, f)
		nextSignal(t, signals)
		cancel()
		late += drainAfterCancel(t, signals)
	}
	if late != 0 {
		t.Fatalf("%d signals arrived after cancellation, want 0", late)
	}
}

func TestPullRequestEventsCancelWithPendingSignalSendsNothing(t *testing.T) {
	body := newScriptedBody(feedEvent(testReviewEvent)+feedBeat, false, true)
	f, _ := feedWithScript(t, body)
	timer := silenceFrom(f)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	signals := openFeed(ctx, t, f)
	waitResets(t, timer.resets, 2)
	cancel()
	waitBarrier(t, body.release)
	expectClosed(t, signals)
}

type failingStream struct {
	base http.RoundTripper
	err  error
}

func (t *failingStream) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet {
		return nil, t.err
	}
	return t.base.RoundTrip(req)
}

func TestPullRequestEventsDialFailureIsNotSilence(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), streamScript(t))
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	f.client.Transport = &failingStream{base: f.client.Transport, err: dialErr}
	_, err := f.PullRequestEvents(t.Context(), feedRef(t))
	if errors.Is(err, ErrFeedSilent) {
		t.Fatalf("PullRequestEvents error = %v, a dial timeout is not a silent feed", err)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("PullRequestEvents error = %v, want the dial timeout kept in the chain", err)
	}
}

func TestPullRequestEventsIgnoresMalformedEvent(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), streamThenHold(t,
		"data: {not json}\n\n",
		feedEvent(testReviewEvent),
	))
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	defer cancel()
	if signal := nextSignal(t, signals); signal.Err != nil || signal.Kind != contracts.PullRequestReviewSubmitted {
		t.Fatalf("first signal = %+v, want review submitted after the malformed event", signal)
	}
}

func TestPullRequestEventsReconnectEndsWithFeedEnded(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), streamThenHold(t, feedEnd("reconnect", "")))
	ctx, cancel := context.WithCancel(t.Context())
	signals := openFeed(ctx, t, f)
	defer cancel()
	if last := nextSignal(t, signals); !errors.Is(last.Err, ErrFeedEnded) || last.Kind != 0 {
		t.Fatalf("last signal = %+v, want ErrFeedEnded with a zero kind", last)
	}
	expectClosed(t, signals)
}

func TestPullRequestEventsSilentHeadersEndWithSilence(t *testing.T) {
	f := feedForge(t, subscribeCreated(t, nil), func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	f.client.Transport = &http.Transport{ResponseHeaderTimeout: testHeaderWait}
	if _, err := f.PullRequestEvents(t.Context(), feedRef(t)); !errors.Is(err, ErrFeedSilent) {
		t.Fatalf("PullRequestEvents error = %v, want ErrFeedSilent", err)
	}
}
