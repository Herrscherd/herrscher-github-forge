package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	reviewPageSize = 100
	pendingReview  = "PENDING"
)

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

func (f *forge) reviewsPage(ctx context.Context, ref contracts.PullRequestRef, cursor contracts.PageCursor) ([]reviewWire, contracts.PageCursor, error) {
	number, err := pageNumber(cursor)
	if err != nil {
		return nil, "", err
	}
	values := url.Values{}
	values.Set("per_page", strconv.Itoa(reviewPageSize))
	values.Set("page", strconv.Itoa(number))
	spec := request{method: http.MethodGet, target: f.repoURL(ref.Repo(), values, "pulls", strconv.Itoa(ref.Number()), "reviews")}
	var wires []reviewWire
	var next contracts.PageCursor
	err = f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		if decodeErr := decodeJSON(resp.Body, &wires); decodeErr != nil {
			return decodeErr
		}
		var cursorErr error
		next, cursorErr = nextPageCursor(resp.Header.Get(headerLink))
		return cursorErr
	})
	return wires, next, err
}

func (f *forge) latestReview(ctx context.Context, ref contracts.PullRequestRef) (contracts.ReviewID, error) {
	var latest reviewWire
	found := false
	var cursor contracts.PageCursor
	for {
		wires, next, err := f.reviewsPage(ctx, ref, cursor)
		if err != nil {
			return "", err
		}
		for _, wire := range wires {
			if wire.submitted() && (!found || wire.newerThan(latest)) {
				latest, found = wire, true
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if !found {
		return "", nil
	}
	return contracts.ReviewID(strconv.FormatInt(latest.ID, 10)), nil
}
