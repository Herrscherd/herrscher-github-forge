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

const threadsJSON = `{"repository":{"pullRequest":{"reviewThreads":{
  "pageInfo":{"hasNextPage":true,"endCursor":"t2"},
  "nodes":[
    {"id":"T1","isResolved":false,"path":"main.go","line":12,"originalLine":3,
     "comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
       {"body":"fix this","createdAt":"2026-11-22T10:00:00Z","authorAssociation":"MEMBER",
        "author":{"__typename":"User","login":"reviewer","databaseId":42}},
       {"body":"done","createdAt":"2026-11-22T11:00:00Z","authorAssociation":"NONE",
        "author":{"__typename":"Bot","login":"ci-bot","databaseId":7}}
     ]}},
    {"id":"T2","isResolved":true,"path":"README.md","line":null,"originalLine":7,
     "comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
       {"body":"ghost","createdAt":"2026-11-22T12:00:00Z","authorAssociation":"FIRST_TIME_CONTRIBUTOR","author":null}
     ]}}
  ]}}}}`

const threadWithTwoCommentPagesJSON = `{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
  {"id":"T1","isResolved":false,"path":"main.go","line":12,"originalLine":12,
   "comments":{"pageInfo":{"hasNextPage":true,"endCursor":"c2"},"nodes":[
     {"body":"first","createdAt":"2026-11-22T10:00:00Z","authorAssociation":"MEMBER","author":{"__typename":"User","login":"reviewer","databaseId":42}}
   ]}}
]}}}}`

const laterCommentPageJSON = `{"node":{"comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
  {"body":"second","createdAt":"2026-11-22T11:00:00Z","authorAssociation":"MEMBER","author":{"__typename":"User","login":"reviewer","databaseId":42}}
]}}}`

const endlessCommentPageJSON = `{"node":{"comments":{"pageInfo":{"hasNextPage":true,"endCursor":"more"},"nodes":[]}}}`

const threadDeletedMidwayJSON = `{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
  {"id":"T1","isResolved":false,"path":"main.go","line":12,"originalLine":12,
   "comments":{"pageInfo":{"hasNextPage":true,"endCursor":"c2"},"nodes":[
     {"body":"first","createdAt":"2026-11-22T10:00:00Z","authorAssociation":"MEMBER","author":{"__typename":"User","login":"reviewer","databaseId":42}}
   ]}},
  {"id":"T2","isResolved":true,"path":"README.md","line":7,"originalLine":7,
   "comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
     {"body":"kept","createdAt":"2026-11-22T12:00:00Z","authorAssociation":"NONE","author":null}
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
		t.Errorf("variables = %v", seen.Variables)
	}
	assertGhostAuthor(t, page.Items[1])
}

func TestReviewThreadLineFallsBackToOriginalLine(t *testing.T) {
	f, _ := newTestForge(t, graphQLHandler(t, nil, json.RawMessage(threadsJSON)))
	page, err := f.ReviewThreads(t.Context(), mustPullRef(t, testRepo(t)), contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Line != 12 {
		t.Errorf("line with both fields set = %d, want 12", page.Items[0].Line)
	}
	if page.Items[1].Line != 7 {
		t.Errorf("line from originalLine = %d, want 7", page.Items[1].Line)
	}
}

func TestReviewThreadsReadsEveryCommentPage(t *testing.T) {
	var sent []sentGraphQL
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request sentGraphQL
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		sent = append(sent, request)
		if _, followUp := request.Variables["threadId"]; followUp {
			graphQLData(t, w, json.RawMessage(laterCommentPageJSON))
			return
		}
		graphQLData(t, w, json.RawMessage(threadWithTwoCommentPagesJSON))
	})
	f, _ := newTestForge(t, handler)
	page, err := f.ReviewThreads(t.Context(), mustPullRef(t, testRepo(t)), contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	comments := page.Items[0].Comments
	if len(comments) != 2 || comments[0].Body != "first" || comments[1].Body != "second" {
		t.Fatalf("comments = %+v, want both pages in order", comments)
	}
	if len(sent) != 2 || sent[1].Variables["threadId"] != "T1" || sent[1].Variables["after"] != "c2" {
		t.Errorf("requests = %+v, want one follow-up for thread T1 after c2", sent)
	}
}

func TestReviewThreadsRefusesThreadPastCommentPageLimit(t *testing.T) {
	var followUps atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request sentGraphQL
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if _, followUp := request.Variables["threadId"]; !followUp {
			graphQLData(t, w, json.RawMessage(threadWithTwoCommentPagesJSON))
			return
		}
		followUps.Add(1)
		graphQLData(t, w, json.RawMessage(endlessCommentPageJSON))
	})
	f, _ := newTestForge(t, handler)
	_, err := f.ReviewThreads(t.Context(), mustPullRef(t, testRepo(t)), contracts.PageRequest{})
	if !errors.Is(err, errTooManyCommentPages) {
		t.Fatalf("err = %v, want errTooManyCommentPages", err)
	}
	if got := followUps.Load(); got != maxCommentPages-1 {
		t.Errorf("follow-up requests = %d, want %d", got, maxCommentPages-1)
	}
}

func TestReviewThreadsSkipsThreadDeletedBetweenPages(t *testing.T) {
	var followUps atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request sentGraphQL
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if _, followUp := request.Variables["threadId"]; followUp {
			followUps.Add(1)
			graphQLData(t, w, json.RawMessage(`{"node":null}`))
			return
		}
		graphQLData(t, w, json.RawMessage(threadDeletedMidwayJSON))
	})
	f, _ := newTestForge(t, handler)
	page, err := f.ReviewThreads(t.Context(), mustPullRef(t, testRepo(t)), contracts.PageRequest{})
	if err != nil {
		t.Fatalf("err = %v, want the listing to survive a thread deleted midway", err)
	}
	if got := followUps.Load(); got != 1 {
		t.Errorf("follow-up requests = %d, want 1 for the deleted thread", got)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "T2" {
		t.Fatalf("items = %+v, want only thread T2", page.Items)
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
