package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	issueStateOpen   = "open"
	issueStateClosed = "closed"
)

type labelWire struct {
	Name string `json:"name"`
}

type issueWire struct {
	Number            int         `json:"number"`
	Title             string      `json:"title"`
	Body              string      `json:"body"`
	HTMLURL           string      `json:"html_url"`
	State             string      `json:"state"`
	AuthorAssociation string      `json:"author_association"`
	User              actorWire   `json:"user"`
	Labels            []labelWire `json:"labels"`
	PullRequest       *struct{}   `json:"pull_request"`
}

func (i issueWire) toIssue(repo contracts.ForgeRepoRef) (contracts.Issue, error) {
	ref, err := contracts.NewIssueRef(repo, i.Number)
	if err != nil {
		return contracts.Issue{}, fmt.Errorf("issue %d: %w", i.Number, err)
	}
	state, err := issueStateOf(i.State)
	if err != nil {
		return contracts.Issue{}, err
	}
	labels := make([]string, 0, len(i.Labels))
	for _, label := range i.Labels {
		labels = append(labels, label.Name)
	}
	return contracts.Issue{
		Ref:    ref,
		Title:  i.Title,
		Body:   i.Body,
		URL:    i.HTMLURL,
		Author: i.User.forgeActor(i.AuthorAssociation),
		Labels: labels,
		State:  state,
	}, nil
}

func issueStateOf(text string) (contracts.IssueState, error) {
	switch text {
	case issueStateOpen:
		return contracts.IssueOpen, nil
	case issueStateClosed:
		return contracts.IssueClosed, nil
	}
	return contracts.IssueStateUnknown, fmt.Errorf("%q: %w", text, errUnknownIssueState)
}

func issueStateName(state contracts.IssueState) (string, error) {
	switch state {
	case contracts.IssueOpen:
		return issueStateOpen, nil
	case contracts.IssueClosed:
		return issueStateClosed, nil
	case contracts.IssueStateUnknown:
	}
	return "", fmt.Errorf("%s: %w", state, errUnknownIssueState)
}

func (f *forge) Issue(ctx context.Context, ref contracts.IssueRef) (contracts.Issue, error) {
	if err := f.checkRepo(ref.Repo()); err != nil {
		return contracts.Issue{}, err
	}
	var wire issueWire
	if err := f.getJSON(ctx, f.repoURL(ref.Repo(), nil, "issues", strconv.Itoa(ref.Number())), &wire); err != nil {
		return contracts.Issue{}, err
	}
	if wire.PullRequest != nil {
		return contracts.Issue{}, fmt.Errorf("issue %d of %s: %w: %w", ref.Number(), ref.Repo(), ErrNamesPullRequest, contracts.ErrForgeNotFound)
	}
	return wire.toIssue(ref.Repo())
}

func checkLabels(labels []string) error {
	for _, label := range labels {
		if strings.Contains(label, ",") {
			return fmt.Errorf("label %q: %w", label, ErrInvalidLabel)
		}
	}
	return nil
}

func issuesOf(wires []issueWire, repo contracts.ForgeRepoRef) ([]contracts.Issue, error) {
	items := make([]contracts.Issue, 0, len(wires))
	for index := range wires {
		wire := &wires[index]
		if wire.PullRequest != nil {
			continue
		}
		issue, err := wire.toIssue(repo)
		if err != nil {
			return nil, err
		}
		items = append(items, issue)
	}
	return items, nil
}

func (f *forge) Issues(ctx context.Context, repo contracts.ForgeRepoRef, query contracts.IssueQuery, page contracts.PageRequest) (contracts.Page[contracts.Issue], error) {
	if err := f.checkRepo(repo); err != nil {
		return contracts.Page[contracts.Issue]{}, err
	}
	limit, err := pageLimit(page)
	if err != nil {
		return contracts.Page[contracts.Issue]{}, err
	}
	number, err := pageNumber(page.Cursor)
	if err != nil {
		return contracts.Page[contracts.Issue]{}, err
	}
	state, err := issueStateName(query.Listed())
	if err != nil {
		return contracts.Page[contracts.Issue]{}, err
	}
	if err := checkLabels(query.Labels); err != nil {
		return contracts.Page[contracts.Issue]{}, err
	}
	values := url.Values{}
	values.Set("state", state)
	values.Set("labels", strings.Join(query.Labels, ","))
	values.Set("per_page", strconv.Itoa(limit))
	values.Set("page", strconv.Itoa(number))
	spec := request{method: http.MethodGet, target: f.repoURL(repo, values, "issues")}
	var items []contracts.Issue
	var next contracts.PageCursor
	exchangeErr := f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		var wires []issueWire
		if err := decodeJSON(resp.Body, &wires); err != nil {
			return err
		}
		converted, err := issuesOf(wires, repo)
		if err != nil {
			return err
		}
		items = converted
		var cursorErr error
		next, cursorErr = nextPageCursor(resp.Header.Get(headerLink))
		return cursorErr
	})
	if exchangeErr != nil {
		return contracts.Page[contracts.Issue]{}, exchangeErr
	}
	return contracts.Page[contracts.Issue]{Items: items, Next: next}, nil
}
