package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func mustJobRef(tb testing.TB) contracts.JobRef {
	tb.Helper()
	job, err := contracts.NewJobRef(testRepo(tb), testJobID)
	if err != nil {
		tb.Fatal(err)
	}
	return job
}

func TestJobLogReturnsOnlyTheTail(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/actions/jobs/7/logs" {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeBody(t, w, "first line\nsecond line\nthe end")
	})
	f, _ := newTestForge(t, handler)
	tail, err := f.JobLog(t.Context(), mustJobRef(t), 7)
	if err != nil {
		t.Fatal(err)
	}
	if string(tail) != "the end" {
		t.Errorf("tail = %q, want %q", tail, "the end")
	}
}

func TestJobLogRefusesLimitOutOfRangeWithoutSending(t *testing.T) {
	var calls atomic.Int32
	f, _ := newTestForge(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	for _, limit := range []int{0, -1, contracts.MaxJobLogBytes + 1} {
		if _, err := f.JobLog(t.Context(), mustJobRef(t), limit); !errors.Is(err, contracts.ErrInvalidLogLimit) {
			t.Errorf("limit %d: err = %v, want ErrInvalidLogLimit", limit, err)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("%d requests were sent for refused limits", got)
	}
}

func TestJobLogDropsAuthorizationAcrossHosts(t *testing.T) {
	seenByStorage := make(chan string, 1)
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenByStorage <- r.Header.Get("Authorization")
		writeBody(t, w, "0123456789abcdef")
	}))
	t.Cleanup(storage.Close)

	var sawToken atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawToken.Store(r.Header.Get("Authorization") == "Bearer "+testSecret)
		http.Redirect(w, r, storage.URL+"/blob/logs", http.StatusFound)
	}))
	t.Cleanup(api.Close)

	apiOnLocalhost := strings.Replace(api.URL, "127.0.0.1", "localhost", 1)
	apiEndpoints, err := parseEndpoints(apiOnLocalhost, apiOnLocalhost+"/graphql")
	if err != nil {
		t.Fatal(err)
	}
	f := newForge(mustHost(t), &fakeCreds{token: mustToken(t)}, apiEndpoints, api.Client(), fixedClock)

	tail, err := f.JobLog(t.Context(), mustJobRef(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(tail) != "cdef" {
		t.Errorf("tail = %q, want %q", tail, "cdef")
	}
	if !sawToken.Load() {
		t.Error("the API did not receive the token on the first request")
	}
	if got := <-seenByStorage; got != "" {
		t.Errorf("storage host received Authorization %q after the redirect", got)
	}
}

func TestTailBufferKeepsLastBytesAcrossChunks(t *testing.T) {
	data := strings.Repeat("ab", 200)
	tail, err := readTail(strings.NewReader(data), 3, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if string(tail) != "bab" {
		t.Errorf("tail = %q, want %q", tail, "bab")
	}
}

func TestTailBufferReturnsShortLogWhole(t *testing.T) {
	tail, err := readTail(strings.NewReader("hi"), 10, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if string(tail) != "hi" {
		t.Errorf("tail = %q, want %q", tail, "hi")
	}
}

func TestReadTailRefusesLogBeyondReadLimit(t *testing.T) {
	if _, err := readTail(strings.NewReader(strings.Repeat("x", 11)), 4, 10); !errors.Is(err, errJobLogTooLarge) {
		t.Fatalf("err = %v, want errJobLogTooLarge", err)
	}
}

func TestReadTailAcceptsLogAtReadLimit(t *testing.T) {
	tail, err := readTail(strings.NewReader("0123456789"), 4, 10)
	if err != nil {
		t.Fatal(err)
	}
	if string(tail) != "6789" {
		t.Errorf("tail = %q, want %q", tail, "6789")
	}
}

const testJobID = 7
