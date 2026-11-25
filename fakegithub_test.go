package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type endpoint int

const (
	endpointPull endpoint = iota
	endpointCheckRuns
	endpointStatuses
	endpointReviews
	endpointMerge
)

type fakeCall struct {
	endpoint    endpoint
	sha         string
	page        int
	conditional bool
}

type fakeGitHub struct {
	t         *testing.T
	mu        sync.Mutex
	pageSize  int
	pull      map[string]any
	checkRuns []map[string]any
	statuses  []map[string]any
	reviews   []map[string]any
	fail      map[endpoint]int
	mergeCode int
	calls     []fakeCall
}

func newFakeGitHub(t *testing.T, pull map[string]any) *fakeGitHub {
	t.Helper()
	return &fakeGitHub{t: t, pageSize: 100, pull: pull, fail: make(map[endpoint]int), mergeCode: http.StatusOK}
}

func (g *fakeGitHub) edit(change func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	change()
}

func (g *fakeGitHub) resetCalls() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = nil
}

func (g *fakeGitHub) snapshot() []fakeCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]fakeCall(nil), g.calls...)
}

func (g *fakeGitHub) callsTo(kind endpoint) []fakeCall {
	var matching []fakeCall
	for _, call := range g.snapshot() {
		if call.endpoint == kind {
			matching = append(matching, call)
		}
	}
	return matching
}

func (g *fakeGitHub) unconditionalCalls() int {
	count := 0
	for _, call := range g.snapshot() {
		if !call.conditional {
			count++
		}
	}
	return count
}

func classifyEndpoint(path string) (endpoint, string, bool) {
	switch path {
	case "/repos/acme/widgets/pulls/5":
		return endpointPull, "", true
	case "/repos/acme/widgets/pulls/5/reviews":
		return endpointReviews, "", true
	case "/repos/acme/widgets/pulls/5/merge":
		return endpointMerge, "", true
	}
	parts := strings.Split(path, "/")
	if len(parts) == 7 && parts[1] == "repos" && parts[4] == "commits" {
		switch parts[6] {
		case "check-runs":
			return endpointCheckRuns, parts[5], true
		case "status":
			return endpointStatuses, parts[5], true
		}
	}
	return 0, "", false
}

func pageParam(r *http.Request) int {
	number, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || number < 1 {
		return 1
	}
	return number
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	kind, sha, ok := classifyEndpoint(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	conditional := r.Header.Get(headerIfNoneMatch) != ""
	g.calls = append(g.calls, fakeCall{endpoint: kind, sha: sha, page: pageParam(r), conditional: conditional})
	if code, failing := g.fail[kind]; failing {
		writeJSON(g.t, w, code, map[string]string{"message": "refused"})
		return
	}
	switch kind {
	case endpointPull:
		g.respond(w, r, g.pull, "")
	case endpointMerge:
		writeJSON(g.t, w, g.mergeCode, map[string]bool{"merged": g.mergeCode == http.StatusOK})
	case endpointCheckRuns:
		g.servePage(w, r, g.checkRuns, func(page []map[string]any) any {
			return map[string]any{"total_count": len(g.checkRuns), "check_runs": page}
		})
	case endpointStatuses:
		g.servePage(w, r, g.statuses, func(page []map[string]any) any {
			return map[string]any{"state": "pending", "statuses": page}
		})
	case endpointReviews:
		g.servePage(w, r, g.reviews, func(page []map[string]any) any {
			return page
		})
	}
}

func (g *fakeGitHub) servePage(w http.ResponseWriter, r *http.Request, items []map[string]any, wrap func([]map[string]any) any) {
	last := max(1, (len(items)+g.pageSize-1)/g.pageSize)
	number := pageParam(r)
	start := min((number-1)*g.pageSize, len(items))
	end := min(start+g.pageSize, len(items))
	page := append([]map[string]any{}, items[start:end]...)
	var links []string
	if number < last {
		links = append(links, fmt.Sprintf("<%s?page=%d>; rel=\"next\"", r.URL.Path, number+1))
	}
	if last > 1 {
		links = append(links, fmt.Sprintf("<%s?page=%d>; rel=\"last\"", r.URL.Path, last))
	}
	g.respond(w, r, wrap(page), strings.Join(links, ", "))
}

func (g *fakeGitHub) respond(w http.ResponseWriter, r *http.Request, body any, link string) {
	data, err := json.Marshal(body)
	if err != nil {
		g.t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set(headerETag, etag)
	if link != "" {
		w.Header().Set(headerLink, link)
	}
	if r.Header.Get(headerIfNoneMatch) == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		g.t.Errorf("write response: %v", err)
	}
}

func checkRunFixture(id int64, name, status, conclusion string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "status": status, "conclusion": conclusion,
		"details_url": "", "app": map[string]any{"slug": "other-app"},
	}
}

func statusFixture(context, state string) map[string]any {
	return map[string]any{"context": context, "state": state, "target_url": ""}
}
