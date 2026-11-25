package github

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const defaultPageSize = 30

func pageLimit(page contracts.PageRequest) (int, error) {
	if err := page.Validate(); err != nil {
		return 0, err
	}
	if page.Limit == 0 {
		return defaultPageSize, nil
	}
	return page.Limit, nil
}

func cursorVariable(cursor contracts.PageCursor) any {
	if cursor == "" {
		return nil
	}
	return string(cursor)
}

func pageNumber(cursor contracts.PageCursor) (int, error) {
	if cursor == "" {
		return 1, nil
	}
	number, err := strconv.Atoi(string(cursor))
	if err != nil || number < 1 {
		return 0, fmt.Errorf("%q: %w", cursor, errInvalidCursor)
	}
	return number, nil
}

type pageLinks struct {
	next int
	last int
}

func parsePageLinks(link string) (pageLinks, error) {
	var links pageLinks
	for _, part := range strings.Split(link, ",") {
		target, relations, found := strings.Cut(part, ";")
		if !found {
			continue
		}
		switch {
		case strings.Contains(relations, `rel="next"`):
			number, err := linkPageNumber(target)
			if err != nil {
				return pageLinks{}, err
			}
			links.next = number
		case strings.Contains(relations, `rel="last"`):
			number, err := linkPageNumber(target)
			if err != nil {
				return pageLinks{}, err
			}
			links.last = number
		}
	}
	return links, nil
}

func linkPageNumber(target string) (int, error) {
	link, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>"))
	if err != nil {
		return 0, fmt.Errorf("link header target: %w", errUnexpectedValue)
	}
	page := link.Query().Get("page")
	number, err := strconv.Atoi(page)
	if err != nil || number < 1 {
		return 0, fmt.Errorf("link header page %q: %w", page, errUnexpectedValue)
	}
	return number, nil
}

func nextPageCursor(link string) (contracts.PageCursor, error) {
	links, err := parsePageLinks(link)
	if err != nil {
		return "", err
	}
	if links.next == 0 {
		return "", nil
	}
	return contracts.PageCursor(strconv.Itoa(links.next)), nil
}
