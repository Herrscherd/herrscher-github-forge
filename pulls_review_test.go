package github

import (
	"errors"
	"net/http"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func reviewPayload(id int64, state, submittedAt string) map[string]any {
	payload := map[string]any{"id": id, "state": state, "user": map[string]any{"login": "reviewer"}}
	if submittedAt != "" {
		payload["submitted_at"] = submittedAt
	} else {
		payload["submitted_at"] = nil
	}
	return payload
}

func readPullWith(t *testing.T, fake *fakeGitHub) (contracts.PullRequest, error) {
	t.Helper()
	f, _ := newTestForge(t, fake)
	return f.PullRequest(t.Context(), mustPullRef(t, testRepo(t)), "")
}

func openFake(t *testing.T) *fakeGitHub {
	t.Helper()
	return newFakeGitHub(t, pullPayload(5, "open", "", "clean"))
}

func TestPullRequestWithNoCheckIsChecksNone(t *testing.T) {
	got, err := readPullWith(t, openFake(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksNone {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksNone)
	}
}

func TestPullRequestWithQueuedCheckIsChecksPending(t *testing.T) {
	fake := openFake(t)
	fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "queued", ""), checkRunFixture(2, "lint", "completed", "success")}
	got, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksPending {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksPending)
	}
}

func TestPullRequestWithFailedCheckIsChecksFailed(t *testing.T) {
	fake := openFake(t)
	fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "failure"), checkRunFixture(2, "lint", "in_progress", "")}
	got, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksFailed {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksFailed)
	}
}

func TestPullRequestWithActionRequiredCheckIsChecksActionRequired(t *testing.T) {
	fake := openFake(t)
	fake.checkRuns = []map[string]any{checkRunFixture(1, "deploy", "completed", "action_required"), checkRunFixture(2, "lint", "completed", "success")}
	got, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksActionRequired {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksActionRequired)
	}
}

func TestPullRequestCountsFailedCommitStatus(t *testing.T) {
	fake := openFake(t)
	fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "success")}
	fake.statuses = []map[string]any{statusFixture("ci/legacy", "failure")}
	got, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksFailed {
		t.Errorf("checks = %v, want %v", got.Checks, contracts.ChecksFailed)
	}
}

func TestPullRequestCountsPendingCommitStatus(t *testing.T) {
	fake := openFake(t)
	fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "success")}
	fake.statuses = []map[string]any{statusFixture("ci/legacy", "pending")}
	got, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks != contracts.ChecksPending {
		t.Errorf("checks = %v, want %v: a pending commit status is still running", got.Checks, contracts.ChecksPending)
	}
}

func TestPullRequestSummarizesChecksAcrossAllPages(t *testing.T) {
	fake := openFake(t)
	fake.pageSize = 1
	fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "in_progress", ""), checkRunFixture(2, "lint", "completed", "failure")}
	got, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	if pages := len(fake.callsTo(endpointCheckRuns)); pages != 2 {
		t.Errorf("check-run pages read = %d, want 2", pages)
	}
	if got.Checks != contracts.ChecksFailed {
		t.Errorf("checks = %v, want %v: a failure on the second page must count", got.Checks, contracts.ChecksFailed)
	}
}

func TestPullRequestPropagatesChecksReadError(t *testing.T) {
	fake := openFake(t)
	fake.fail[endpointCheckRuns] = http.StatusInternalServerError
	got, err := readPullWith(t, fake)
	if !errors.Is(err, errUnexpectedStatus) {
		t.Fatalf("err = %v, want errUnexpectedStatus", err)
	}
	if got.Checks != contracts.ChecksUnknown || got.Ref.Number() != 0 {
		t.Errorf("returned %+v with the error, want the zero pull request", got)
	}
}

func TestPullRequestDegradesForbiddenChecks(t *testing.T) {
	for _, kind := range []endpoint{endpointCheckRuns, endpointStatuses} {
		for _, code := range []int{http.StatusForbidden, http.StatusNotFound} {
			fake := openFake(t)
			fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "failure")}
			fake.fail[kind] = code
			got, err := readPullWith(t, fake)
			if err != nil {
				t.Fatalf("endpoint %d answered %d: err = %v, want the pull request with unknown checks", kind, code, err)
			}
			if got.Checks != contracts.ChecksUnknown {
				t.Errorf("endpoint %d answered %d: checks = %v, want %v", kind, code, got.Checks, contracts.ChecksUnknown)
			}
			if got.Ref.Number() != 5 {
				t.Errorf("endpoint %d answered %d: pull request number = %d, want 5", kind, code, got.Ref.Number())
			}
		}
	}
}

func TestPullRequestDegradesForbiddenReviews(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound} {
		fake := openFake(t)
		fake.checkRuns = []map[string]any{checkRunFixture(1, "build", "completed", "success")}
		fake.reviews = []map[string]any{reviewPayload(7, "APPROVED", "2026-11-20T09:00:00Z")}
		fake.fail[endpointReviews] = code
		got, err := readPullWith(t, fake)
		if err != nil {
			t.Fatalf("reviews answered %d: err = %v, want the pull request with no latest review", code, err)
		}
		if got.LatestReview != "" {
			t.Errorf("reviews answered %d: latest review = %q, want empty", code, got.LatestReview)
		}
		if got.Checks != contracts.ChecksPassed {
			t.Errorf("reviews answered %d: checks = %v, want %v", code, got.Checks, contracts.ChecksPassed)
		}
	}
}

func TestPullRequestLatestReviewIsNewestAcrossPages(t *testing.T) {
	fake := openFake(t)
	fake.pageSize = 1
	fake.reviews = []map[string]any{
		reviewPayload(12, "COMMENTED", "2026-11-20T09:00:00Z"),
		reviewPayload(4, "COMMENTED", "2026-11-21T09:00:00Z"),
		reviewPayload(3, "APPROVED", "2026-11-22T09:00:00Z"),
	}
	got, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestReview != "3" {
		t.Errorf("latest review = %q, want %q: the newest submission is on the last page", got.LatestReview, "3")
	}
}

func TestPullRequestIgnoresPendingReviewEvenWithSubmittedDate(t *testing.T) {
	fake := openFake(t)
	fake.reviews = []map[string]any{
		reviewPayload(2, "APPROVED", "2026-11-20T09:00:00Z"),
		reviewPayload(9, "PENDING", "2026-11-23T09:00:00Z"),
	}
	got, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestReview != "2" {
		t.Errorf("latest review = %q, want %q: a PENDING review is not submitted", got.LatestReview, "2")
	}
}

func TestPullRequestCountsDismissedReview(t *testing.T) {
	fake := openFake(t)
	fake.reviews = []map[string]any{
		reviewPayload(5, "APPROVED", "2026-11-20T09:00:00Z"),
		reviewPayload(6, "DISMISSED", "2026-11-21T09:00:00Z"),
	}
	got, err := readPullWith(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestReview != "6" {
		t.Errorf("latest review = %q, want %q: a dismissed review was still submitted", got.LatestReview, "6")
	}
}

func TestPullRequestBreaksSubmittedTiesByHighestID(t *testing.T) {
	tests := map[string][]map[string]any{
		"ascending":  {reviewPayload(3, "COMMENTED", "2026-11-20T09:00:00Z"), reviewPayload(4, "COMMENTED", "2026-11-20T09:00:00Z")},
		"descending": {reviewPayload(4, "COMMENTED", "2026-11-20T09:00:00Z"), reviewPayload(3, "COMMENTED", "2026-11-20T09:00:00Z")},
	}
	for name, reviews := range tests {
		fake := openFake(t)
		fake.reviews = reviews
		got, err := readPullWith(t, fake)
		if err != nil {
			t.Fatal(err)
		}
		if got.LatestReview != "4" {
			t.Errorf("%s: latest review = %q, want %q: equal dates go to the highest ID", name, got.LatestReview, "4")
		}
	}
}

func TestPullRequestWithoutReviewHasEmptyLatestReview(t *testing.T) {
	got, err := readPullWith(t, openFake(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestReview != "" {
		t.Errorf("latest review = %q, want empty", got.LatestReview)
	}
}

func TestPullRequestPropagatesReviewsReadError(t *testing.T) {
	fake := openFake(t)
	fake.fail[endpointReviews] = http.StatusInternalServerError
	_, err := readPullWith(t, fake)
	if !errors.Is(err, errUnexpectedStatus) {
		t.Fatalf("err = %v, want errUnexpectedStatus", err)
	}
}

func TestPullRequestRefusesMoreThanTwentyReviewPages(t *testing.T) {
	fake := openFake(t)
	fake.pageSize = 1
	for id := range maxListingPages + 1 {
		fake.reviews = append(fake.reviews, reviewPayload(int64(id+1), "COMMENTED", "2026-11-20T09:00:00Z"))
	}
	_, err := readPullWith(t, fake)
	if !errors.Is(err, errTooManyPages) {
		t.Fatalf("err = %v, want errTooManyPages", err)
	}
}

func TestPullRequestReadsEachEndpointOnce(t *testing.T) {
	fake := openFake(t)
	if _, err := readPullWith(t, fake); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.snapshot()); got != 4 {
		t.Errorf("requests = %d, want 4 (pull request, check runs, statuses, reviews)", got)
	}
	for _, kind := range []endpoint{endpointPull, endpointCheckRuns, endpointStatuses, endpointReviews} {
		if got := len(fake.callsTo(kind)); got != 1 {
			t.Errorf("endpoint %d read %d times, want 1", kind, got)
		}
	}
}
