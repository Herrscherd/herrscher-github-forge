package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func testNow() time.Time {
	return time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC)
}

const testSecret = "ghp_testSecretValue1234567890"

func fixedClock() time.Time {
	return testNow()
}

type fakeCreds struct {
	token       contracts.ForgeToken
	err         error
	invalidated int
}

func (c *fakeCreds) Token(context.Context, contracts.ForgeHost) (contracts.ForgeToken, error) {
	if c.err != nil {
		return contracts.ForgeToken{}, c.err
	}
	return c.token, nil
}

func (c *fakeCreds) Invalidate(context.Context, contracts.ForgeHost) {
	c.invalidated++
}

func mustToken(tb testing.TB) contracts.ForgeToken {
	tb.Helper()
	token, err := contracts.NewForgeToken(testSecret, "octocat", contracts.TokenSourceSaved)
	if err != nil {
		tb.Fatal(err)
	}
	return token
}

func mustHost(tb testing.TB) contracts.ForgeHost {
	tb.Helper()
	host, err := contracts.ParseForgeHost(publicHost)
	if err != nil {
		tb.Fatal(err)
	}
	return host
}

func mustRepo(tb testing.TB, text string) contracts.ForgeRepoRef {
	tb.Helper()
	repo, err := contracts.ParseRepoRef(text)
	if err != nil {
		tb.Fatal(err)
	}
	return repo
}

func mustPullRef(tb testing.TB, repo contracts.ForgeRepoRef) contracts.PullRequestRef {
	tb.Helper()
	ref, err := contracts.NewPullRequestRef(repo, testPullNumber)
	if err != nil {
		tb.Fatal(err)
	}
	return ref
}

func mustIssueRef(tb testing.TB, repo contracts.ForgeRepoRef, number int) contracts.IssueRef {
	tb.Helper()
	ref, err := contracts.NewIssueRef(repo, number)
	if err != nil {
		tb.Fatal(err)
	}
	return ref
}

func testRepo(tb testing.TB) contracts.ForgeRepoRef {
	tb.Helper()
	return mustRepo(tb, "github.com/acme/widgets")
}

func newTestForge(tb testing.TB, handler http.Handler) (*forge, *fakeCreds) {
	tb.Helper()
	server := httptest.NewServer(handler)
	tb.Cleanup(server.Close)
	api, err := parseEndpoints(server.URL, server.URL+"/graphql")
	if err != nil {
		tb.Fatal(err)
	}
	creds := &fakeCreds{token: mustToken(tb)}
	return newForge(mustHost(tb), creds, api, server.Client(), fixedClock), creds
}

func writeJSON(tb testing.TB, w http.ResponseWriter, status int, payload any) {
	tb.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		tb.Errorf("encode response: %v", err)
	}
}

func graphQLData(tb testing.TB, w http.ResponseWriter, data any) {
	tb.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		tb.Fatal(err)
	}
	writeJSON(tb, w, http.StatusOK, map[string]json.RawMessage{"data": raw})
}

func writeBody(tb testing.TB, w io.Writer, body string) {
	tb.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		tb.Errorf("write response: %v", err)
	}
}

const testPullNumber = 5

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func mustCommit(tb testing.TB) contracts.CommitSHA {
	tb.Helper()
	commit, err := contracts.ParseCommitSHA(testCommit)
	if err != nil {
		tb.Fatal(err)
	}
	return commit
}

type sentGraphQL struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func graphQLHandler(t *testing.T, seen *sentGraphQL, data json.RawMessage) http.Handler {
	t.Helper()
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			t.Errorf("GraphQL path = %q", r.URL.Path)
		}
		if seen != nil {
			if err := json.NewDecoder(r.Body).Decode(seen); err != nil {
				t.Fatal(err)
			}
		}
		graphQLData(t, w, data)
	})
}
