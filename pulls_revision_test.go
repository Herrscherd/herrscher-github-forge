package github

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const otherHeadSHA = "fedcba9876543210fedcba9876543210fedcba98"

func readPullSince(t *testing.T, fake *fakeGitHub, since contracts.Revision) (contracts.PullRequest, error) {
	t.Helper()
	f := newFakeForge(t, fake)
	return f.PullRequest(t.Context(), mustPullRef(t, testRepo(t)), since)
}

func readFirstRevision(t *testing.T, fake *fakeGitHub) contracts.Revision {
	t.Helper()
	got, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	return got.Revision
}

func TestPullRequestNotModifiedWhenNothingChanged(t *testing.T) {
	fake := openFake(t)
	revision := readFirstRevision(t, fake)
	fake.resetCalls()
	_, err := readPullSince(t, fake, revision)
	if !errors.Is(err, contracts.ErrForgeNotModified) {
		t.Fatalf("err = %v, want ErrForgeNotModified", err)
	}
	if got := fake.unconditionalCalls(); got != 0 {
		t.Errorf("unconditional requests = %d, want 0", got)
	}
	if got := len(fake.snapshot()); got != 4 {
		t.Errorf("requests = %d, want 4 conditional probes (pull request, check runs, statuses, reviews)", got)
	}
}

func TestPullRequestReportsCheckFailedAfterNotModifiedPull(t *testing.T) {
	fake := openFake(t)
	fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "in_progress", "")}
	revision := readFirstRevision(t, fake)
	fake.edit(func() {
		fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "failure")}
	})
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatalf("err = %v, want the pull request with the failed check", err)
	}
	if got.Checks != contracts.ChecksFailed {
		t.Errorf("checks = %v, want %v: the pull request was 304 but its check changed", got.Checks, contracts.ChecksFailed)
	}
}

func TestPullRequestSeesReviewOnNewLastPage(t *testing.T) {
	fake := openFake(t)
	for id := range 100 {
		fake.reviews = append(fake.reviews, reviewPayload(int64(id+1), "COMMENTED", "2026-11-20T09:00:00Z"))
	}
	revision := readFirstRevision(t, fake)
	fake.edit(func() {
		fake.reviews = append(fake.reviews, reviewPayload(101, "APPROVED", "2026-11-22T09:00:00Z"))
	})
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatalf("err = %v, want the pull request with the new review", err)
	}
	if got.LatestReview != "101" {
		t.Errorf("latest review = %q, want %q: the review was added on the new last page", got.LatestReview, "101")
	}
}

func TestPullRequestRereadsWhenLastReviewPageChanges(t *testing.T) {
	fake := openFake(t)
	fake.requestPageSize = 1
	fake.reviews = []map[string]any{
		reviewPayload(1, "COMMENTED", "2026-11-20T09:00:00Z"),
		reviewPayload(2, "APPROVED", "2026-11-22T09:00:00Z"),
	}
	revision := readFirstRevision(t, fake)
	fake.edit(func() {
		fake.reviews[1] = reviewPayload(2, "DISMISSED", "2026-11-22T09:00:00Z")
	})
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatalf("err = %v, want the pull request re-read: the last review page changed", err)
	}
	if got.LatestReview != "2" {
		t.Errorf("latest review = %q, want %q", got.LatestReview, "2")
	}
}

func TestPullRequestRereadsWhenHeadSHAChanges(t *testing.T) {
	fake := openFake(t)
	revision := readFirstRevision(t, fake)
	fake.edit(func() {
		fake.pull = pullPayload(5, "open", "", "clean")
		fake.pull["head"] = map[string]any{"ref": "feature", "sha": otherHeadSHA}
	})
	fake.resetCalls()
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatal(err)
	}
	if got.HeadSHA.String() != otherHeadSHA {
		t.Errorf("head SHA = %s, want %s", got.HeadSHA, otherHeadSHA)
	}
	checkRunCalls := fake.callsTo(endpointCheckRuns)
	if len(checkRunCalls) == 0 {
		t.Fatal("no check-run request after the head changed, want the checks of the new head to be read")
	}
	for _, call := range checkRunCalls {
		if call.sha != otherHeadSHA {
			t.Errorf("check runs read for %s, want %s", call.sha, otherHeadSHA)
		}
		if call.conditional {
			t.Errorf("check runs for the new head sent If-None-Match from the old revision")
		}
	}
}

func TestPullRequestTreatsUnreadableRevisionAsAbsent(t *testing.T) {
	fake := openFake(t)
	valid := readFirstRevision(t, fake)
	for _, since := range []contracts.Revision{"", "garbage", contracts.Revision(strings.Replace(string(valid), "v2", "v1", 1))} {
		fake.resetCalls()
		got, err := readPullSince(t, fake, since)
		if err != nil {
			t.Fatalf("revision %q: err = %v, want a full read", since, err)
		}
		if got.Ref.Number() != 5 {
			t.Errorf("revision %q: pull request number = %d, want 5", since, got.Ref.Number())
		}
		if got := fake.unconditionalCalls(); got != 4 {
			t.Errorf("revision %q: unconditional requests = %d, want 4 (a full read)", since, got)
		}
	}
}

func TestPullRequestRefusesRevisionOfAnotherVersion(t *testing.T) {
	fake := openFake(t)
	_, err := readPullSince(t, fake, "v3;pr=x;head=;checks=,1,false;status=,1,false;reviews=,1,false")
	if err != nil {
		t.Fatalf("err = %v, want a full read for a revision of another version", err)
	}
	for _, call := range fake.snapshot() {
		if call.conditional {
			t.Errorf("endpoint %d sent If-None-Match from a revision of another version", call.endpoint)
		}
	}
}

func TestPullRequestNotModifiedFallsBackToFullReadWhenProbeIsForbidden(t *testing.T) {
	fake := openFake(t)
	revision := readFirstRevision(t, fake)
	fake.fail[endpointStatuses] = http.StatusForbidden
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatalf("err = %v, want the pull request with unknown checks", err)
	}
	if got.Checks != contracts.ChecksUnknown {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksUnknown)
	}
}

func TestPullRequestRefusesRevisionWithUnknownFieldsAsAbsent(t *testing.T) {
	fake := openFake(t)
	valid := string(readFirstRevision(t, fake))
	tampered := strings.Replace(valid, "reviews=", "reviewz=", 1)
	if _, err := readPullSince(t, fake, contracts.Revision(tampered)); err != nil {
		t.Fatalf("err = %v, want a full read", err)
	}
	if !errors.Is(parseRevisionError(t, tampered), errUnreadableRevision) {
		t.Errorf("revision with a renamed field parsed, want errUnreadableRevision")
	}
}

func parseRevisionError(t *testing.T, text string) error {
	t.Helper()
	_, err := parseRevision(contracts.Revision(text))
	return err
}

func pagedOpenFake(t *testing.T) *fakeGitHub {
	t.Helper()
	fake := openFake(t)
	fake.requestPageSize = 2
	return fake
}

func TestPullRequestRereadsWhenMiddleCheckRunPageChanges(t *testing.T) {
	fake := pagedOpenFake(t)
	for id := range 5 {
		fake.checkRuns = append(fake.checkRuns, checkRunFixture(int64(id+1), "job", "completed", "success"))
	}
	revision := readFirstRevision(t, fake)
	fake.edit(func() {
		fake.checkRuns[2] = checkRunFixture(3, "job", "completed", "failure")
	})
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatalf("err = %v, want the pull request re-read: a middle check-run page changed", err)
	}
	if got.Checks != contracts.ChecksFailed {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksFailed)
	}
}

func TestPullRequestRereadsWhenMiddleStatusPageChanges(t *testing.T) {
	fake := pagedOpenFake(t)
	for index := range 5 {
		fake.statuses = append(fake.statuses, statusFixture(fmt.Sprintf("ci/%d", index), "success"))
	}
	revision := readFirstRevision(t, fake)
	fake.edit(func() {
		fake.statuses[2] = statusFixture("ci/2", "failure")
	})
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatalf("err = %v, want the pull request re-read: a middle status page changed", err)
	}
	if got.Checks != contracts.ChecksFailed {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksFailed)
	}
}

func TestPullRequestRereadsWhenMiddleReviewPageChanges(t *testing.T) {
	fake := pagedOpenFake(t)
	for index := range 5 {
		fake.reviews = append(fake.reviews, reviewPayload(int64(index+1), "COMMENTED", fmt.Sprintf("2026-11-%02dT09:00:00Z", 20+index)))
	}
	revision := readFirstRevision(t, fake)
	fake.edit(func() {
		fake.reviews[2] = reviewPayload(3, "APPROVED", "2026-11-26T09:00:00Z")
	})
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatalf("err = %v, want the pull request re-read: a middle review page changed", err)
	}
	if got.LatestReview != "3" {
		t.Errorf("latest review = %q, want %q", got.LatestReview, "3")
	}
}

func TestPullRequestNotModifiedWhenSinglePageListsUnchanged(t *testing.T) {
	fake := openFake(t)
	fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "success")}
	revision := readFirstRevision(t, fake)
	fake.resetCalls()
	if _, err := readPullSince(t, fake, revision); !errors.Is(err, contracts.ErrForgeNotModified) {
		t.Fatalf("err = %v, want ErrForgeNotModified for single-page listings", err)
	}
}

func TestPullRequestSeesReviewAddedToAFullFirstPage(t *testing.T) {
	fake := openFake(t)
	fake.omitLink = true
	for id := range 100 {
		fake.reviews = append(fake.reviews, reviewPayload(int64(id+1), "COMMENTED", "2026-11-20T09:00:00Z"))
	}
	revision := readFirstRevision(t, fake)
	fake.edit(func() {
		fake.reviews = append(fake.reviews, reviewPayload(101, "APPROVED", "2026-11-22T09:00:00Z"))
	})
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatalf("err = %v, want the pull request with the review on the second page", err)
	}
	if got.LatestReview != "101" {
		t.Errorf("latest review = %q, want %q: the first page was full, so its 304 proves nothing", got.LatestReview, "101")
	}
}

func TestPullRequestSeesCheckRunAddedToAFullFirstPageWithoutTotalCount(t *testing.T) {
	fake := openFake(t)
	fake.omitLink = true
	fake.omitTotal = true
	for id := range 100 {
		fake.checkRuns = append(fake.checkRuns, checkRunFixture(int64(id+1), "job", "completed", "success"))
	}
	revision := readFirstRevision(t, fake)
	fake.edit(func() {
		fake.checkRuns = append(fake.checkRuns, checkRunFixture(101, "job", "completed", "failure"))
	})
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatalf("err = %v, want the pull request with the failed check on the second page", err)
	}
	if got.Checks != contracts.ChecksFailed {
		t.Errorf("checks = %v, want %v: the first page was full, so its 304 proves nothing", got.Checks, contracts.ChecksFailed)
	}
}

func TestPullRequestCostWhenAProbeChainStopsAtAMultiPageListing(t *testing.T) {
	fake := pagedOpenFake(t)
	fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "success")}
	for index := range 5 {
		fake.statuses = append(fake.statuses, statusFixture(fmt.Sprintf("ci/%d", index), "success"))
	}
	fake.reviews = []map[string]any{reviewPayload(1, "APPROVED", "2026-11-20T09:00:00Z")}
	revision := readFirstRevision(t, fake)
	fake.resetCalls()
	if _, err := readPullSince(t, fake, revision); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.snapshot()); got != 8 {
		t.Errorf("requests = %d, want 8: 2 pull request, 1 probe and 1 page of check runs, 3 status pages, 1 review", got)
	}
	for kind, want := range map[endpoint]int{endpointPull: 2, endpointCheckRuns: 2, endpointStatuses: 3, endpointReviews: 1, endpointMerge: 0} {
		if got := len(fake.callsTo(kind)); got != want {
			t.Errorf("endpoint %d requests = %d, want %d", kind, got, want)
		}
	}
}

func TestPullRequestReadsAMultiPageListingInFullWhen304OmitsLink(t *testing.T) {
	fake := pagedOpenFake(t)
	fake.omitLink = true
	for id := range 3 {
		fake.checkRuns = append(fake.checkRuns, checkRunFixture(int64(id+1), "job", "completed", "success"))
	}
	revision := readFirstRevision(t, fake)
	fake.resetCalls()
	if _, err := readPullSince(t, fake, revision); err != nil {
		t.Fatalf("err = %v, want a full read: the revision records two pages", err)
	}
	if got := len(fake.callsTo(endpointCheckRuns)); got != 2 {
		t.Errorf("check-run requests = %d, want 2: the probe is skipped and both pages are read", got)
	}
}

func TestPullRequestCostWhenAListSpansPages(t *testing.T) {
	fake := pagedOpenFake(t)
	for id := range 5 {
		fake.checkRuns = append(fake.checkRuns, checkRunFixture(int64(id+1), "job", "completed", "success"))
	}
	fake.statuses = []map[string]any{statusFixture("ci/ok", "success")}
	fake.reviews = []map[string]any{reviewPayload(1, "APPROVED", "2026-11-20T09:00:00Z")}
	revision := readFirstRevision(t, fake)
	fake.resetCalls()
	if _, err := readPullSince(t, fake, revision); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.snapshot()); got != 7 {
		t.Errorf("requests = %d, want 7: one 304 and one full read of the pull request, 3 check-run pages, 1 status, 1 review", got)
	}
	for kind, want := range map[endpoint]int{endpointPull: 2, endpointCheckRuns: 3, endpointStatuses: 1, endpointReviews: 1, endpointMerge: 0} {
		if got := len(fake.callsTo(kind)); got != want {
			t.Errorf("endpoint %d requests = %d, want %d", kind, got, want)
		}
	}
}

func quotaRemaining(f *forge) int {
	f.quota.mu.Lock()
	defer f.quota.mu.Unlock()
	return f.quota.remaining
}

func TestPullRequestReadsFullyWhenRevisionHasNoHead(t *testing.T) {
	fake := openFake(t)
	fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "success")}
	revision := readFirstRevision(t, fake)
	headless := regexp.MustCompile("head=[0-9a-f]+").ReplaceAllString(string(revision), "head=")
	if headless == string(revision) {
		t.Fatal("the revision holds no head to remove")
	}
	handler, conditional := countConditional(fake)
	f, _ := newTestForge(t, handler)
	_, err := f.PullRequest(t.Context(), mustPullRef(t, testRepo(t)), contracts.Revision(headless))
	if errors.Is(err, contracts.ErrForgeNotModified) {
		t.Fatal("err = ErrForgeNotModified, want a full read: a revision without a head cannot prove the checks unchanged")
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := conditional.Load(); got != 1 {
		t.Errorf("conditional requests = %d, want 1: only the pull request carries its validator, no listing is probed", got)
	}
}

func TestPullRequestAtRestCostsFourNotModifiedProbesAndNoQuota(t *testing.T) {
	fake := openFake(t)
	fake.omitLink = true
	fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "success")}
	f := newFakeForge(t, fake)
	ref := mustPullRef(t, testRepo(t))
	first, err := f.PullRequest(t.Context(), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	before := quotaRemaining(f)
	fake.resetCalls()
	_, err = f.PullRequest(t.Context(), ref, first.Revision)
	if !errors.Is(err, contracts.ErrForgeNotModified) {
		t.Fatalf("err = %v, want ErrForgeNotModified for a single-page listing that answers 304 without Link", err)
	}
	calls := fake.snapshot()
	if len(calls) != 4 {
		t.Fatalf("requests = %d, want 4 probes", len(calls))
	}
	for _, call := range calls {
		if !call.notModified {
			t.Errorf("endpoint %d answered 200, want 304", call.endpoint)
		}
	}
	if after := quotaRemaining(f); after != before {
		t.Errorf("quota remaining = %d, want %d: a 304 costs no quota", after, before)
	}
}

func countConditional(fake *fakeGitHub) (http.Handler, *atomic.Int32) {
	var conditional atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(headerIfNoneMatch) != "" {
			conditional.Add(1)
		}
		fake.ServeHTTP(w, r)
	})
	return handler, &conditional
}

func TestPullRequestReadsInFullWhenFirstPageIsFullAtAnotherPageSize(t *testing.T) {
	fake := openFake(t)
	fake.requestPageSize = 2
	fake.checkRuns = []map[string]any{
		checkRunFixture(1, "build", "completed", "success"),
		checkRunFixture(2, "lint", "completed", "success"),
	}
	revision := readFirstRevision(t, fake)
	handler, conditional := countConditional(fake)
	f, _ := newTestForge(t, handler)
	_, err := f.PullRequest(t.Context(), mustPullRef(t, testRepo(t)), revision)
	if errors.Is(err, contracts.ErrForgeNotModified) {
		t.Fatal("err = ErrForgeNotModified: a first page of 2 items is full at page size 2, so the 304 proves nothing")
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := conditional.Load(); got != 1 {
		t.Errorf("conditional requests = %d, want 1: only the pull request carries its validator, the full first page is never probed", got)
	}
}
