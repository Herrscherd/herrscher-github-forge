package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
	"github.com/Herrscherd/herrscher-contracts/forgetest"
)

func repoPayload(name string) map[string]any {
	return map[string]any{
		"name":           name,
		"owner":          map[string]string{"login": "acme"},
		"clone_url":      "https://github.com/acme/" + name + ".git",
		"description":    "a widget kit",
		"private":        true,
		"archived":       false,
		"default_branch": "main",
	}
}

func pagedRepos(t *testing.T, repos []map[string]any, calls *atomic.Int32) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("affiliation") != "owner,collaborator,organization_member" || q.Get("sort") != "full_name" || q.Get("direction") != "asc" {
			t.Errorf("query = %v", q)
		}
		limit, err := strconv.Atoi(q.Get("per_page"))
		if err != nil {
			t.Errorf("per_page = %q: %v", q.Get("per_page"), err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		number, err := strconv.Atoi(q.Get("page"))
		if err != nil {
			t.Errorf("page = %q: %v", q.Get("page"), err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		start := min((number-1)*limit, len(repos))
		end := min(start+limit, len(repos))
		if end < len(repos) {
			w.Header().Set(headerLink, `<?page=`+strconv.Itoa(number+1)+`>; rel="next"`)
		}
		writeJSON(t, w, http.StatusOK, repos[start:end])
	})
	return mux
}

func TestRepositoriesPagesFollowLink(t *testing.T) {
	var calls atomic.Int32
	repos := []map[string]any{repoPayload("one"), repoPayload("two"), repoPayload("three")}
	f, _ := newTestForge(t, pagedRepos(t, repos, &calls))
	first, err := f.Repositories(t.Context(), contracts.PageRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Next != "2" {
		t.Fatalf("first page = %d items, next %q", len(first.Items), first.Next)
	}
	second, err := f.Repositories(t.Context(), contracts.PageRequest{Cursor: first.Next, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Next != "" {
		t.Fatalf("last page = %d items, next %q", len(second.Items), second.Next)
	}
	if second.Items[0].Repo().Name() != "three" {
		t.Errorf("last repository = %q", second.Items[0].Repo())
	}
}

func TestRepositoriesDefaultLimitIsThirty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("per_page"); got != "30" {
			t.Errorf("per_page = %q, want 30", got)
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	})
	f, _ := newTestForge(t, mux)
	if _, err := f.Repositories(t.Context(), contracts.PageRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoriesExplicitLimitIsSent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("per_page"); got != "7" {
			t.Errorf("per_page = %q, want 7", got)
		}
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	})
	f, _ := newTestForge(t, mux)
	if _, err := f.Repositories(t.Context(), contracts.PageRequest{Limit: 7}); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoriesMapsFields(t *testing.T) {
	first := repoPayload("widgets")
	first["archived"] = true
	first["private"] = false
	second := repoPayload("gadgets")
	second["description"] = nil
	second["default_branch"] = ""
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{first, second})
	})
	f, _ := newTestForge(t, mux)
	page, err := f.Repositories(t.Context(), contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(page.Items))
	}
	widgets := page.Items[0]
	if widgets.Repo().String() != "github.com/acme/widgets" || widgets.CloneURL() != "https://github.com/acme/widgets.git" {
		t.Errorf("widgets = %q, %q", widgets.Repo(), widgets.CloneURL())
	}
	if widgets.Description() != "a widget kit" || widgets.Private() || !widgets.Archived() {
		t.Errorf("widgets flags = %q private %v archived %v", widgets.Description(), widgets.Private(), widgets.Archived())
	}
	if widgets.DefaultBranch().String() != "main" {
		t.Errorf("default branch = %q", widgets.DefaultBranch())
	}
	gadgets := page.Items[1]
	if gadgets.Description() != "" || !gadgets.DefaultBranch().IsZero() || !gadgets.Private() {
		t.Errorf("gadgets = description %q branch %q private %v", gadgets.Description(), gadgets.DefaultBranch(), gadgets.Private())
	}
}

func TestRepositoriesSkipsRefusedRepositories(t *testing.T) {
	badBranch := repoPayload("bad-branch")
	badBranch["default_branch"] = "feat..x"
	otherHost := repoPayload("moved")
	otherHost["clone_url"] = "https://ghe.example.com/acme/moved.git"
	kept := repoPayload("kept")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{badBranch, otherHost, kept})
	})
	f, _ := newTestForge(t, mux)
	page, err := f.Repositories(t.Context(), contracts.PageRequest{})
	if err != nil {
		t.Fatalf("a refused repository failed the page: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Repo().Name() != "kept" {
		t.Errorf("items = %+v, want only kept", page.Items)
	}
}

func TestRepositoriesRefusesInvalidLimit(t *testing.T) {
	var calls atomic.Int32
	f, _ := newTestForge(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	for _, limit := range []int{-1, contracts.MaxPageLimit + 1} {
		if _, err := f.Repositories(t.Context(), contracts.PageRequest{Limit: limit}); !errors.Is(err, contracts.ErrInvalidPageLimit) {
			t.Errorf("limit %d: err = %v, want ErrInvalidPageLimit", limit, err)
		}
	}
	if calls.Load() != 0 {
		t.Error("a request was sent for an invalid limit")
	}
}

func TestRepositoriesMapsStatusErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: contracts.ErrForgeUnauthorized},
		{name: "forbidden", status: http.StatusForbidden, want: contracts.ErrForgeForbidden},
		{name: "not found", status: http.StatusNotFound, want: contracts.ErrForgeNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, test.status, map[string]string{"message": "no"})
			})
			f, creds := newTestForge(t, mux)
			_, err := f.Repositories(t.Context(), contracts.PageRequest{})
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if test.status == http.StatusUnauthorized && creds.invalidated != 1 {
				t.Errorf("invalidated = %d, want 1", creds.invalidated)
			}
		})
	}
}

func TestRepositoriesOnEnterpriseHost(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/user/repos", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		enterprise := repoPayload("internal")
		enterprise["clone_url"] = "https://ghe.example.com/acme/internal.git"
		writeJSON(t, w, http.StatusOK, []map[string]any{enterprise})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	api, err := parseEndpoints(server.URL+enterpriseRESTPath, server.URL+enterpriseGraphQLPath)
	if err != nil {
		t.Fatal(err)
	}
	host, err := contracts.ParseForgeHost("ghe.example.com")
	if err != nil {
		t.Fatal(err)
	}
	f := newForge(host, &fakeCreds{token: mustToken(t)}, api, server.Client(), fixedClock)
	page, err := f.Repositories(t.Context(), contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(page.Items) != 1 || page.Items[0].Repo().Host() != host {
		t.Errorf("calls = %d, items = %+v", calls.Load(), page.Items)
	}
}

func TestCheckListsRepositories(t *testing.T) {
	var calls atomic.Int32
	repos := []map[string]any{repoPayload("one"), repoPayload("two"), repoPayload("three")}
	f, _ := newTestForge(t, pagedRepos(t, repos, &calls))
	forgetest.CheckListsRepositories(t, f, mustHost(t))
	if calls.Load() == 0 {
		t.Error("the check sent no listing request")
	}
}
