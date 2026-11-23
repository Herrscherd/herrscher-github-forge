package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const threadsJSON = `{"repository":{"pullRequest":{"reviewThreads":{
  "pageInfo":{"hasNextPage":true,"endCursor":"t2"},
  "nodes":[
    {"id":"T1","isResolved":false,"path":"main.go","line":12,
     "comments":{"nodes":[
       {"databaseId":1,"body":"fix this","createdAt":"2026-11-22T10:00:00Z","authorAssociation":"MEMBER",
        "author":{"__typename":"User","login":"reviewer","databaseId":42}},
       {"databaseId":2,"body":"done","createdAt":"2026-11-22T11:00:00Z","authorAssociation":"NONE",
        "author":{"__typename":"Bot","login":"ci-bot","databaseId":7}}
     ]}},
    {"id":"T2","isResolved":true,"path":"README.md","line":0,
     "comments":{"nodes":[
       {"databaseId":3,"body":"ghost","createdAt":"2026-11-22T12:00:00Z","authorAssociation":"FIRST_TIME_CONTRIBUTOR","author":null}
     ]}}
  ]}}}}`

func TestReviewThreadsMapsCommentsAndAuthors(t *testing.T) {
	var seen sentGraphQL
	f, _ := newTestForge(t, graphQLHandler(t, &seen, json.RawMessage(threadsJSON)))
	ref, err := contracts.NewPullRequestRef(testRepo(t), 5)
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.ReviewThreads(t.Context(), ref, contracts.PageRequest{Cursor: "t1"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Next != "t2" || len(page.Items) != 2 {
		t.Fatalf("page = next %q, %d items", page.Next, len(page.Items))
	}
	first := page.Items[0]
	if first.ID != "T1" || first.Path != "main.go" || first.Line != 12 || first.Resolved {
		t.Errorf("thread = %+v", first)
	}
	if len(first.Comments) != 2 {
		t.Fatalf("comments = %d, want 2", len(first.Comments))
	}
	member := first.Comments[0]
	if member.Author.Login != "reviewer" || member.Author.ID != 42 || member.Author.Bot || member.Author.Association != contracts.AssociationMember {
		t.Errorf("user comment author = %+v", member.Author)
	}
	if member.Body != "fix this" || member.CreatedAt.IsZero() {
		t.Errorf("user comment = %+v", member)
	}
	if bot := first.Comments[1].Author; !bot.Bot || bot.Login != "ci-bot" {
		t.Errorf("bot author = %+v, want Bot", bot)
	}
	if seen.Variables["after"] != "t1" || seen.Variables["number"] != float64(5) {
		assertGhostAuthor(t, page.Items[1])
		t.Errorf("variables = %v", seen.Variables)
	}
}

func TestReviewThreadsRefusesBadLimit(t *testing.T) {
	f, _ := newTestForge(t, http.NewServeMux())
	ref := mustPullRef(t, testRepo(t))
	if _, err := f.ReviewThreads(t.Context(), ref, contracts.PageRequest{Limit: 500}); !errors.Is(err, contracts.ErrInvalidPageLimit) {
		t.Fatalf("err = %v, want ErrInvalidPageLimit", err)
	}
}

func TestReviewThreadsMissingPullRequestIsNotFound(t *testing.T) {
	f, _ := newTestForge(t, graphQLHandler(t, nil, json.RawMessage(`{"repository":{"pullRequest":null}}`)))
	if _, err := f.ReviewThreads(t.Context(), mustPullRef(t, testRepo(t)), contracts.PageRequest{}); !errors.Is(err, contracts.ErrForgeNotFound) {
		t.Fatalf("err = %v, want ErrForgeNotFound", err)
	}
}

func TestReplyToThreadSendsMutation(t *testing.T) {
	var seen sentGraphQL
	f, _ := newTestForge(t, graphQLHandler(t, &seen, json.RawMessage(`{"addPullRequestReviewThreadReply":{"comment":{"id":"C1"}}}`)))
	thread, err := contracts.NewThreadRef(mustPullRef(t, testRepo(t)), "T1")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ReplyToThread(t.Context(), thread, "Thanks"); err != nil {
		t.Fatal(err)
	}
	input, ok := seen.Variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("input = %v", seen.Variables["input"])
	}
	if input["pullRequestReviewThreadId"] != "T1" || input["body"] != "Thanks" {
		t.Errorf("input = %v", input)
	}
	if !strings.Contains(seen.Query, "addPullRequestReviewThreadReply") {
		t.Error("the mutation name is missing from the query")
	}
}

func TestReplyToThreadMapsNotFoundError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"errors": []map[string]string{{"type": "NOT_FOUND", "message": "gone"}}})
	})
	f, _ := newTestForge(t, handler)
	thread, err := contracts.NewThreadRef(mustPullRef(t, testRepo(t)), "T1")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ReplyToThread(t.Context(), thread, "x"); !errors.Is(err, contracts.ErrForgeNotFound) {
		t.Fatalf("err = %v, want ErrForgeNotFound", err)
	}
}

func TestGraphQLErrorOtherTypeIsNotNotFound(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"errors": []map[string]string{{"type": "FORBIDDEN", "message": "no"}}})
	})
	f, _ := newTestForge(t, handler)
	thread, err := contracts.NewThreadRef(mustPullRef(t, testRepo(t)), "T1")
	if err != nil {
		t.Fatal(err)
	}
	err = f.ReplyToThread(t.Context(), thread, "x")
	if err == nil || errors.Is(err, contracts.ErrForgeNotFound) {
		t.Fatalf("err = %v, want a non-NOT_FOUND GraphQL error", err)
	}
}

func assertGhostAuthor(t *testing.T, resolved contracts.ReviewThread) {
	t.Helper()
	if !resolved.Resolved || resolved.Comments[0].Author.Login != "" || resolved.Comments[0].Author.Association != contracts.AssociationFirstTimer {
		t.Errorf("resolved thread with ghost author = %+v", resolved)
	}
}
