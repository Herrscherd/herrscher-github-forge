package github

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	actionsAppSlug = "github-actions"
	maxQuotedValue = 64
)

const checksQuery = `query($owner: String!, $name: String!, $oid: GitObjectID!, $first: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    object(oid: $oid) {
      ... on Commit {
        statusCheckRollup {
          contexts(first: $first, after: $after) {
            pageInfo { hasNextPage endCursor }
            nodes {
              __typename
              ... on CheckRun {
                name status conclusion detailsUrl databaseId
                checkSuite { app { slug } }
              }
              ... on StatusContext {
                context state targetUrl
              }
            }
          }
        }
      }
    }
  }
}`

type checksResponse struct {
	Repository *struct {
		Object *struct {
			StatusCheckRollup *struct {
				Contexts contextsWire `json:"contexts"`
			} `json:"statusCheckRollup"`
		} `json:"object"`
	} `json:"repository"`
}

type contextsWire struct {
	PageInfo pageInfoWire    `json:"pageInfo"`
	Nodes    []checkNodeWire `json:"nodes"`
}

type pageInfoWire struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

func (p pageInfoWire) cursor() contracts.PageCursor {
	if !p.HasNextPage {
		return ""
	}
	return contracts.PageCursor(p.EndCursor)
}

type checkNodeWire struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"detailsUrl"`
	DatabaseID int64  `json:"databaseId"`
	CheckSuite *struct {
		App *struct {
			Slug string `json:"slug"`
		} `json:"app"`
	} `json:"checkSuite"`
	Context   string `json:"context"`
	State     string `json:"state"`
	TargetURL string `json:"targetUrl"`
}

func (n checkNodeWire) isActionsJob() bool {
	return n.CheckSuite != nil && n.CheckSuite.App != nil && n.CheckSuite.App.Slug == actionsAppSlug
}

func (n checkNodeWire) toCheck(repo contracts.ForgeRepoRef, commit contracts.CommitSHA) (contracts.Check, error) {
	switch n.Typename {
	case "CheckRun":
		return checkRunCheck(repo, commit, checkRunFacts{
			name:        n.Name,
			status:      n.Status,
			conclusion:  n.Conclusion,
			url:         n.DetailsURL,
			jobID:       n.DatabaseID,
			fromActions: n.isActionsJob(),
		})
	case "StatusContext":
		return statusCheck(commit, n.Context, n.State, n.TargetURL)
	}
	return contracts.Check{}, fmt.Errorf("check kind %s: %w", quotedValue(n.Typename), errUnexpectedValue)
}

type checkRunFacts struct {
	name        string
	status      string
	conclusion  string
	url         string
	jobID       int64
	fromActions bool
}

func checkRunCheck(repo contracts.ForgeRepoRef, commit contracts.CommitSHA, facts checkRunFacts) (contracts.Check, error) {
	status, err := checkRunStatus(facts.status)
	if err != nil {
		return contracts.Check{}, fmt.Errorf("check run %q: %w", facts.name, err)
	}
	check := contracts.Check{Name: facts.name, Commit: commit, Status: status, URL: facts.url}
	if check.Status == contracts.CheckCompleted {
		check.Conclusion = checkRunConclusion(facts.conclusion)
	}
	if facts.fromActions {
		job, err := contracts.NewJobRef(repo, facts.jobID)
		if err != nil {
			return contracts.Check{}, fmt.Errorf("check run %q: %w", facts.name, err)
		}
		check.Job = &job
	}
	return check, nil
}

func statusCheck(commit contracts.CommitSHA, name, state, targetURL string) (contracts.Check, error) {
	check := contracts.Check{Name: name, Commit: commit, URL: targetURL}
	switch state {
	case "SUCCESS":
		check.Status, check.Conclusion = contracts.CheckCompleted, contracts.ConclusionSuccess
	case "FAILURE", "ERROR":
		check.Status, check.Conclusion = contracts.CheckCompleted, contracts.ConclusionFailure
	case "PENDING":
		check.Status = contracts.CheckInProgress
	case "EXPECTED":
		check.Status = contracts.CheckQueued
	default:
		return contracts.Check{}, fmt.Errorf("status context %q state %s: %w", name, quotedValue(state), errUnexpectedValue)
	}
	return check, nil
}

func checkRunStatus(text string) (contracts.CheckStatus, error) {
	switch text {
	case "QUEUED", "REQUESTED", "WAITING", "PENDING":
		return contracts.CheckQueued, nil
	case "IN_PROGRESS":
		return contracts.CheckInProgress, nil
	case "COMPLETED":
		return contracts.CheckCompleted, nil
	}
	return 0, fmt.Errorf("status %s: %w", quotedValue(text), errUnexpectedValue)
}

func quotedValue(text string) string {
	if len(text) > maxQuotedValue {
		return strconv.QuoteToASCII(text[:maxQuotedValue]) + "..."
	}
	return strconv.QuoteToASCII(text)
}

func checkRunConclusion(text string) contracts.CheckConclusion {
	switch text {
	case "SUCCESS":
		return contracts.ConclusionSuccess
	case "FAILURE":
		return contracts.ConclusionFailure
	case "NEUTRAL":
		return contracts.ConclusionNeutral
	case "CANCELLED":
		return contracts.ConclusionCancelled
	case "SKIPPED":
		return contracts.ConclusionSkipped
	case "TIMED_OUT":
		return contracts.ConclusionTimedOut
	case "ACTION_REQUIRED":
		return contracts.ConclusionActionRequired
	}
	return contracts.ConclusionOther
}

func (c contextsWire) toPage(repo contracts.ForgeRepoRef, commit contracts.CommitSHA) (contracts.Page[contracts.Check], error) {
	items := make([]contracts.Check, 0, len(c.Nodes))
	for index := range c.Nodes {
		check, err := c.Nodes[index].toCheck(repo, commit)
		if err != nil {
			return contracts.Page[contracts.Check]{}, err
		}
		items = append(items, check)
	}
	return contracts.Page[contracts.Check]{Items: items, Next: c.PageInfo.cursor()}, nil
}

func (f *forge) Checks(ctx context.Context, repo contracts.ForgeRepoRef, commit contracts.CommitSHA, page contracts.PageRequest) (contracts.Page[contracts.Check], error) {
	if err := f.checkRepo(repo); err != nil {
		return contracts.Page[contracts.Check]{}, err
	}
	if commit == (contracts.CommitSHA{}) {
		return contracts.Page[contracts.Check]{}, errMissingCommit
	}
	limit, err := pageLimit(page)
	if err != nil {
		return contracts.Page[contracts.Check]{}, err
	}
	variables := map[string]any{
		"owner":       repo.Namespace(),
		"name":        repo.Name(),
		"oid":         commit.String(),
		"first":       limit,
		afterVariable: cursorVariable(page.Cursor),
	}
	var response checksResponse
	if err := f.graphQL(ctx, checksQuery, variables, &response); err != nil {
		return contracts.Page[contracts.Check]{}, err
	}
	if response.Repository == nil || response.Repository.Object == nil {
		return contracts.Page[contracts.Check]{}, fmt.Errorf("commit %s of %s: %w", commit, repo, contracts.ErrForgeNotFound)
	}
	rollup := response.Repository.Object.StatusCheckRollup
	if rollup == nil {
		return contracts.Page[contracts.Check]{}, nil
	}
	return rollup.Contexts.toPage(repo, commit)
}

type checkRunsWire struct {
	CheckRuns []checkRunWire `json:"check_runs"`
}

type checkRunWire struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Conclusion string   `json:"conclusion"`
	DetailsURL string   `json:"details_url"`
	App        *appWire `json:"app"`
}

type appWire struct {
	Slug string `json:"slug"`
}

type combinedStatusWire struct {
	Statuses []statusWire `json:"statuses"`
}

type statusWire struct {
	Context   string `json:"context"`
	State     string `json:"state"`
	TargetURL string `json:"target_url"`
}

func (r checkRunWire) toCheck(repo contracts.ForgeRepoRef, commit contracts.CommitSHA) (contracts.Check, error) {
	return checkRunCheck(repo, commit, checkRunFacts{
		name:        r.Name,
		status:      strings.ToUpper(r.Status),
		conclusion:  strings.ToUpper(r.Conclusion),
		url:         r.DetailsURL,
		jobID:       r.ID,
		fromActions: r.App != nil && r.App.Slug == actionsAppSlug,
	})
}

func checkRunChecks(repo contracts.ForgeRepoRef, commit contracts.CommitSHA) func(io.Reader) ([]contracts.Check, error) {
	return func(body io.Reader) ([]contracts.Check, error) {
		var wire checkRunsWire
		if err := decodeJSON(body, &wire); err != nil {
			return nil, err
		}
		checks := make([]contracts.Check, 0, len(wire.CheckRuns))
		for _, run := range wire.CheckRuns {
			check, err := run.toCheck(repo, commit)
			if err != nil {
				return nil, err
			}
			checks = append(checks, check)
		}
		return checks, nil
	}
}

func statusChecks(commit contracts.CommitSHA) func(io.Reader) ([]contracts.Check, error) {
	return func(body io.Reader) ([]contracts.Check, error) {
		var wire combinedStatusWire
		if err := decodeJSON(body, &wire); err != nil {
			return nil, err
		}
		checks := make([]contracts.Check, 0, len(wire.Statuses))
		for _, status := range wire.Statuses {
			check, err := statusCheck(commit, status.Context, strings.ToUpper(status.State), status.TargetURL)
			if err != nil {
				return nil, err
			}
			checks = append(checks, check)
		}
		return checks, nil
	}
}

func (f *forge) checkRunsListing(repo contracts.ForgeRepoRef, commit contracts.CommitSHA) func(int) *url.URL {
	return func(number int) *url.URL {
		return f.pagedRepoURL(repo, url.Values{"filter": {"latest"}}, number, "commits", commit.String(), "check-runs")
	}
}

func (f *forge) statusListing(repo contracts.ForgeRepoRef, commit contracts.CommitSHA) func(int) *url.URL {
	return func(number int) *url.URL {
		return f.pagedRepoURL(repo, url.Values{}, number, "commits", commit.String(), "status")
	}
}

type headChecks struct {
	state    contracts.ChecksState
	runs     listingState
	statuses listingState
}

func (f *forge) readHeadChecks(ctx context.Context, repo contracts.ForgeRepoRef, commit contracts.CommitSHA) (headChecks, error) {
	runs, runState, runErr := readListing(ctx, f, f.checkRunsListing(repo, commit), checkRunChecks(repo, commit))
	statuses, statusState, statusErr := readListing(ctx, f, f.statusListing(repo, commit), statusChecks(commit))
	if err := firstUnreadable(runErr, statusErr); err != nil {
		return headChecks{}, err
	}
	if degraded(runErr) || degraded(statusErr) {
		return headChecks{state: contracts.ChecksUnknown, runs: runState, statuses: statusState}, nil
	}
	return headChecks{
		state:    contracts.SummarizeChecks(append(runs, statuses...)),
		runs:     runState,
		statuses: statusState,
	}, nil
}

func firstUnreadable(errs ...error) error {
	for _, err := range errs {
		if err != nil && !degraded(err) {
			return err
		}
	}
	return nil
}
