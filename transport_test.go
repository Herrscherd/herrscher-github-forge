package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func repoAccessHandler(t *testing.T, status int, headers map[string]string, check func(r *http.Request)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		for key, value := range headers {
			w.Header().Set(key, value)
		}
		if status >= http.StatusBadRequest {
			w.WriteHeader(status)
			writeBody(t, w, `{"message":"refused"}`)
			return
		}
		writeJSON(t, w, status, map[string]any{"permissions": map[string]bool{"pull": true}})
	})
}

func TestRequestsCarryAuthAndVersionHeaders(t *testing.T) {
	var got http.Header
	handler := repoAccessHandler(t, http.StatusOK, nil, func(r *http.Request) { got = r.Header.Clone() })
	f, _ := newTestForge(t, handler)
	if _, err := f.RepoAccess(t.Context(), testRepo(t)); err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"Authorization":        "Bearer " + testSecret,
		"Accept":               "application/vnd.github+json",
		"X-Github-Api-Version": "2022-11-28",
	}
	for name, want := range checks {
		if value := got.Get(name); value != want {
			t.Errorf("header %s = %q, want %q", name, value, want)
		}
	}
}

func TestUnauthorizedInvalidatesAndMapsToSentinel(t *testing.T) {
	f, creds := newTestForge(t, repoAccessHandler(t, http.StatusUnauthorized, nil, nil))
	_, err := f.RepoAccess(t.Context(), testRepo(t))
	if !errors.Is(err, contracts.ErrForgeUnauthorized) {
		t.Fatalf("err = %v, want ErrForgeUnauthorized", err)
	}
	if creds.invalidated != 1 {
		t.Errorf("Invalidate called %d times, want 1", creds.invalidated)
	}
}

func TestForbiddenWithoutThrottleMapsToForbidden(t *testing.T) {
	f, _ := newTestForge(t, repoAccessHandler(t, http.StatusForbidden, nil, nil))
	if _, err := f.RepoAccess(t.Context(), testRepo(t)); !errors.Is(err, contracts.ErrForgeForbidden) {
		t.Fatalf("err = %v, want ErrForgeForbidden", err)
	}
}

func TestForbiddenWithExhaustedQuotaMapsToRateLimited(t *testing.T) {
	headers := map[string]string{headerRateRemaining: "0", headerRateReset: "1798000000"}
	f, _ := newTestForge(t, repoAccessHandler(t, http.StatusForbidden, headers, nil))
	var limited *contracts.ForgeRateLimitedError
	if _, err := f.RepoAccess(t.Context(), testRepo(t)); !errors.As(err, &limited) {
		t.Fatalf("err = %v, want ForgeRateLimitedError", err)
	}
	if limited.Reset.Unix() != 1798000000 {
		t.Errorf("reset = %v, want the header reset", limited.Reset)
	}
}

func TestForbiddenWithRetryAfterMapsToRateLimited(t *testing.T) {
	f, _ := newTestForge(t, repoAccessHandler(t, http.StatusForbidden, map[string]string{headerRetryAfter: "60"}, nil))
	var limited *contracts.ForgeRateLimitedError
	if _, err := f.RepoAccess(t.Context(), testRepo(t)); !errors.As(err, &limited) {
		t.Fatalf("err = %v, want ForgeRateLimitedError", err)
	}
	if !limited.Reset.Equal(testNow().Add(time.Minute)) {
		t.Errorf("reset = %v, want one minute after the clock", limited.Reset)
	}
}

func TestTooManyRequestsMapsToRateLimited(t *testing.T) {
	f, _ := newTestForge(t, repoAccessHandler(t, http.StatusTooManyRequests, nil, nil))
	var limited *contracts.ForgeRateLimitedError
	if _, err := f.RepoAccess(t.Context(), testRepo(t)); !errors.As(err, &limited) {
		t.Fatalf("err = %v, want ForgeRateLimitedError", err)
	}
}

func TestNotFoundMapsToSentinel(t *testing.T) {
	f, _ := newTestForge(t, repoAccessHandler(t, http.StatusNotFound, nil, nil))
	if _, err := f.RepoAccess(t.Context(), testRepo(t)); !errors.Is(err, contracts.ErrForgeNotFound) {
		t.Fatalf("err = %v, want ErrForgeNotFound", err)
	}
}

func TestUnexpectedStatusIsAnError(t *testing.T) {
	f, _ := newTestForge(t, repoAccessHandler(t, http.StatusInternalServerError, nil, nil))
	if _, err := f.RepoAccess(t.Context(), testRepo(t)); !errors.Is(err, errUnexpectedStatus) {
		t.Fatalf("err = %v, want errUnexpectedStatus", err)
	}
}

func TestErrorsNeverCarryTheToken(t *testing.T) {
	statuses := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError}
	for _, status := range statuses {
		f, _ := newTestForge(t, repoAccessHandler(t, status, nil, nil))
		_, err := f.RepoAccess(t.Context(), testRepo(t))
		if err == nil {
			t.Fatalf("status %d: no error", status)
		}
		if strings.Contains(err.Error(), testSecret) {
			t.Errorf("status %d: error text leaks the token: %q", status, err)
		}
	}
}

func TestMissingCredentialStopsBeforeSending(t *testing.T) {
	var calls atomic.Int32
	f, creds := newTestForge(t, repoAccessHandler(t, http.StatusOK, nil, func(*http.Request) { calls.Add(1) }))
	creds.err = contracts.ErrForgeNotConnected
	if _, err := f.RepoAccess(t.Context(), testRepo(t)); !errors.Is(err, contracts.ErrForgeNotConnected) {
		t.Fatalf("err = %v, want ErrForgeNotConnected", err)
	}
	if calls.Load() != 0 {
		t.Error("a request was sent without a credential")
	}
}

func TestEmptyTokenIsNotConnected(t *testing.T) {
	f, creds := newTestForge(t, repoAccessHandler(t, http.StatusOK, nil, nil))
	creds.token = contracts.ForgeToken{}
	if _, err := f.RepoAccess(t.Context(), testRepo(t)); !errors.Is(err, contracts.ErrForgeNotConnected) {
		t.Fatalf("err = %v, want ErrForgeNotConnected", err)
	}
}

func TestSemaphoreLimitsInFlightRequests(t *testing.T) {
	f, _ := newTestForge(t, repoAccessHandler(t, http.StatusOK, nil, nil))
	for range maxInFlight {
		f.slots <- struct{}{}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.RepoAccess(ctx, testRepo(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled while every slot is taken", err)
	}
	for range maxInFlight {
		<-f.slots
	}
	if _, err := f.RepoAccess(t.Context(), testRepo(t)); err != nil {
		t.Errorf("after slots are freed: %v", err)
	}
}

func TestSemaphoreAdmitsFourAtATime(t *testing.T) {
	var inFlight, peak atomic.Int32
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := inFlight.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		inFlight.Add(-1)
		writeJSON(t, w, http.StatusOK, map[string]any{"permissions": make(map[string]bool)})
	})
	f, _ := newTestForge(t, handler)
	done := make(chan error, 6)
	for range 6 {
		go func() {
			_, err := f.RepoAccess(t.Context(), testRepo(t))
			done <- err
		}()
	}
	for range maxInFlight {
		<-entered
	}
	select {
	case <-entered:
		t.Fatal("a fifth request reached the server while four were in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range 6 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	if got := peak.Load(); got != maxInFlight {
		t.Errorf("peak in flight = %d, want %d", got, maxInFlight)
	}
}

func TestReadBoundedRefusesOversizedBody(t *testing.T) {
	body := strings.NewReader(strings.Repeat("a", maxResponseBytes+1))
	if _, err := readBounded(body); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("err = %v, want errResponseTooLarge", err)
	}
}

func TestReadBoundedKeepsBodyAtLimit(t *testing.T) {
	body := strings.NewReader(strings.Repeat("a", maxResponseBytes))
	data, err := readBounded(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != maxResponseBytes {
		t.Errorf("read %d bytes, want %d", len(data), maxResponseBytes)
	}
}

func TestTooManyRedirectsStopsTheChain(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+"/loop", http.StatusFound)
	}))
	t.Cleanup(server.Close)
	api, err := parseEndpoints(server.URL, server.URL+"/graphql")
	if err != nil {
		t.Fatal(err)
	}
	f := newForge(mustHost(t), &fakeCreds{token: mustToken(t)}, api, server.Client(), fixedClock)
	_, err = f.JobLog(t.Context(), mustJobRef(t), 16)
	if !errors.Is(err, errTooManyRedirects) {
		t.Fatalf("err = %v, want errTooManyRedirects", err)
	}
}

func jobLogForge(t *testing.T, server *httptest.Server) *forge {
	t.Helper()
	api, err := parseEndpoints(server.URL, server.URL+"/graphql")
	if err != nil {
		t.Fatal(err)
	}
	return newForge(mustHost(t), &fakeCreds{token: mustToken(t)}, api, server.Client(), fixedClock)
}

func TestRedirectToSiblingPortDropsAuthorization(t *testing.T) {
	seenBySibling := make(chan string, 1)
	sibling := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBySibling <- r.Header.Get(headerAuthorization)
		writeBody(t, w, "0123456789abcdef")
	}))
	t.Cleanup(sibling.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sibling.URL+"/blob/logs", http.StatusFound)
	}))
	t.Cleanup(api.Close)

	if _, err := jobLogForge(t, api).JobLog(t.Context(), mustJobRef(t), 4); err != nil {
		t.Fatal(err)
	}
	if got := <-seenBySibling; got != "" {
		t.Errorf("sibling port received Authorization %q after the redirect", got)
	}
}

func TestRedirectFromHTTPSToHTTPDropsAuthorization(t *testing.T) {
	seenByPlain := make(chan string, 1)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenByPlain <- r.Header.Get(headerAuthorization)
		writeBody(t, w, "0123456789abcdef")
	}))
	t.Cleanup(plain.Close)
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/blob/logs", http.StatusFound)
	}))
	t.Cleanup(secure.Close)

	tail, err := jobLogForge(t, secure).JobLog(t.Context(), mustJobRef(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(tail) != "cdef" {
		t.Errorf("tail = %q, want %q", tail, "cdef")
	}
	if got := <-seenByPlain; got != "" {
		t.Errorf("plain HTTP host received Authorization %q after an HTTPS redirect", got)
	}
}

func TestDropAuthorizationOnSchemeChangeWithSameHostAndPort(t *testing.T) {
	origin, err := url.Parse("https://api.example.test/graphql")
	if err != nil {
		t.Fatal(err)
	}
	downgraded, err := url.Parse("http://api.example.test/graphql")
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{URL: downgraded, Header: http.Header{}}
	req.Header.Set(headerAuthorization, "Bearer "+testSecret)
	if err := dropAuthorizationAcrossOrigins(req, []*http.Request{{URL: origin}}); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get(headerAuthorization); got != "" {
		t.Errorf("Authorization after an https to http redirect = %q, want none", got)
	}
}

func TestSameOriginRedirectKeepsAuthorization(t *testing.T) {
	origin, err := url.Parse("https://api.example.test/graphql")
	if err != nil {
		t.Fatal(err)
	}
	req := &http.Request{URL: origin, Header: http.Header{}}
	req.Header.Set(headerAuthorization, "Bearer "+testSecret)
	if err := dropAuthorizationAcrossOrigins(req, []*http.Request{{URL: origin}}); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get(headerAuthorization); got != "Bearer "+testSecret {
		t.Errorf("Authorization on the same origin = %q, want it kept", got)
	}
}

func TestGraphQLIsNotRefusedByExhaustedRESTQuota(t *testing.T) {
	seen := false
	mux := http.NewServeMux()
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		seen = true
		graphQLData(t, w, make(map[string]any))
	})
	mux.HandleFunc("GET /repos/acme/widgets/pulls/5", func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("REST request sent although the core quota is exhausted")
	})
	f, _ := newTestForge(t, mux)
	f.quota.observe(rateHeader(0, testNow().Add(time.Hour)))
	var out map[string]any
	if err := f.graphQL(t.Context(), "query{viewer{login}}", nil, &out); err != nil {
		t.Fatalf("GraphQL err = %v, want nil", err)
	}
	if !seen {
		t.Error("GraphQL request was not sent")
	}
	var limited *contracts.ForgeRateLimitedError
	if _, err := f.PullRequest(t.Context(), mustPullRef(t, testRepo(t)), ""); !errors.As(err, &limited) {
		t.Fatalf("REST err = %v, want ForgeRateLimitedError", err)
	}
}
