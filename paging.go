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

func nextPageCursor(link string) (contracts.PageCursor, error) {
	for _, part := range strings.Split(link, ",") {
		target, relations, found := strings.Cut(part, ";")
		if !found || !strings.Contains(relations, `rel="next"`) {
			continue
		}
		next, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>"))
		if err != nil {
			return "", fmt.Errorf("link header next target: %w", errUnexpectedValue)
		}
		page := next.Query().Get("page")
		number, err := strconv.Atoi(page)
		if err != nil || number < 1 {
			return "", fmt.Errorf("link header next page %q: %w", page, errUnexpectedValue)
		}
		return contracts.PageCursor(page), nil
	}
	return "", nil
}
