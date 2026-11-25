package github

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const testHeadSHA = "0123456789abcdef0123456789abcdef01234567"

func pullPayload(number int, state, mergedAt, mergeable string) map[string]any {
	return map[string]any{
		"number":             number,
		"title":              "Add thing",
		"html_url":           "https://github.com/acme/widgets/pull/5",
		"state":              state,
		"merged_at":          mergedAt,
		"draft":              false,
		"mergeable_state":    mergeable,
		"author_association": "CONTRIBUTOR",
		"user":               map[string]any{"login": "octocat", "id": 1, "type": "User"},
		"head":               map[string]any{"ref": "feature", "sha": testHeadSHA},
		"base":               map[string]any{"ref": "main", "sha": "fedcba9876543210fedcba9876543210fedcba98"},
	}
}

func TestPullRequestMapsStateAndFields(t *testing.T) {
	tests := []struct {
		name      string
		state     string
		mergedAt  string
		mergeable string
		wantState contracts.PullRequestState
		wantMerge contracts.MergeState
	}{
		{name: "open clean", state: "open", mergeable: "clean", wantState: contracts.PullRequestOpen, wantMerge: contracts.MergeStateClean},
		{name: "unstable is clean", state: "open", mergeable: "unstable", wantState: contracts.PullRequestOpen, wantMerge: contracts.MergeStateClean},
		{name: "has hooks is clean", state: "open", mergeable: "has_hooks", wantState: contracts.PullRequestOpen, wantMerge: contracts.MergeStateClean},
		{name: "dirty conflicts", state: "open", mergeable: "dirty", wantState: contracts.PullRequestOpen, wantMerge: contracts.MergeStateConflicting},
		{name: "blocked", state: "open", mergeable: "blocked", wantState: contracts.PullRequestOpen, wantMerge: contracts.MergeStateBlocked},
		{name: "behind", state: "open", mergeable: "behind", wantState: contracts.PullRequestOpen, wantMerge: contracts.MergeStateBehind},
		{name: "draft", state: "open", mergeable: "draft", wantState: contracts.PullRequestOpen, wantMerge: contracts.MergeStateDraft},
		{name: "unrecognised is unknown", state: "open", mergeable: "computing", wantState: contracts.PullRequestOpen, wantMerge: contracts.MergeStateUnknown},
		{name: "merged wins over closed", state: "closed", mergedAt: "2026-11-20T10:00:00Z", mergeable: "unknown", wantState: contracts.PullRequestMerged, wantMerge: contracts.MergeStateUnknown},
		{name: "closed unmerged", state: "closed", mergeable: "unknown", wantState: contracts.PullRequestClosed, wantMerge: contracts.MergeStateUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readPullWith(t, newFakeGitHub(t, pullPayload(5, test.state, test.mergedAt, test.mergeable)))
			if err != nil {
				t.Fatal(err)
			}
			if got.State != test.wantState {
				t.Errorf("state = %v, want %v", got.State, test.wantState)
			}
			if got.Merge != test.wantMerge {
				t.Errorf("merge = %v, want %v", got.Merge, test.wantMerge)
			}
		})
	}
}

func TestPullRequestCarriesRevisionHeadAndAuthor(t *testing.T) {
	got, err := readPullWith(t, openFake(t))
	if err != nil {
		t.Fatal(err)
	}
	revision, err := parseRevision(got.Revision)
	if err != nil {
		t.Fatalf("revision %q: %v", got.Revision, err)
	}
	if revision.head.String() != testHeadSHA || revision.pull == "" {
		t.Errorf("revision = %+v, want the head SHA and the pull request ETag", revision)
	}
	if got.HeadSHA.String() != testHeadSHA {
		t.Errorf("head SHA = %s, want %s", got.HeadSHA, testHeadSHA)
	}
	if got.Head != "feature" || got.Base != "main" || got.Title != "Add thing" {
		t.Errorf("branches or title = %q %q %q", got.Head, got.Base, got.Title)
	}
	if got.Author.Login != "octocat" || got.Author.Association != contracts.AssociationContributor || got.Author.Bot {
		t.Errorf("author = %+v", got.Author)
	}
}

func TestPullRequestWithoutSinceSendsNoConditionalHeader(t *testing.T) {
	fake := openFake(t)
	if _, err := readPullWith(t, fake); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.snapshot() {
		if call.conditional {
			t.Errorf("endpoint %d sent If-None-Match with an empty revision", call.endpoint)
		}
	}
}

func TestPullRequestRefusesForeignRepoNamespace(t *testing.T) {
	f, _ := newTestForge(t, http.NewServeMux())
	nested := mustRepo(t, "github.com/acme/team/widgets")
	if _, err := f.PullRequest(t.Context(), mustPullRef(t, nested), ""); !errors.Is(err, contracts.ErrInvalidRepoRef) {
		t.Fatalf("err = %v, want ErrInvalidRepoRef", err)
	}
}

func TestOpenPullRequestPostsDraftAndMapsResult(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/acme/widgets/pulls", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var sent map[string]any
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"title": "Add thing", "head": "feature", "base": "main", "body": "text", "draft": true}
		for key, value := range want {
			if sent[key] != value {
				t.Errorf("%s = %v, want %v", key, sent[key], value)
			}
		}
		writeJSON(t, w, http.StatusCreated, pullPayload(9, "open", "", "draft"))
	})
	f, _ := newTestForge(t, mux)
	draft := contracts.PullRequestDraft{Repo: testRepo(t), Head: "feature", Base: "main", Title: "Add thing", Body: "text", Draft: true}
	got, err := f.OpenPullRequest(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ref.Number() != 9 || got.Merge != contracts.MergeStateDraft {
		t.Errorf("opened = number %d merge %v", got.Ref.Number(), got.Merge)
	}
}

func TestOpenPullRequestRefusesZeroRepo(t *testing.T) {
	f, _ := newTestForge(t, http.NewServeMux())
	if _, err := f.OpenPullRequest(t.Context(), contracts.PullRequestDraft{}); !errors.Is(err, contracts.ErrInvalidRepoRef) {
		t.Fatalf("err = %v, want ErrInvalidRepoRef", err)
	}
}

func TestMergePullRequestSendsMethodName(t *testing.T) {
	tests := map[contracts.MergeMethod]string{
		contracts.MergeMethodMerge:  "merge",
		contracts.MergeMethodSquash: "squash",
		contracts.MergeMethodRebase: "rebase",
	}
	for method, want := range tests {
		mux := http.NewServeMux()
		mux.HandleFunc("PUT /repos/acme/widgets/pulls/5/merge", func(w http.ResponseWriter, r *http.Request) {
			var sent struct {
				MergeMethod string `json:"merge_method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Fatal(err)
			}
			if sent.MergeMethod != want {
				t.Errorf("merge_method = %q, want %q", sent.MergeMethod, want)
			}
			writeJSON(t, w, http.StatusOK, map[string]bool{"merged": true})
		})
		f, _ := newTestForge(t, mux)
		if err := f.MergePullRequest(t.Context(), mustPullRef(t, testRepo(t)), method); err != nil {
			t.Errorf("%v: %v", method, err)
		}
	}
}

func TestMergePullRequestRefusesUnknownMethodWithoutSending(t *testing.T) {
	f, _ := newTestForge(t, http.NewServeMux())
	err := f.MergePullRequest(t.Context(), mustPullRef(t, testRepo(t)), 0)
	if !errors.Is(err, errInvalidMergeMethod) {
		t.Fatalf("err = %v, want errInvalidMergeMethod", err)
	}
}

func TestMergePullRequestRefusalReadsStateBack(t *testing.T) {
	for _, status := range []int{http.StatusMethodNotAllowed, http.StatusConflict} {
		fake := newFakeGitHub(t, pullPayload(5, "open", "", "blocked"))
		fake.mergeCode = status
		fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "failure")}
		fake.reviews = []map[string]any{reviewPayload(3, "APPROVED", "2026-11-20T09:00:00Z")}
		f, _ := newTestForge(t, fake)
		var refused *contracts.ForgeMergeRefusedError
		err := f.MergePullRequest(t.Context(), mustPullRef(t, testRepo(t)), contracts.MergeMethodSquash)
		if !errors.As(err, &refused) {
			t.Fatalf("status %d: err = %v, want ForgeMergeRefusedError", status, err)
		}
		if refused.State != contracts.MergeStateBlocked {
			t.Errorf("status %d: refused state = %v, want blocked", status, refused.State)
		}
		if got := len(fake.callsTo(endpointPull)); got != 1 {
			t.Errorf("status %d: pull request read %d times, want 1", status, got)
		}
		for _, kind := range []endpoint{endpointCheckRuns, endpointStatuses, endpointReviews} {
			if got := len(fake.callsTo(kind)); got != 0 {
				t.Errorf("status %d: endpoint %d read %d times on a merge refusal, want 0", status, kind, got)
			}
		}
	}
}

func TestMergePullRequestSucceedsOnOK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /repos/acme/widgets/pulls/5/merge", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]bool{"merged": true})
	})
	f, _ := newTestForge(t, mux)
	if err := f.MergePullRequest(t.Context(), mustPullRef(t, testRepo(t)), contracts.MergeMethodMerge); err != nil {
		t.Fatal(err)
	}
}
