package github

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const otherHeadSHA = "fedcba9876543210fedcba9876543210fedcba98"

func readPullSince(t *testing.T, fake *fakeGitHub, since contracts.Revision) (contracts.PullRequest, error) {
	t.Helper()
	f, _ := newTestForge(t, fake)
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
	fake.pageSize = 1
	fake.reviews = []map[string]any{reviewPayload(1, "COMMENTED", "2026-11-20T09:00:00Z")}
	revision := readFirstRevision(t, fake)
	fake.edit(func() {
		fake.reviews = append(fake.reviews, reviewPayload(2, "APPROVED", "2026-11-22T09:00:00Z"))
	})
	got, err := readPullSince(t, fake, revision)
	if err != nil {
		t.Fatalf("err = %v, want the pull request with the new review", err)
	}
	if got.LatestReview != "2" {
		t.Errorf("latest review = %q, want %q: the review was added on the last page", got.LatestReview, "2")
	}
}

func TestPullRequestRereadsWhenLastReviewPageChanges(t *testing.T) {
	fake := openFake(t)
	fake.pageSize = 1
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
	for _, call := range fake.callsTo(endpointCheckRuns) {
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
	for _, since := range []contracts.Revision{"", "garbage", contracts.Revision(strings.Replace(string(valid), "v1", "v2", 1))} {
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
	_, err := readPullSince(t, fake, "v2;pr=x;head=;checks=,0,;status=,0,;reviews=,0,")
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
	fake.pageSize = 2
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
