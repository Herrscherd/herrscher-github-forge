package github

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	repoAffiliation = "owner,collaborator,organization_member"
	repoSortName    = "full_name"
	repoDirection   = "asc"
)

type repoOwnerWire struct {
	Login string `json:"login"`
}

type repoListWire struct {
	Owner         repoOwnerWire `json:"owner"`
	Name          string        `json:"name"`
	CloneURL      string        `json:"clone_url"`
	Description   *string       `json:"description"`
	Private       bool          `json:"private"`
	Archived      bool          `json:"archived"`
	DefaultBranch string        `json:"default_branch"`
}

func (f *forge) Repositories(ctx context.Context, page contracts.PageRequest) (contracts.Page[contracts.ForgeRepo], error) {
	limit, err := pageLimit(page)
	if err != nil {
		return contracts.Page[contracts.ForgeRepo]{}, err
	}
	number, err := pageNumber(page.Cursor)
	if err != nil {
		return contracts.Page[contracts.ForgeRepo]{}, err
	}
	values := url.Values{}
	values.Set("affiliation", repoAffiliation)
	values.Set("sort", repoSortName)
	values.Set("direction", repoDirection)
	values.Set("per_page", strconv.Itoa(limit))
	values.Set("page", strconv.Itoa(number))
	spec := request{method: http.MethodGet, target: f.restURL(values, "user", "repos")}
	var items []contracts.ForgeRepo
	var next contracts.PageCursor
	exchangeErr := f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		var wires []repoListWire
		if err := decodeJSON(resp.Body, &wires); err != nil {
			return err
		}
		items = f.listedRepos(wires)
		var cursorErr error
		next, cursorErr = nextPageCursor(resp.Header.Get(headerLink))
		return cursorErr
	})
	if exchangeErr != nil {
		return contracts.Page[contracts.ForgeRepo]{}, exchangeErr
	}
	return contracts.Page[contracts.ForgeRepo]{Items: items, Next: next}, nil
}

func (f *forge) listedRepos(wires []repoListWire) []contracts.ForgeRepo {
	repos := make([]contracts.ForgeRepo, 0, len(wires))
	for _, wire := range wires {
		repo, ok := f.listedRepo(wire)
		if !ok {
			continue
		}
		repos = append(repos, repo)
	}
	return repos
}

func (f *forge) listedRepo(wire repoListWire) (contracts.ForgeRepo, bool) {
	repo, err := contracts.ParseRepoRef(f.host.String() + "/" + wire.Owner.Login + "/" + wire.Name)
	if err != nil {
		return contracts.ForgeRepo{}, false
	}
	branch, err := branchOf(wire.DefaultBranch)
	if err != nil {
		return contracts.ForgeRepo{}, false
	}
	listed, err := contracts.NewForgeRepo(f.host, contracts.ForgeRepoFields{
		Repo:          repo,
		CloneURL:      wire.CloneURL,
		Description:   descriptionOf(wire.Description),
		Private:       wire.Private,
		Archived:      wire.Archived,
		DefaultBranch: branch,
	})
	if err != nil {
		return contracts.ForgeRepo{}, false
	}
	return listed, true
}

func branchOf(text string) (contracts.BranchName, error) {
	if text == "" {
		return contracts.BranchName{}, nil
	}
	return contracts.ParseBranchName(text)
}

func descriptionOf(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}
