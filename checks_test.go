package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

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

const checksRollupJSON = `{"repository":{"object":{"statusCheckRollup":{"contexts":{
  "pageInfo":{"hasNextPage":true,"endCursor":"c2"},
  "nodes":[
    {"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://x/1","databaseId":99,"checkSuite":{"app":{"slug":"github-actions"}}},
    {"__typename":"CheckRun","name":"lint","status":"IN_PROGRESS","conclusion":null,"detailsUrl":"https://x/2","databaseId":100,"checkSuite":{"app":{"slug":"other-app"}}},
    {"__typename":"CheckRun","name":"deploy","status":"WAITING","conclusion":null,"detailsUrl":"","databaseId":101,"checkSuite":{"app":{"slug":"other-app"}}},
    {"__typename":"CheckRun","name":"stale","status":"COMPLETED","conclusion":"STALE","detailsUrl":"","databaseId":102,"checkSuite":null},
    {"__typename":"StatusContext","context":"ci/legacy","state":"ERROR","targetUrl":"https://x/3"},
    {"__typename":"StatusContext","context":"ci/pending","state":"PENDING","targetUrl":""},
    {"__typename":"StatusContext","context":"ci/expected","state":"EXPECTED","targetUrl":""},
    {"__typename":"StatusContext","context":"ci/ok","state":"SUCCESS","targetUrl":""}
  ]}}}}}`

func TestChecksMapsCheckRunsAndStatuses(t *testing.T) {
	f, _ := newTestForge(t, graphQLHandler(t, nil, json.RawMessage(checksRollupJSON)))
	page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Next != "c2" {
		t.Errorf("next = %q, want c2", page.Next)
	}
	if len(page.Items) != 8 {
		t.Fatalf("got %d checks, want 8", len(page.Items))
	}
	test := page.Items[0]
	if test.Status != contracts.CheckCompleted || test.Conclusion != contracts.ConclusionSuccess {
		t.Errorf("test check = %v %v", test.Status, test.Conclusion)
	}
	if test.Job == nil || test.Job.ID() != 99 || test.Job.Repo() != testRepo(t) {
		t.Errorf("Actions check job = %+v, want job 99", test.Job)
	}
	if test.Commit != mustCommit(t) || test.URL != "https://x/1" {
		t.Errorf("test check commit or URL = %s %q", test.Commit, test.URL)
	}
	lint := page.Items[1]
	if lint.Status != contracts.CheckInProgress || lint.Job != nil {
		t.Errorf("running non-Actions check = %v job %v", lint.Status, lint.Job)
	}
	if _, concluded := lint.Concluded(); concluded {
		t.Error("a running check reports a conclusion")
	}
	if page.Items[2].Status != contracts.CheckQueued {
		t.Errorf("WAITING check status = %v, want queued", page.Items[2].Status)
	}
	if got := page.Items[3].Conclusion; got != contracts.ConclusionOther {
		t.Errorf("unknown completed conclusion = %v, want other", got)
	}
	if c, _ := page.Items[3].Concluded(); c == contracts.ConclusionNone {
		t.Error("a completed check mapped to no conclusion")
	}
}

func TestChecksMapsStatusContexts(t *testing.T) {
	f, _ := newTestForge(t, graphQLHandler(t, nil, json.RawMessage(checksRollupJSON)))
	page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	legacy := page.Items[4]
	if legacy.Name != "ci/legacy" || legacy.Conclusion != contracts.ConclusionFailure || legacy.Status != contracts.CheckCompleted {
		t.Errorf("ERROR status = %+v", legacy)
	}
	if page.Items[5].Status != contracts.CheckInProgress || page.Items[6].Status != contracts.CheckQueued {
		t.Errorf("pending or expected status = %v %v", page.Items[5].Status, page.Items[6].Status)
	}
	if page.Items[7].Conclusion != contracts.ConclusionSuccess {
		t.Errorf("SUCCESS status conclusion = %v", page.Items[7].Conclusion)
	}
}

func TestChecksSendsDefaultLimitAndCursor(t *testing.T) {
	var seen sentGraphQL
	f, _ := newTestForge(t, graphQLHandler(t, &seen, json.RawMessage(`{"repository":{"object":{"statusCheckRollup":null}}}`)))
	page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{Cursor: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Next != "" {
		t.Errorf("empty rollup gave %+v", page)
	}
	if seen.Variables["first"] != float64(defaultPageSize) {
		t.Errorf("first = %v, want %d", seen.Variables["first"], defaultPageSize)
	}
	if seen.Variables["after"] != "c1" {
		t.Errorf("after = %v, want c1", seen.Variables["after"])
	}
	if seen.Variables["oid"] != testCommit || seen.Variables["owner"] != "acme" || seen.Variables["name"] != "widgets" {
		t.Errorf("variables = %v", seen.Variables)
	}
	if !strings.Contains(seen.Query, "statusCheckRollup") {
		t.Error("the query does not read statusCheckRollup")
	}
}

func TestChecksRefusesBadLimitAndMissingCommit(t *testing.T) {
	var calls atomic.Int32
	f, _ := newTestForge(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	if _, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{Limit: 101}); !errors.Is(err, contracts.ErrInvalidPageLimit) {
		t.Errorf("limit 101: err = %v, want ErrInvalidPageLimit", err)
	}
	if _, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{Limit: -1}); !errors.Is(err, contracts.ErrInvalidPageLimit) {
		t.Errorf("limit -1: err = %v, want ErrInvalidPageLimit", err)
	}
	if _, err := f.Checks(t.Context(), testRepo(t), contracts.CommitSHA{}, contracts.PageRequest{}); !errors.Is(err, errMissingCommit) {
		t.Errorf("zero commit: err = %v, want errMissingCommit", err)
	}
	if calls.Load() != 0 {
		t.Error("a refused call reached the server")
	}
}

func TestChecksMissingObjectIsNotFound(t *testing.T) {
	f, _ := newTestForge(t, graphQLHandler(t, nil, json.RawMessage(`{"repository":{"object":null}}`)))
	if _, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{}); !errors.Is(err, contracts.ErrForgeNotFound) {
		t.Fatalf("err = %v, want ErrForgeNotFound", err)
	}
}

func TestChecksUnknownCheckRunStatusReadsAsQueued(t *testing.T) {
	data := `{"repository":{"object":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"__typename":"CheckRun","name":"x","status":"BOGUS","conclusion":null,"detailsUrl":"","databaseId":1,"checkSuite":null}]}}}}}`
	f, _ := newTestForge(t, graphQLHandler(t, nil, json.RawMessage(data)))
	page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
	if err != nil {
		t.Fatalf("err = %v, want the listing to survive an unknown status", err)
	}
	if len(page.Items) != 1 || page.Items[0].Status != contracts.CheckQueued {
		t.Fatalf("items = %+v, want one queued check", page.Items)
	}
	if _, concluded := page.Items[0].Concluded(); concluded {
		t.Error("an unknown status reports a conclusion")
	}
}

func TestChecksUnknownStatusContextStateReadsAsQueued(t *testing.T) {
	data := `{"repository":{"object":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"__typename":"StatusContext","context":"ci/new","state":"BOGUS","targetUrl":""}]}}}}}`
	f, _ := newTestForge(t, graphQLHandler(t, nil, json.RawMessage(data)))
	page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
	if err != nil {
		t.Fatalf("err = %v, want the listing to survive an unknown state", err)
	}
	if len(page.Items) != 1 || page.Items[0].Status != contracts.CheckQueued {
		t.Fatalf("items = %+v, want one queued check", page.Items)
	}
	if _, concluded := page.Items[0].Concluded(); concluded {
		t.Error("an unknown state reports a conclusion")
	}
}

func TestChecksRefusesUnknownCheckKind(t *testing.T) {
	data := `{"repository":{"object":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"__typename":"Mystery"}]}}}}}`
	f, _ := newTestForge(t, graphQLHandler(t, nil, json.RawMessage(data)))
	if _, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{}); !errors.Is(err, errUnexpectedValue) {
		t.Fatalf("err = %v, want errUnexpectedValue", err)
	}
}

func TestChecksLastPageHasNoNextCursor(t *testing.T) {
	data := `{"repository":{"object":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":false,"endCursor":"c9"},"nodes":[]}}}}}`
	f, _ := newTestForge(t, graphQLHandler(t, nil, json.RawMessage(data)))
	page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Next != "" {
		t.Errorf("next = %q on the last page, want empty", page.Next)
	}
}
