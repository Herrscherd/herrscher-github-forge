package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	listingPageSize = 100
	maxListingPages = 20
)

type listingState struct {
	firstETag string
	last      int
	firstFull bool
}

type pageHead struct {
	etag        string
	links       pageLinks
	notModified bool
}

func (f *forge) requestPage(ctx context.Context, target *url.URL, ifNoneMatch string, decode func(io.Reader) error) (pageHead, error) {
	spec := request{method: http.MethodGet, target: target, ifNoneMatch: ifNoneMatch}
	var head pageHead
	err := f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		head.etag = resp.Header.Get(headerETag)
		links, err := parsePageLinks(resp.Header.Get(headerLink))
		if err != nil {
			return err
		}
		head.links = links
		head.notModified = resp.StatusCode == http.StatusNotModified
		if head.notModified || decode == nil {
			return nil
		}
		return decode(resp.Body)
	})
	return head, err
}

func readListing[Item any](ctx context.Context, client *forge, listing func(number int) *url.URL, decodePage func(io.Reader) ([]Item, error)) ([]Item, listingState, error) {
	var items []Item
	var state listingState
	number := 1
	for read := 1; read <= maxListingPages; read++ {
		var page []Item
		head, err := client.requestPage(ctx, listing(number), "", func(body io.Reader) error {
			decoded, decodeErr := decodePage(body)
			page = decoded
			return decodeErr
		})
		if err != nil {
			return nil, listingState{}, err
		}
		if read == 1 {
			state.firstETag = head.etag
			state.last = max(head.links.last, 1)
			state.firstFull = len(page) == listingPageSize
		}
		items = append(items, page...)
		if head.links.next == 0 {
			return items, state, nil
		}
		number = head.links.next
	}
	return nil, listingState{}, errTooManyPages
}

func (f *forge) listingUnchanged(ctx context.Context, listing func(number int) *url.URL, prev listingState) (bool, error) {
	if prev.last != 1 || prev.firstFull {
		return false, nil
	}
	first, err := f.requestPage(ctx, listing(1), prev.firstETag, nil)
	if err != nil {
		return false, err
	}
	return first.notModified, nil
}

func (f *forge) pagedRepoURL(repo contracts.ForgeRepoRef, query url.Values, number int, elements ...string) *url.URL {
	query.Set("per_page", strconv.Itoa(listingPageSize))
	query.Set("page", strconv.Itoa(number))
	return f.repoURL(repo, query, elements...)
}

func degraded(err error) bool {
	return errors.Is(err, contracts.ErrForgeForbidden) || errors.Is(err, contracts.ErrForgeNotFound) || errors.Is(err, errTooManyPages)
}
