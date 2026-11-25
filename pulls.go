package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type branchWire struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type pullWire struct {
	Number            int        `json:"number"`
	Title             string     `json:"title"`
	HTMLURL           string     `json:"html_url"`
	State             string     `json:"state"`
	MergedAt          string     `json:"merged_at"`
	Draft             bool       `json:"draft"`
	MergeableState    string     `json:"mergeable_state"`
	AuthorAssociation string     `json:"author_association"`
	User              actorWire  `json:"user"`
	Head              branchWire `json:"head"`
	Base              branchWire `json:"base"`
}

type pullCreateWire struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
	Draft bool   `json:"draft"`
}

type mergeWire struct {
	MergeMethod string `json:"merge_method"`
}

func (p pullWire) pullState() contracts.PullRequestState {
	switch {
	case p.MergedAt != "":
		return contracts.PullRequestMerged
	case p.State == "open":
		return contracts.PullRequestOpen
	}
	return contracts.PullRequestClosed
}

func mergeStateOf(mergeable string) contracts.MergeState {
	switch mergeable {
	case "clean", "unstable", "has_hooks":
		return contracts.MergeStateClean
	case "dirty":
		return contracts.MergeStateConflicting
	case "blocked":
		return contracts.MergeStateBlocked
	case "behind":
		return contracts.MergeStateBehind
	case "draft":
		return contracts.MergeStateDraft
	}
	return contracts.MergeStateUnknown
}

func (p pullWire) toPullRequest(ref contracts.PullRequestRef) (contracts.PullRequest, error) {
	head, err := contracts.ParseCommitSHA(p.Head.SHA)
	if err != nil {
		return contracts.PullRequest{}, fmt.Errorf("pull request head: %w", err)
	}
	return contracts.PullRequest{
		Ref:     ref,
		Title:   p.Title,
		URL:     p.HTMLURL,
		Head:    p.Head.Ref,
		Base:    p.Base.Ref,
		HeadSHA: head,
		State:   p.pullState(),
		Draft:   p.Draft,
		Merge:   mergeStateOf(p.MergeableState),
		Author:  p.User.forgeActor(p.AuthorAssociation),
	}, nil
}

func (f *forge) pullURL(ref contracts.PullRequestRef, elements ...string) *url.URL {
	path := slices.Concat([]string{"pulls", strconv.Itoa(ref.Number())}, elements)
	return f.repoURL(ref.Repo(), nil, path...)
}

type pullRead struct {
	wire        pullWire
	etag        string
	notModified bool
}

func (f *forge) fetchPull(ctx context.Context, ref contracts.PullRequestRef, ifNoneMatch string) (pullRead, error) {
	spec := request{method: http.MethodGet, target: f.pullURL(ref), ifNoneMatch: ifNoneMatch}
	var read pullRead
	err := f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		read.etag = resp.Header.Get(headerETag)
		if resp.StatusCode == http.StatusNotModified {
			read.notModified = true
			return nil
		}
		return decodeJSON(resp.Body, &read.wire)
	})
	return read, err
}

func (f *forge) PullRequest(ctx context.Context, ref contracts.PullRequestRef, since contracts.Revision) (contracts.PullRequest, error) {
	if err := f.checkRepo(ref.Repo()); err != nil {
		return contracts.PullRequest{}, err
	}
	prev, err := parseRevision(since)
	if err != nil {
		return f.fullPull(ctx, ref)
	}
	read, err := f.fetchPull(ctx, ref, prev.pull)
	if err != nil {
		return contracts.PullRequest{}, err
	}
	if !read.notModified {
		return f.assemble(ctx, ref, read)
	}
	unchanged, err := f.unchangedSince(ctx, ref, prev)
	if err != nil {
		return contracts.PullRequest{}, err
	}
	if unchanged {
		return contracts.PullRequest{}, contracts.ErrForgeNotModified
	}
	return f.fullPull(ctx, ref)
}

func (f *forge) fullPull(ctx context.Context, ref contracts.PullRequestRef) (contracts.PullRequest, error) {
	read, err := f.fetchPull(ctx, ref, "")
	if err != nil {
		return contracts.PullRequest{}, err
	}
	return f.assemble(ctx, ref, read)
}

func (f *forge) unchangedSince(ctx context.Context, ref contracts.PullRequestRef, prev revisionState) (bool, error) {
	if prev.head == (contracts.CommitSHA{}) {
		return false, nil
	}
	repo := ref.Repo()
	probes := []struct {
		listing func(int) *url.URL
		state   listingState
	}{
		{f.checkRunsListing(repo, prev.head), prev.checks},
		{f.statusListing(repo, prev.head), prev.status},
		{f.reviewsListing(ref), prev.reviews},
	}
	for _, probe := range probes {
		unchanged, err := f.listingUnchanged(ctx, probe.listing, probe.state)
		if degraded(err) {
			return false, nil
		}
		if err != nil || !unchanged {
			return false, err
		}
	}
	return true, nil
}

func (f *forge) assemble(ctx context.Context, ref contracts.PullRequestRef, read pullRead) (contracts.PullRequest, error) {
	pull, err := read.wire.toPullRequest(ref)
	if err != nil {
		return contracts.PullRequest{}, err
	}
	checks, err := f.readHeadChecks(ctx, ref.Repo(), pull.HeadSHA)
	if err != nil {
		return contracts.PullRequest{}, err
	}
	latest, reviews, err := f.readReviews(ctx, ref)
	if err != nil {
		return contracts.PullRequest{}, err
	}
	pull.Checks = checks.state
	pull.LatestReview = latest
	pull.Revision = revisionState{
		pull:    read.etag,
		head:    pull.HeadSHA,
		checks:  checks.runs,
		status:  checks.statuses,
		reviews: reviews,
	}.revision()
	return pull, nil
}

func (f *forge) OpenPullRequest(ctx context.Context, draft contracts.PullRequestDraft) (contracts.PullRequest, error) {
	if err := f.checkRepo(draft.Repo); err != nil {
		return contracts.PullRequest{}, err
	}
	payload, err := encodeJSON(pullCreateWire{Title: draft.Title, Head: draft.Head, Base: draft.Base, Body: draft.Body, Draft: draft.Draft})
	if err != nil {
		return contracts.PullRequest{}, err
	}
	spec := request{method: http.MethodPost, target: f.repoURL(draft.Repo, nil, "pulls"), body: payload}
	var pull contracts.PullRequest
	exchangeErr := f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		var wire pullWire
		if err := decodeJSON(resp.Body, &wire); err != nil {
			return err
		}
		ref, err := contracts.NewPullRequestRef(draft.Repo, wire.Number)
		if err != nil {
			return fmt.Errorf("opened pull request: %w", err)
		}
		converted, err := wire.toPullRequest(ref)
		if err != nil {
			return err
		}
		pull = converted
		return nil
	})
	return pull, exchangeErr
}

func mergeMethodName(method contracts.MergeMethod) (string, error) {
	switch method {
	case contracts.MergeMethodMerge:
		return "merge", nil
	case contracts.MergeMethodSquash:
		return "squash", nil
	case contracts.MergeMethodRebase:
		return "rebase", nil
	}
	return "", fmt.Errorf("%s: %w", method, errInvalidMergeMethod)
}

func (f *forge) MergePullRequest(ctx context.Context, ref contracts.PullRequestRef, method contracts.MergeMethod) error {
	if err := f.checkRepo(ref.Repo()); err != nil {
		return err
	}
	name, err := mergeMethodName(method)
	if err != nil {
		return err
	}
	payload, err := encodeJSON(mergeWire{MergeMethod: name})
	if err != nil {
		return err
	}
	spec := request{method: http.MethodPut, target: f.pullURL(ref, "merge"), body: payload, accepted: []int{http.StatusMethodNotAllowed, http.StatusConflict}}
	refused := false
	err = f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		refused = resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusConflict
		return nil
	})
	if err != nil || !refused {
		return err
	}
	read, err := f.fetchPull(ctx, ref, "")
	if err != nil {
		return err
	}
	pull, err := read.wire.toPullRequest(ref)
	if err != nil {
		return err
	}
	return &contracts.ForgeMergeRefusedError{State: pull.Merge}
}
