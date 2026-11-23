package github

import (
	"context"
	"fmt"
	"strconv"

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
		return n.checkRun(repo, commit)
	case "StatusContext":
		return n.statusContext(commit)
	}
	return contracts.Check{}, fmt.Errorf("check kind %s: %w", quotedValue(n.Typename), errUnexpectedValue)
}

func (n checkNodeWire) checkRun(repo contracts.ForgeRepoRef, commit contracts.CommitSHA) (contracts.Check, error) {
	status, err := checkRunStatus(n.Status)
	if err != nil {
		return contracts.Check{}, fmt.Errorf("check run %q: %w", n.Name, err)
	}
	check := contracts.Check{Name: n.Name, Commit: commit, Status: status, URL: n.DetailsURL}
	if check.Status == contracts.CheckCompleted {
		check.Conclusion = checkRunConclusion(n.Conclusion)
	}
	if n.isActionsJob() {
		job, err := contracts.NewJobRef(repo, n.DatabaseID)
		if err != nil {
			return contracts.Check{}, fmt.Errorf("check run %q: %w", n.Name, err)
		}
		check.Job = &job
	}
	return check, nil
}

func (n checkNodeWire) statusContext(commit contracts.CommitSHA) (contracts.Check, error) {
	check := contracts.Check{Name: n.Context, Commit: commit, URL: n.TargetURL}
	switch n.State {
	case "SUCCESS":
		check.Status, check.Conclusion = contracts.CheckCompleted, contracts.ConclusionSuccess
	case "FAILURE", "ERROR":
		check.Status, check.Conclusion = contracts.CheckCompleted, contracts.ConclusionFailure
	case "PENDING":
		check.Status = contracts.CheckInProgress
	case "EXPECTED":
		check.Status = contracts.CheckQueued
	default:
		return contracts.Check{}, fmt.Errorf("status context %q state %s: %w", n.Context, quotedValue(n.State), errUnexpectedValue)
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
