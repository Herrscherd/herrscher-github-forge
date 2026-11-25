package github

import (
	"context"
	"io"
	"net/url"
	"strconv"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const pendingReview = "PENDING"

type reviewWire struct {
	ID          int64     `json:"id"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
}

func (r reviewWire) submitted() bool {
	return r.State != pendingReview && !r.SubmittedAt.IsZero()
}

func (r reviewWire) newerThan(other reviewWire) bool {
	if r.SubmittedAt.Equal(other.SubmittedAt) {
		return r.ID > other.ID
	}
	return r.SubmittedAt.After(other.SubmittedAt)
}

func decodeReviews(body io.Reader) ([]reviewWire, error) {
	var reviews []reviewWire
	if err := decodeJSON(body, &reviews); err != nil {
		return nil, err
	}
	return reviews, nil
}

func (f *forge) reviewsListing(ref contracts.PullRequestRef) func(int) *url.URL {
	return func(number int) *url.URL {
		return f.pagedRepoURL(ref.Repo(), url.Values{}, number, "pulls", strconv.Itoa(ref.Number()), "reviews")
	}
}

func (f *forge) readReviews(ctx context.Context, ref contracts.PullRequestRef) (contracts.ReviewID, listingState, error) {
	reviews, state, err := readListing(ctx, f, f.reviewsListing(ref), decodeReviews)
	if degraded(err) {
		return "", listingState{}, nil
	}
	if err != nil {
		return "", listingState{}, err
	}
	return latestReview(reviews), state, nil
}

func latestReview(reviews []reviewWire) contracts.ReviewID {
	var latest reviewWire
	found := false
	for _, review := range reviews {
		if review.submitted() && (!found || review.newerThan(latest)) {
			latest, found = review, true
		}
	}
	if !found {
		return ""
	}
	return contracts.ReviewID(strconv.FormatInt(latest.ID, 10))
}
