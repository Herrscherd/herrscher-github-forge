package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func checkRunNode(name, status, conclusion string) map[string]any {
	return map[string]any{
		"__typename": "CheckRun", "name": name, "status": status, "conclusion": conclusion,
		"detailsUrl": "", "databaseId": 1, "checkSuite": map[string]any{"app": map[string]any{"slug": "other-app"}},
	}
}

func statusContextNode(context, state string) map[string]any {
	return map[string]any{"__typename": "StatusContext", "context": context, "state": state, "targetUrl": ""}
}

func rollupData(hasNext bool, endCursor string, nodes ...map[string]any) map[string]any {
	return map[string]any{"repository": map[string]any{"object": map[string]any{"statusCheckRollup": map[string]any{
		"contexts": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": endCursor},
			"nodes":    nodes,
		},
	}}}}
}

func reviewPayload(id int64, state, submittedAt string) map[string]any {
	payload := map[string]any{"id": id, "state": state, "user": map[string]any{"login": "reviewer"}}
	if submittedAt != "" {
		payload["submitted_at"] = submittedAt
	} else {
		payload["submitted_at"] = nil
	}
	return payload
}

func pullMuxWith(t *testing.T, checks any, reviews http.HandlerFunc) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/pulls/5", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, pullPayload(5, "open", "", "clean"))
	})
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var sent sentGraphQL
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(checks)
		if err != nil {
			t.Fatal(err)
		}
		graphQLData(t, w, json.RawMessage(raw))
	})
	mux.HandleFunc("GET /repos/acme/widgets/pulls/5/reviews", reviews)
	return mux
}

func noReviews(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	}
}

func noChecks(t *testing.T) http.Handler {
	t.Helper()
	return graphQLHandler(t, nil, json.RawMessage(`{"repository":{"object":{"statusCheckRollup":null}}}`))
}

func pullReviewed(t *testing.T, mux http.Handler) (contracts.PullRequest, error) {
	t.Helper()
	f, _ := newTestForge(t, mux)
	return f.PullRequest(t.Context(), mustPullRef(t, testRepo(t)), "")
}

func TestPullRequestWithNoCheckIsChecksNone(t *testing.T) {
	mux := pullMuxWith(t, map[string]any{"repository": map[string]any{"object": map[string]any{"statusCheckRollup": nil}}}, noReviews(t))
	got, err := pullReviewed(t, mux)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksNone {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksNone)
	}
}

func TestPullRequestWithQueuedCheckIsChecksPending(t *testing.T) {
	data := rollupData(false, "", checkRunNode("build", "QUEUED", ""), checkRunNode("lint", "COMPLETED", "SUCCESS"))
	got, err := pullReviewed(t, pullMuxWith(t, data, noReviews(t)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksPending {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksPending)
	}
}

func TestPullRequestWithFailedCheckIsChecksFailed(t *testing.T) {
	data := rollupData(false, "", checkRunNode("build", "COMPLETED", "FAILURE"), checkRunNode("lint", "IN_PROGRESS", ""))
	got, err := pullReviewed(t, pullMuxWith(t, data, noReviews(t)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksFailed {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksFailed)
	}
}

func TestPullRequestWithActionRequiredCheckIsChecksActionRequired(t *testing.T) {
	data := rollupData(false, "", checkRunNode("deploy", "COMPLETED", "ACTION_REQUIRED"), checkRunNode("lint", "COMPLETED", "SUCCESS"))
	got, err := pullReviewed(t, pullMuxWith(t, data, noReviews(t)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksActionRequired {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksActionRequired)
	}
}

func TestPullRequestCountsFailedCommitStatus(t *testing.T) {
	data := rollupData(false, "", checkRunNode("build", "COMPLETED", "SUCCESS"), statusContextNode("ci/legacy", "FAILURE"))
	got, err := pullReviewed(t, pullMuxWith(t, data, noReviews(t)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksFailed {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksFailed)
	}
}

func TestPullRequestSummarizesChecksAcrossAllPages(t *testing.T) {
	var pages atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/pulls/5", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, pullPayload(5, "open", "", "clean"))
	})
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		var sent sentGraphQL
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Fatal(err)
		}
		pages.Add(1)
		var data map[string]any
		if sent.Variables["after"] == nil {
			data = rollupData(true, "c1", checkRunNode("build", "IN_PROGRESS", ""))
		} else {
			data = rollupData(false, "", checkRunNode("lint", "COMPLETED", "FAILURE"))
		}
		graphQLData(t, w, data)
	})
	mux.HandleFunc("GET /repos/acme/widgets/pulls/5/reviews", noReviews(t))
	got, err := pullReviewed(t, mux)
	if err != nil {
		t.Fatal(err)
	}
	if pages.Load() != 2 {
		t.Errorf("GraphQL pages read = %d, want 2", pages.Load())
	}
	if got.Checks != contracts.ChecksFailed {
		t.Errorf("checks = %v, want %v: a failure on the second page must count", got.Checks, contracts.ChecksFailed)
	}
}

func TestPullRequestPropagatesChecksReadError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/pulls/5", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, pullPayload(5, "open", "", "clean"))
	})
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /repos/acme/widgets/pulls/5/reviews", noReviews(t))
	got, err := pullReviewed(t, mux)
	if !errors.Is(err, errUnexpectedStatus) {
		t.Fatalf("err = %v, want errUnexpectedStatus", err)
	}
	if got.Checks != contracts.ChecksUnknown || got.Ref.Number() != 0 {
		t.Errorf("returned %+v with the error, want the zero pull request", got)
	}
}

func TestPullRequestLatestReviewIsNewestAcrossPages(t *testing.T) {
	reviews := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			writeJSON(t, w, http.StatusOK, []map[string]any{
				reviewPayload(3, "APPROVED", "2026-11-22T09:00:00Z"),
				reviewPayload(4, "COMMENTED", "2026-11-21T09:00:00Z"),
			})
			return
		}
		w.Header().Set("Link", `</repos/acme/widgets/pulls/5/reviews?page=2>; rel="next"`)
		writeJSON(t, w, http.StatusOK, []map[string]any{
			reviewPayload(12, "COMMENTED", "2026-11-20T09:00:00Z"),
		})
	}
	got, err := pullReviewed(t, pullMuxWith(t, rollupData(false, ""), reviews))
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestReview != "3" {
		t.Errorf("latest review = %q, want %q: the newest submission is on the second page", got.LatestReview, "3")
	}
}

func TestPullRequestIgnoresPendingReviews(t *testing.T) {
	reviews := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, []map[string]any{reviewPayload(9, "PENDING", "")})
	}
	got, err := pullReviewed(t, pullMuxWith(t, rollupData(false, ""), reviews))
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestReview != "" {
		t.Errorf("latest review = %q, want empty: a PENDING review is not submitted", got.LatestReview)
	}
}

func TestPullRequestWithoutReviewHasEmptyLatestReview(t *testing.T) {
	got, err := pullReviewed(t, pullMuxWith(t, rollupData(false, ""), noReviews(t)))
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestReview != "" {
		t.Errorf("latest review = %q, want empty", got.LatestReview)
	}
}

func TestPullRequestPropagatesReviewsReadError(t *testing.T) {
	reviews := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}
	_, err := pullReviewed(t, pullMuxWith(t, rollupData(false, ""), reviews))
	if !errors.Is(err, contracts.ErrForgeNotFound) {
		t.Fatalf("err = %v, want ErrForgeNotFound", err)
	}
}

func TestPullRequestReadsEachEndpointOnce(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/widgets/pulls/5", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(t, w, http.StatusOK, pullPayload(5, "open", "", "clean"))
	})
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		graphQLData(t, w, rollupData(false, ""))
	})
	mux.HandleFunc("GET /repos/acme/widgets/pulls/5/reviews", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(t, w, http.StatusOK, []map[string]any{})
	})
	if _, err := pullReviewed(t, mux); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("requests = %d, want 3 (pull request, checks, reviews)", got)
	}
}
