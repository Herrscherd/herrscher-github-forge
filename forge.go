package github

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type forge struct {
	host     contracts.ForgeHost
	creds    contracts.ForgeCredentials
	api      endpoints
	client   *http.Client
	now      func() time.Time
	quota    quota
	slots    chan struct{}
	pageSize int
}

func newForge(host contracts.ForgeHost, creds contracts.ForgeCredentials, api endpoints, client *http.Client, now func() time.Time) *forge {
	policed := *client
	policed.CheckRedirect = dropAuthorizationAcrossOrigins
	return &forge{
		host:     host,
		creds:    creds,
		api:      api,
		client:   &policed,
		now:      now,
		slots:    make(chan struct{}, maxInFlight),
		pageSize: listingPageSize,
	}
}

func (f *forge) Manifest() contracts.Manifest {
	return manifest()
}

func (f *forge) checkRepo(repo contracts.ForgeRepoRef) error {
	if err := contracts.RequireRepoHost(f.host, repo); err != nil {
		return err
	}
	if strings.Contains(repo.Namespace(), "/") {
		return fmt.Errorf("%s: namespace has more than one segment: %w", repo, contracts.ErrInvalidRepoRef)
	}
	return nil
}

func (f *forge) restURL(query url.Values, elements ...string) *url.URL {
	target := f.api.rest.JoinPath(elements...)
	target.RawQuery = query.Encode()
	return target
}

func (f *forge) repoURL(repo contracts.ForgeRepoRef, query url.Values, elements ...string) *url.URL {
	path := slices.Concat([]string{"repos", repo.Namespace(), repo.Name()}, elements)
	return f.restURL(query, path...)
}

var _ contracts.Forge = (*forge)(nil)
