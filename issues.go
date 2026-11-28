package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	issueStateOpen   = "open"
	issueStateClosed = "closed"

	issueSortCreated   = "created"
	issueSortUpdated   = "updated"
	sortAscending      = "asc"
	sortDescending     = "desc"
	issueSinceWidening = time.Second
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
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
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
		Ref:           ref,
		Title:         i.Title,
		Body:          i.Body,
		URL:           i.HTMLURL,
		Author:        i.User.forgeActor(i.AuthorAssociation),
		Labels:        labels,
		State:         state,
		CreatedAt:     i.CreatedAt,
		UpdatedAt:     i.UpdatedAt,
		IsPullRequest: i.PullRequest != nil,
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

func issueSortName(sort contracts.IssueSort) string {
	switch sort {
	case contracts.IssueSortCreated:
		return issueSortCreated
	case contracts.IssueSortUpdated:
		return issueSortUpdated
	case contracts.IssueSortDefault:
	}
	return ""
}

func sortDirectionName(direction contracts.SortDirection) string {
	switch direction {
	case contracts.SortAscending:
		return sortAscending
	case contracts.SortDescending:
		return sortDescending
	case contracts.SortDirectionDefault:
	}
	return ""
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
			return fmt.Errorf("label %q: %w: %w", label, ErrInvalidLabel, contracts.ErrUnsupportedIssueFilter)
		}
		if strings.TrimSpace(label) == "" {
			return fmt.Errorf("label %q is empty: %w: %w", label, ErrInvalidLabel, contracts.ErrUnsupportedIssueFilter)
		}
	}
	return nil
}

func issuesOf(wires []issueWire, repo contracts.ForgeRepoRef, updatedSince time.Time) ([]contracts.Issue, error) {
	items := make([]contracts.Issue, 0, len(wires))
	secondBound := updatedSince.Truncate(time.Second)
	for index := range wires {
		issue, err := wires[index].toIssue(repo)
		if err != nil {
			return nil, err
		}
		if !updatedSince.IsZero() && issue.UpdatedAt.Before(secondBound) {
			continue
		}
		items = append(items, issue)
	}
	return items, nil
}

func (f *forge) issuesURL(repo contracts.ForgeRepoRef, query contracts.IssueQuery, limit, number int) (*url.URL, error) {
	state, err := issueStateName(query.Listed())
	if err != nil {
		return nil, err
	}
	if err := checkLabels(query.Labels); err != nil {
		return nil, err
	}
	values := url.Values{}
	values.Set("state", state)
	if len(query.Labels) > 0 {
		values.Set("labels", strings.Join(query.Labels, ","))
	}
	if !query.Mentioned.IsZero() {
		values.Set("mentioned", query.Mentioned.String())
	}
	if !query.UpdatedSince.IsZero() {
		values.Set("since", query.UpdatedSince.Add(-issueSinceWidening).UTC().Format(time.RFC3339))
	}
	if sort := issueSortName(query.Sort); sort != "" {
		values.Set("sort", sort)
	}
	if direction := sortDirectionName(query.Direction); direction != "" {
		values.Set("direction", direction)
	}
	values.Set("per_page", strconv.Itoa(limit))
	values.Set("page", strconv.Itoa(number))
	return f.repoURL(repo, values, "issues"), nil
}

func (f *forge) Issues(ctx context.Context, repo contracts.ForgeRepoRef, query contracts.IssueQuery, page contracts.PageRequest) (contracts.Page[contracts.Issue], error) {
	listing, err := f.IssuesSince(ctx, repo, query, page, "")
	if err != nil {
		return contracts.Page[contracts.Issue]{}, err
	}
	return listing.Page, nil
}

func (f *forge) IssuesSince(ctx context.Context, repo contracts.ForgeRepoRef, query contracts.IssueQuery, page contracts.PageRequest, since contracts.Revision) (contracts.IssueListing, error) {
	if err := f.checkRepo(repo); err != nil {
		return contracts.IssueListing{}, err
	}
	if err := query.Validate(); err != nil {
		return contracts.IssueListing{}, err
	}
	spec, err := f.issuesRequest(repo, query, page, since)
	if err != nil {
		return contracts.IssueListing{}, err
	}
	var listing contracts.IssueListing
	unchanged := false
	exchangeErr := f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		if resp.StatusCode == http.StatusNotModified {
			unchanged = true
			return nil
		}
		decoded, err := decodeIssueListing(resp, repo, query, page)
		listing = decoded
		return err
	})
	if exchangeErr != nil {
		return contracts.IssueListing{}, exchangeErr
	}
	if unchanged {
		return contracts.IssueListing{}, fmt.Errorf("issues of %s: %w", repo, contracts.ErrForgeNotModified)
	}
	return listing, nil
}

func (f *forge) issuesRequest(repo contracts.ForgeRepoRef, query contracts.IssueQuery, page contracts.PageRequest, since contracts.Revision) (request, error) {
	limit, err := pageLimit(page)
	if err != nil {
		return request{}, err
	}
	number, err := pageNumber(page.Cursor)
	if err != nil {
		return request{}, err
	}
	target, err := f.issuesURL(repo, query, limit, number)
	if err != nil {
		return request{}, err
	}
	spec := request{method: http.MethodGet, target: target}
	if etag, bound := contracts.IssueListingETag(since, repo, query, page); bound {
		spec.ifNoneMatch = etag
	}
	return spec, nil
}

func decodeIssueListing(resp *http.Response, repo contracts.ForgeRepoRef, query contracts.IssueQuery, page contracts.PageRequest) (contracts.IssueListing, error) {
	var wires []issueWire
	if err := decodeJSON(resp.Body, &wires); err != nil {
		return contracts.IssueListing{}, err
	}
	items, err := issuesOf(wires, repo, query.UpdatedSince)
	if err != nil {
		return contracts.IssueListing{}, err
	}
	next, err := nextPageCursor(resp.Header.Get(headerLink))
	if err != nil {
		return contracts.IssueListing{}, err
	}
	return contracts.IssueListing{
		Page:     contracts.Page[contracts.Issue]{Items: items, Next: next},
		Revision: contracts.IssueListingRevision(repo, query, page, resp.Header.Get(headerETag)),
	}, nil
}

type issueCreateWire struct {
	Title  string   `json:"title"`
	Body   string   `json:"body,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

type commentWire struct {
	Body string `json:"body"`
}

func (f *forge) OpenIssue(ctx context.Context, draft contracts.IssueDraft) (contracts.Issue, error) {
	if err := f.checkRepo(draft.Repo); err != nil {
		return contracts.Issue{}, err
	}
	if strings.TrimSpace(draft.Title) == "" {
		return contracts.Issue{}, contracts.ErrEmptyIssueTitle
	}
	payload, err := encodeJSON(issueCreateWire{Title: draft.Title, Body: draft.Body, Labels: draft.Labels})
	if err != nil {
		return contracts.Issue{}, err
	}
	spec := request{method: http.MethodPost, target: f.repoURL(draft.Repo, nil, "issues"), body: payload}
	var opened contracts.Issue
	exchangeErr := f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		var wire issueWire
		if err := decodeJSON(resp.Body, &wire); err != nil {
			return err
		}
		converted, err := wire.toIssue(draft.Repo)
		if err != nil {
			return err
		}
		opened = converted
		return nil
	})
	return opened, exchangeErr
}

func (f *forge) CommentOnIssue(ctx context.Context, ref contracts.IssueRef, body string) error {
	if err := f.checkRepo(ref.Repo()); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return contracts.ErrEmptyComment
	}
	if _, err := f.Issue(ctx, ref); err != nil {
		return err
	}
	payload, err := encodeJSON(commentWire{Body: body})
	if err != nil {
		return err
	}
	spec := request{method: http.MethodPost, target: f.repoURL(ref.Repo(), nil, "issues", strconv.Itoa(ref.Number()), "comments"), body: payload}
	return f.exchange(ctx, spec, func(contracts.ForgeToken, *http.Response) error { return nil })
}
