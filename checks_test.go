package github

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const escapedCJK = `\u4e2d`

func actionsCheckRun(id int64, name, status, conclusion string) map[string]any {
	run := checkRunFixture(id, name, status, conclusion)
	run["app"] = map[string]any{"slug": "github-actions"}
	return run
}

func checkNames(items []contracts.Check) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func TestChecksMapsCheckRunsAndStatuses(t *testing.T) {
	fake := openFake(t)
	test := actionsCheckRun(99, "test", "completed", "success")
	test["details_url"] = "https://x/1"
	fake.checkRuns = []map[string]any{
		test,
		checkRunFixture(100, "lint", "in_progress", ""),
		checkRunFixture(101, "deploy", "waiting", ""),
		checkRunFixture(102, "stale", "completed", "stale"),
	}
	fake.statuses = []map[string]any{
		statusFixture("ci/legacy", "error"),
		statusFixture("ci/pending", "pending"),
		statusFixture("ci/ok", "success"),
	}
	f, _ := newTestForge(t, fake)
	page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Next != "" || len(page.Items) != 7 {
		t.Fatalf("got %d checks, next %q; want 7 and no next", len(page.Items), page.Next)
	}
	assertCheckRunsMapped(t, page.Items[:4])
	assertStatusesMapped(t, page.Items[4:])
}

func assertCheckRunsMapped(t *testing.T, items []contracts.Check) {
	t.Helper()
	got := items[0]
	if got.Status != contracts.CheckCompleted || got.Conclusion != contracts.ConclusionSuccess {
		t.Errorf("test check = %v %v", got.Status, got.Conclusion)
	}
	if got.Job == nil || got.Job.ID() != 99 || got.Job.Repo() != testRepo(t) {
		t.Errorf("Actions check job = %+v, want job 99", got.Job)
	}
	if got.Commit != mustCommit(t) || got.URL != "https://x/1" {
		t.Errorf("test check commit or URL = %s %q", got.Commit, got.URL)
	}
	lint := items[1]
	if lint.Status != contracts.CheckInProgress || lint.Job != nil {
		t.Errorf("running non-Actions check = %v job %v", lint.Status, lint.Job)
	}
	if _, concluded := lint.Concluded(); concluded {
		t.Error("a running check reports a conclusion")
	}
	if items[2].Status != contracts.CheckQueued {
		t.Errorf("waiting check status = %v, want queued", items[2].Status)
	}
	if got := items[3].Conclusion; got != contracts.ConclusionOther {
		t.Errorf("unknown completed conclusion = %v, want other", got)
	}
}

func assertStatusesMapped(t *testing.T, items []contracts.Check) {
	t.Helper()
	legacy := items[0]
	if legacy.Name != "ci/legacy" || legacy.Status != contracts.CheckCompleted || legacy.Conclusion != contracts.ConclusionFailure {
		t.Errorf("error status = %+v", legacy)
	}
	if items[1].Status != contracts.CheckInProgress {
		t.Errorf("pending status = %v, want in progress", items[1].Status)
	}
	if items[2].Conclusion != contracts.ConclusionSuccess {
		t.Errorf("success status conclusion = %v", items[2].Conclusion)
	}
}

func TestChecksPagesThroughBothListings(t *testing.T) {
	fake := openFake(t)
	fake.pageSize = 2
	fake.checkRuns = []map[string]any{
		checkRunFixture(1, "r1", "completed", "success"),
		checkRunFixture(2, "r2", "completed", "success"),
		checkRunFixture(3, "r3", "completed", "success"),
	}
	fake.statuses = []map[string]any{statusFixture("ci/a", "success"), statusFixture("ci/b", "success")}
	f, _ := newTestForge(t, fake)
	cursor := contracts.PageCursor("")
	var names []string
	for _, want := range [][]string{{"r1", "r2"}, {"r3", "ci/a"}, {"ci/b"}} {
		page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		got := checkNames(page.Items)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("page at cursor %q = %v, want %v", cursor, got, want)
		}
		names = append(names, got...)
		cursor = page.Next
	}
	if cursor != "" {
		t.Errorf("next after the last page = %q, want empty", cursor)
	}
	if len(names) != 5 {
		t.Errorf("read %d checks across pages, want 5", len(names))
	}
}

func TestChecksDefaultLimitIsThirty(t *testing.T) {
	fake := openFake(t)
	for id := range 35 {
		fake.checkRuns = append(fake.checkRuns, checkRunFixture(int64(id+1), "job", "completed", "success"))
	}
	f, _ := newTestForge(t, fake)
	first, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != defaultPageSize || first.Next != "30" {
		t.Errorf("first page = %d checks, next %q; want %d and next 30", len(first.Items), first.Next, defaultPageSize)
	}
	last, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{Cursor: first.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Items) != 5 || last.Next != "" {
		t.Errorf("last page = %d checks, next %q; want 5 and no next", len(last.Items), last.Next)
	}
}

func TestChecksRefusesBadCursorBeforeAnyRequest(t *testing.T) {
	fake := openFake(t)
	f, _ := newTestForge(t, fake)
	for _, cursor := range []contracts.PageCursor{"-1", "x", "1.5"} {
		if _, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{Cursor: cursor}); !errors.Is(err, errInvalidCursor) {
			t.Errorf("cursor %q: err = %v, want errInvalidCursor", cursor, err)
		}
	}
	if got := len(fake.snapshot()); got != 0 {
		t.Errorf("refused cursors sent %d requests, want none", got)
	}
}

func TestChecksRefusesBadLimitAndMissingCommit(t *testing.T) {
	fake := openFake(t)
	f, _ := newTestForge(t, fake)
	if _, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{Limit: 101}); !errors.Is(err, contracts.ErrInvalidPageLimit) {
		t.Errorf("limit 101: err = %v, want ErrInvalidPageLimit", err)
	}
	if _, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{Limit: -1}); !errors.Is(err, contracts.ErrInvalidPageLimit) {
		t.Errorf("limit -1: err = %v, want ErrInvalidPageLimit", err)
	}
	if _, err := f.Checks(t.Context(), testRepo(t), contracts.CommitSHA{}, contracts.PageRequest{}); !errors.Is(err, errMissingCommit) {
		t.Errorf("zero commit: err = %v, want errMissingCommit", err)
	}
	if got := len(fake.snapshot()); got != 0 {
		t.Errorf("refused calls sent %d requests, want none", got)
	}
}

func TestChecksForbiddenOnEitherListingIsReturned(t *testing.T) {
	for _, kind := range []endpoint{endpointCheckRuns, endpointStatuses} {
		fake := openFake(t)
		fake.fail[kind] = 403
		f, _ := newTestForge(t, fake)
		page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
		if !errors.Is(err, contracts.ErrForgeForbidden) {
			t.Errorf("endpoint %d refused: err = %v, want ErrForgeForbidden", kind, err)
		}
		if len(page.Items) != 0 {
			t.Errorf("endpoint %d refused: %d checks returned with the error", kind, len(page.Items))
		}
	}
}

func TestChecksRefusesListingOverTwentyPages(t *testing.T) {
	fill := map[string]func(*fakeGitHub){
		"check runs": func(fake *fakeGitHub) {
			for id := range maxListingPages + 1 {
				fake.checkRuns = append(fake.checkRuns, checkRunFixture(int64(id+1), "job", "completed", "success"))
			}
		},
		"statuses": func(fake *fakeGitHub) {
			for index := range maxListingPages + 1 {
				fake.statuses = append(fake.statuses, statusFixture(fmt.Sprintf("ci/%d", index), "success"))
			}
		},
	}
	for name, fillListing := range fill {
		fake := openFake(t)
		fake.pageSize = 1
		fillListing(fake)
		f, _ := newTestForge(t, fake)
		page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
		if !errors.Is(err, errTooManyPages) {
			t.Errorf("%s over %d pages: err = %v, want errTooManyPages", name, maxListingPages, err)
		}
		if len(page.Items) != 0 {
			t.Errorf("%s over %d pages: %d checks returned with the error, want none", name, maxListingPages, len(page.Items))
		}
	}
}

func TestChecksMissingCommitIsNotFound(t *testing.T) {
	fake := openFake(t)
	fake.fail[endpointCheckRuns] = 404
	f, _ := newTestForge(t, fake)
	if _, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{}); !errors.Is(err, contracts.ErrForgeNotFound) {
		t.Fatalf("err = %v, want ErrForgeNotFound", err)
	}
}

func TestCheckRunStatusReadsEveryWaitingState(t *testing.T) {
	for _, text := range []string{"QUEUED", "REQUESTED", "WAITING", "PENDING"} {
		got, err := checkRunStatus(text)
		if err != nil || got != contracts.CheckQueued {
			t.Errorf("checkRunStatus(%q) = %v, %v; want queued", text, got, err)
		}
	}
}

func TestChecksRefusesUnknownCheckRunStatus(t *testing.T) {
	fake := openFake(t)
	fake.checkRuns = []map[string]any{checkRunFixture(1, "x", "bogus", "")}
	f, _ := newTestForge(t, fake)
	if _, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{}); !errors.Is(err, errUnexpectedValue) {
		t.Fatalf("err = %v, want errUnexpectedValue for an unknown check run status", err)
	}
}

func TestChecksRefusesUnknownStatusState(t *testing.T) {
	fake := openFake(t)
	fake.statuses = []map[string]any{statusFixture("ci/new", "bogus")}
	f, _ := newTestForge(t, fake)
	if _, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{}); !errors.Is(err, errUnexpectedValue) {
		t.Fatalf("err = %v, want errUnexpectedValue for an unknown status state", err)
	}
}

func TestChecksQuotesUnknownStateBoundedASCII(t *testing.T) {
	fake := openFake(t)
	fake.statuses = []map[string]any{statusFixture("ci/new", strings.Repeat("中", 100)+"TAIL")}
	f, _ := newTestForge(t, fake)
	_, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
	if !errors.Is(err, errUnexpectedValue) {
		t.Fatalf("err = %v, want errUnexpectedValue", err)
	}
	message := err.Error()
	for index := range len(message) {
		if message[index] >= 0x80 {
			t.Fatalf("error %q holds a non-ASCII byte at %d", message, index)
		}
	}
	if !strings.Contains(message, escapedCJK) {
		t.Errorf("error %q does not quote the unknown state", message)
	}
	if strings.Contains(message, "TAIL") {
		t.Errorf("error %q holds the whole unknown state, want it bounded", message)
	}
}

func TestChecksAgreesWithPullRequest(t *testing.T) {
	fake := openFake(t)
	fake.checkRuns = []map[string]any{
		checkRunFixture(1, "a", "completed", "failure"),
		checkRunFixture(2, "b", "in_progress", ""),
	}
	fake.statuses = []map[string]any{statusFixture("ci/ok", "success")}
	f, _ := newTestForge(t, fake)
	page, err := f.Checks(t.Context(), testRepo(t), mustCommit(t), contracts.PageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	pull, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	if want := contracts.SummarizeChecks(page.Items); pull.Checks != want || want == contracts.ChecksUnknown {
		t.Errorf("pull request checks = %v, the listing summarizes to %v", pull.Checks, want)
	}
}
