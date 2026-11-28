package github

import (
	"errors"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/forgetest"
)

func timedIssuePayload(number int, updated time.Time) map[string]any {
	payload := issuePayload(number, "open")
	payload["created_at"] = updated.Add(-time.Hour).Format(time.RFC3339)
	payload["updated_at"] = updated.Format(time.RFC3339)
	return payload
}

func issueNumbers(items []contracts.Issue) []int {
	numbers := make([]int, 0, len(items))
	for index := range items {
		numbers = append(numbers, items[index].Ref.Number())
	}
	return numbers
}

func mustForgeLogin(tb testing.TB, text string) contracts.ForgeLogin {
	tb.Helper()
	login, err := contracts.ParseForgeLogin(text)
	if err != nil {
		tb.Fatal(err)
	}
	return login
}

func TestIssuesUpdatedSinceIncludesTheBoundAndWidensTheRequest(t *testing.T) {
	bound := time.Date(2026, time.November, 20, 10, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("since"); got != "2026-11-20T09:59:59Z" {
			t.Errorf("since = %q, want one second before the bound", got)
		}
		writeJSON(t, w, http.StatusOK, []any{
			timedIssuePayload(1, bound.Add(-time.Second)),
			timedIssuePayload(2, bound),
			timedIssuePayload(3, bound.Add(time.Second)),
		})
	})
	f, _ := newTestForge(t, mux)
	page, err := f.Issues(t.Context(), testRepo(t), contracts.IssueQuery{UpdatedSince: bound}, contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueNumbers(page.Items); !slices.Equal(got, []int{2, 3}) {
		t.Errorf("issues = %v, want [2 3]: the bound is inclusive and the older issue is dropped locally", got)
	}
}

func TestIssuesWithoutUpdatedSinceSendsNoSince(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("since") {
			t.Errorf("query = %v, want no since parameter", r.URL.Query())
		}
		writeJSON(t, w, http.StatusOK, []any{})
	})
	f, _ := newTestForge(t, mux)
	if _, err := f.Issues(t.Context(), testRepo(t), contracts.IssueQuery{}, contracts.PageRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestIssuesSendsTheMentionedLogin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("mentioned"); got != "octocat" {
			t.Errorf("mentioned = %q, want octocat", got)
		}
		writeJSON(t, w, http.StatusOK, []any{issuePayload(3, "open")})
	})
	f, _ := newTestForge(t, mux)
	query := contracts.IssueQuery{Mentioned: mustForgeLogin(t, "octocat")}
	page, err := f.Issues(t.Context(), testRepo(t), query, contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueNumbers(page.Items); !slices.Equal(got, []int{3}) {
		t.Errorf("issues = %v, want [3]", got)
	}
}

func TestIssuesSendsSortAndDirection(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sort") != "updated" || r.URL.Query().Get("direction") != "asc" {
			t.Errorf("query = %v, want sort=updated and direction=asc", r.URL.Query())
		}
		writeJSON(t, w, http.StatusOK, []any{})
	})
	f, _ := newTestForge(t, mux)
	query := contracts.IssueQuery{Sort: contracts.IssueSortUpdated, Direction: contracts.SortAscending}
	if _, err := f.Issues(t.Context(), testRepo(t), query, contracts.PageRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestIssuesFollowsEveryPageOfAListing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("per_page"); got != "2" {
			t.Errorf("per_page = %q, want 2", got)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set(headerLink, `<https://api.github.com/repos/acme/widgets/issues?page=2>; rel="next"`)
			writeJSON(t, w, http.StatusOK, []any{issuePayload(1, "open"), issuePayload(2, "open")})
		case "2":
			writeJSON(t, w, http.StatusOK, []any{issuePayload(3, "open")})
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	})
	f, _ := newTestForge(t, mux)
	repo := testRepo(t)
	first, err := f.Issues(t.Context(), repo, contracts.IssueQuery{}, contracts.PageRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueNumbers(first.Items); !slices.Equal(got, []int{1, 2}) || first.Next != "2" {
		t.Fatalf("first page = %v next %q, want [1 2] next 2", got, first.Next)
	}
	second, err := f.Issues(t.Context(), repo, contracts.IssueQuery{}, contracts.PageRequest{Cursor: first.Next, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueNumbers(second.Items); !slices.Equal(got, []int{3}) || second.Next != "" {
		t.Errorf("last page = %v next %q, want [3] and no next", got, second.Next)
	}
}

func TestIssuesSinceAnswersNotModifiedForTheSameRead(t *testing.T) {
	const etag = `"v1"`
	var conditional atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerETag, etag)
		if r.Header.Get(headerIfNoneMatch) == etag {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(t, w, http.StatusOK, []any{issuePayload(3, "open")})
	})
	f, _ := newTestForge(t, mux)
	repo := testRepo(t)
	query := contracts.IssueQuery{Labels: []string{"bug"}}
	first, err := f.IssuesSince(t.Context(), repo, query, contracts.PageRequest{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == "" {
		t.Fatal("revision is empty, want the ETag of the read")
	}
	_, err = f.IssuesSince(t.Context(), repo, query, contracts.PageRequest{}, first.Revision)
	if !errors.Is(err, contracts.ErrForgeNotModified) {
		t.Fatalf("err = %v, want ErrForgeNotModified", err)
	}
	if conditional.Load() != 1 {
		t.Errorf("conditional requests = %d, want 1", conditional.Load())
	}
}

func TestIssuesSinceReturnsTheNewRevisionWhenTheListingChanged(t *testing.T) {
	var version atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		etag := `"v1"`
		if version.Load() == 1 {
			etag = `"v2"`
		}
		w.Header().Set(headerETag, etag)
		if r.Header.Get(headerIfNoneMatch) == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(t, w, http.StatusOK, []any{issuePayload(3, "open")})
	})
	f, _ := newTestForge(t, mux)
	repo := testRepo(t)
	query := contracts.IssueQuery{}
	first, err := f.IssuesSince(t.Context(), repo, query, contracts.PageRequest{}, "")
	if err != nil {
		t.Fatal(err)
	}
	version.Store(1)
	second, err := f.IssuesSince(t.Context(), repo, query, contracts.PageRequest{}, first.Revision)
	if err != nil {
		t.Fatalf("err = %v, want the changed listing", err)
	}
	etag, bound := contracts.IssueListingETag(second.Revision, repo, query, contracts.PageRequest{})
	if !bound || etag != `"v2"` {
		t.Errorf("revision etag = %q bound %v, want \"v2\" bound to the read", etag, bound)
	}
}

func TestIssuesMapsCreatedAndUpdatedAt(t *testing.T) {
	updated := time.Date(2026, time.November, 20, 10, 0, 0, 0, time.UTC)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, []any{timedIssuePayload(3, updated)})
	})
	f, _ := newTestForge(t, mux)
	page, err := f.Issues(t.Context(), testRepo(t), contracts.IssueQuery{}, contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %+v, want one issue", page.Items)
	}
	got := page.Items[0]
	if !got.UpdatedAt.Equal(updated) || !got.CreatedAt.Equal(updated.Add(-time.Hour)) {
		t.Errorf("created %v updated %v, want %v and %v", got.CreatedAt, got.UpdatedAt, updated.Add(-time.Hour), updated)
	}
}

func TestIssuesSinceRefusesInvalidInputsWithoutSending(t *testing.T) {
	var calls atomic.Int32
	f, _ := newTestForge(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	forgetest.CheckConditionalIssuesRefusesInvalidInput(t, f, testRepo(t))
	if calls.Load() != 0 {
		t.Fatalf("%d requests sent for refused listings, want none", calls.Load())
	}
}

func TestIssuesSinceBindsEachRevisionToItsRead(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerETag, `"v1"`)
		if r.Header.Get(headerIfNoneMatch) == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(t, w, http.StatusOK, []any{issuePayload(3, "open")})
	})
	f, _ := newTestForge(t, mux)
	forgetest.CheckConditionalIssuesBindsRevisions(t, f, testRepo(t))
}
