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

func (p pullWire) toPullRequest(ref contracts.PullRequestRef, revision contracts.Revision) (contracts.PullRequest, error) {
	head, err := contracts.ParseCommitSHA(p.Head.SHA)
	if err != nil {
		return contracts.PullRequest{}, fmt.Errorf("pull request head: %w", err)
	}
	return contracts.PullRequest{
		Ref:      ref,
		Title:    p.Title,
		URL:      p.HTMLURL,
		Head:     p.Head.Ref,
		Base:     p.Base.Ref,
		HeadSHA:  head,
		State:    p.pullState(),
		Draft:    p.Draft,
		Merge:    mergeStateOf(p.MergeableState),
		Author:   p.User.forgeActor(p.AuthorAssociation),
		Revision: revision,
	}, nil
}

func (f *forge) pullURL(ref contracts.PullRequestRef, elements ...string) *url.URL {
	path := slices.Concat([]string{"pulls", strconv.Itoa(ref.Number())}, elements)
	return f.repoURL(ref.Repo(), nil, path...)
}

func (f *forge) PullRequest(ctx context.Context, ref contracts.PullRequestRef, since contracts.Revision) (contracts.PullRequest, error) {
	if err := f.checkRepo(ref.Repo()); err != nil {
		return contracts.PullRequest{}, err
	}
	spec := request{method: http.MethodGet, target: f.pullURL(ref), ifNoneMatch: string(since)}
	var pull contracts.PullRequest
	err := f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		if resp.StatusCode == http.StatusNotModified {
			return contracts.ErrForgeNotModified
		}
		var wire pullWire
		if err := decodeJSON(resp.Body, &wire); err != nil {
			return err
		}
		converted, err := wire.toPullRequest(ref, contracts.Revision(resp.Header.Get(headerETag)))
		if err != nil {
			return err
		}
		pull = converted
		return nil
	})
	return pull, err
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
		converted, err := wire.toPullRequest(ref, contracts.Revision(resp.Header.Get(headerETag)))
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
	pull, err := f.PullRequest(ctx, ref, "")
	if err != nil {
		return err
	}
	return &contracts.ForgeMergeRefusedError{State: pull.Merge}
}
