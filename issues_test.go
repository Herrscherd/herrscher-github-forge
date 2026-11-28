package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/forgetest"
)

func issuePayload(number int, state string) map[string]any {
	return map[string]any{
		"number":             number,
		"title":              "Bug",
		"body":               "it breaks",
		"html_url":           "https://github.com/acme/widgets/issues/3",
		"state":              state,
		"author_association": "CONTRIBUTOR",
		"user":               map[string]any{"login": "octocat", "id": 1, "type": "User"},
		"labels":             []map[string]string{{"name": "bug"}, {"name": "ui"}},
	}
}

func TestIssueMapsFields(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues/3", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, issuePayload(3, "open"))
	})
	f, _ := newTestForge(t, mux)
	got, err := f.Issue(t.Context(), mustIssueRef(t, testRepo(t), 3))
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref.Number() != 3 || got.Title != "Bug" || got.Body != "it breaks" || got.URL == "" {
		t.Errorf("issue = %+v", got)
	}
	if got.State != contracts.IssueOpen || len(got.Labels) != 2 || got.Labels[1] != "ui" {
		t.Errorf("state or labels = %v %q", got.State, got.Labels)
	}
	if got.Author.Login != "octocat" || got.Author.Association != contracts.AssociationContributor {
		t.Errorf("author = %+v", got.Author)
	}
}

func TestIssueUnknownStateIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues/3", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, issuePayload(3, "archived"))
	})
	f, _ := newTestForge(t, mux)
	if _, err := f.Issue(t.Context(), mustIssueRef(t, testRepo(t), 3)); !errors.Is(err, errUnknownIssueState) {
		t.Fatalf("err = %v, want errUnknownIssueState", err)
	}
}

func TestIssueNotFound(t *testing.T) {
	f, _ := newTestForge(t, http.NewServeMux())
	if _, err := f.Issue(t.Context(), mustIssueRef(t, testRepo(t), 3)); !errors.Is(err, contracts.ErrForgeNotFound) {
		t.Fatalf("err = %v, want ErrForgeNotFound", err)
	}
}

func TestIssuesMarksPullRequestsAndFollowsLink(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != "closed" || q.Get("labels") != "bug,ui" || q.Get("per_page") != "30" || q.Get("page") != "1" {
			t.Errorf("query = %v", q)
		}
		pr := issuePayload(4, "open")
		pr["pull_request"] = map[string]string{"url": "x"}
		w.Header().Set("Link", `<https://api.github.com/repos/acme/widgets/issues?page=2>; rel="next", <https://api.github.com/repos/acme/widgets/issues?page=9>; rel="last"`)
		writeJSON(t, w, http.StatusOK, []any{issuePayload(3, "closed"), pr})
	})
	f, _ := newTestForge(t, mux)
	query := contracts.IssueQuery{Labels: []string{"bug", "ui"}, State: contracts.IssueClosed}
	page, err := f.Issues(t.Context(), testRepo(t), query, contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Ref.Number() != 3 || page.Items[1].Ref.Number() != 4 {
		t.Fatalf("items = %+v, want issues 3 and 4 in the forge order", page.Items)
	}
	if page.Items[0].State != contracts.IssueClosed || page.Items[0].IsPullRequest {
		t.Errorf("issue 3 = %+v, want a closed issue that is not a pull request", page.Items[0])
	}
	if !page.Items[1].IsPullRequest {
		t.Errorf("issue 4 = %+v, want it marked as a pull request", page.Items[1])
	}
	if page.Next != "2" {
		t.Errorf("next = %q, want 2", page.Next)
	}
}

func TestIssuesDefaultsToOpenAndSendsPageCursor(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("state") != "open" || q.Get("page") != "3" || q.Get("per_page") != "10" {
			t.Errorf("query = %v", q)
		}
		writeJSON(t, w, http.StatusOK, []any{})
	})
	f, _ := newTestForge(t, mux)
	page, err := f.Issues(t.Context(), testRepo(t), contracts.IssueQuery{}, contracts.PageRequest{Cursor: "3", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Next != "" {
		t.Errorf("last page = %+v", page)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d", calls.Load())
	}
}

func TestIssuesRefusesInvalidInputsWithoutSending(t *testing.T) {
	var calls atomic.Int32
	f, _ := newTestForge(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	repo := testRepo(t)
	if _, err := f.Issues(t.Context(), repo, contracts.IssueQuery{}, contracts.PageRequest{Cursor: "next-please"}); !errors.Is(err, errInvalidCursor) {
		t.Errorf("bad cursor: err = %v, want errInvalidCursor", err)
	}
	if _, err := f.Issues(t.Context(), repo, contracts.IssueQuery{}, contracts.PageRequest{Limit: 101}); !errors.Is(err, contracts.ErrInvalidPageLimit) {
		t.Errorf("limit 101: err = %v, want ErrInvalidPageLimit", err)
	}
	if _, err := f.Issues(t.Context(), repo, contracts.IssueQuery{State: contracts.IssueState(99)}, contracts.PageRequest{}); !errors.Is(err, contracts.ErrInvalidIssueQuery) {
		t.Errorf("unknown state: err = %v, want ErrInvalidIssueQuery", err)
	}
	if calls.Load() != 0 {
		t.Error("a refused listing reached the server")
	}
}

func TestIssueRefusesPullRequestNumber(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues/4", func(w http.ResponseWriter, _ *http.Request) {
		pr := issuePayload(4, "open")
		pr["pull_request"] = map[string]string{"url": "x"}
		writeJSON(t, w, http.StatusOK, pr)
	})
	f, _ := newTestForge(t, mux)
	_, err := f.Issue(t.Context(), mustIssueRef(t, testRepo(t), 4))
	if !errors.Is(err, ErrNamesPullRequest) {
		t.Fatalf("err = %v, want ErrNamesPullRequest", err)
	}
	if !errors.Is(err, contracts.ErrForgeNotFound) {
		t.Errorf("err = %v, want it to match ErrForgeNotFound too", err)
	}
}

func TestIssuesRefusesLabelWithComma(t *testing.T) {
	var calls atomic.Int32
	f, _ := newTestForge(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	query := contracts.IssueQuery{Labels: []string{"bug", "ui,mobile"}}
	_, err := f.Issues(t.Context(), testRepo(t), query, contracts.PageRequest{})
	if !errors.Is(err, ErrInvalidLabel) {
		t.Fatalf("err = %v, want ErrInvalidLabel", err)
	}
	if !errors.Is(err, contracts.ErrUnsupportedIssueFilter) {
		t.Errorf("err = %v, want ErrUnsupportedIssueFilter: the forge cannot filter by a comma label", err)
	}
	if calls.Load() != 0 {
		t.Error("a listing with a comma label reached the server")
	}
}

func TestIssuesRefusesEmptyLabelWithoutSending(t *testing.T) {
	queries := []struct {
		name   string
		labels []string
	}{
		{name: "empty label", labels: []string{""}},
		{name: "blank label", labels: []string{"   "}},
		{name: "empty label after a valid one", labels: []string{"bug", ""}},
	}
	for _, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			var calls atomic.Int32
			f, _ := newTestForge(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			_, err := f.Issues(t.Context(), testRepo(t), contracts.IssueQuery{Labels: query.labels}, contracts.PageRequest{})
			if !errors.Is(err, ErrInvalidLabel) {
				t.Errorf("err = %v, want ErrInvalidLabel", err)
			}
			if !errors.Is(err, contracts.ErrUnsupportedIssueFilter) {
				t.Errorf("err = %v, want ErrUnsupportedIssueFilter", err)
			}
			if calls.Load() != 0 {
				t.Error("a listing with an empty label reached the server")
			}
		})
	}
}

func TestNextPageCursorReadsLinkRelation(t *testing.T) {
	tests := []struct {
		name string
		link string
		want contracts.PageCursor
	}{
		{name: "empty header", link: "", want: ""},
		{name: "next present", link: `<https://api.github.com/x?page=4>; rel="next"`, want: "4"},
		{name: "only last", link: `<https://api.github.com/x?page=9>; rel="last"`, want: ""},
		{name: "next among others", link: `<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=6>; rel="next"`, want: "6"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := nextPageCursor(test.link)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("cursor = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPageNumberAndLimit(t *testing.T) {
	if n, err := pageNumber(""); err != nil || n != 1 {
		t.Errorf("empty cursor = %d, %v; want 1", n, err)
	}
	if _, err := pageNumber("0"); !errors.Is(err, errInvalidCursor) {
		t.Errorf("cursor 0: err = %v, want errInvalidCursor", err)
	}
	if n, err := pageLimit(contracts.PageRequest{}); err != nil || n != defaultPageSize {
		t.Errorf("default limit = %d, %v", n, err)
	}
}

func TestOpenIssuePostsTheDraftAndMapsTheIssue(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/acme/widgets/issues", func(w http.ResponseWriter, r *http.Request) {
		var sent struct {
			Title  string   `json:"title"`
			Body   string   `json:"body"`
			Labels []string `json:"labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		if sent.Title != "Bug" || sent.Body != "it breaks" || len(sent.Labels) != 1 || sent.Labels[0] != "bug" {
			t.Errorf("sent = %+v", sent)
		}
		writeJSON(t, w, http.StatusCreated, issuePayload(12, "open"))
	})
	f, _ := newTestForge(t, mux)
	got, err := f.OpenIssue(t.Context(), contracts.IssueDraft{Repo: testRepo(t), Title: "Bug", Body: "it breaks", Labels: []string{"bug"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref.Number() != 12 || got.State != contracts.IssueOpen || got.URL == "" {
		t.Errorf("opened = %+v", got)
	}
}

func TestCommentOnIssuePostsTheBody(t *testing.T) {
	var posted atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues/3", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, issuePayload(3, "open"))
	})
	mux.HandleFunc("POST /repos/acme/widgets/issues/3/comments", func(w http.ResponseWriter, r *http.Request) {
		var sent struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		if sent.Body != "fixed in #4" {
			t.Errorf("body = %q", sent.Body)
		}
		posted.Store(true)
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": 1})
	})
	f, _ := newTestForge(t, mux)
	if err := f.CommentOnIssue(t.Context(), mustIssueRef(t, testRepo(t), 3), "fixed in #4"); err != nil {
		t.Fatal(err)
	}
	if !posted.Load() {
		t.Fatal("the comment never reached the forge")
	}
}

func TestCommentOnIssueRefusesAPullRequestNumberWithoutPosting(t *testing.T) {
	var posted atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/issues/5", func(w http.ResponseWriter, _ *http.Request) {
		payload := issuePayload(5, "open")
		payload["pull_request"] = map[string]any{"url": "https://api.github.com/repos/acme/widgets/pulls/5"}
		writeJSON(t, w, http.StatusOK, payload)
	})
	mux.HandleFunc("POST /repos/acme/widgets/issues/5/comments", func(http.ResponseWriter, *http.Request) { posted.Store(true) })
	f, _ := newTestForge(t, mux)
	if err := f.CommentOnIssue(t.Context(), mustIssueRef(t, testRepo(t), 5), "hi"); !errors.Is(err, ErrNamesPullRequest) {
		t.Fatalf("err = %v, want ErrNamesPullRequest", err)
	}
	if posted.Load() {
		t.Fatal("a comment was posted on a pull request through the issue port")
	}
}

func TestIssueWritesRefuseEmptyTextWithoutSending(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	f, _ := newTestForge(t, mux)
	forgetest.CheckIssueWriterRefusesEmptyText(t, f, testRepo(t))
	if calls.Load() != 0 {
		t.Fatalf("%d requests sent, want none", calls.Load())
	}
}
