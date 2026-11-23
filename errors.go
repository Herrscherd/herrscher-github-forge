package github

import (
	"errors"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const graphQLNotFound = "NOT_FOUND"

var (
	errNoCredentials      = errors.New("no forge credentials were given to the GitHub plugin")
	errInvalidMergeMethod = errors.New("merge method names no method GitHub offers")
	errUnknownIssueState  = errors.New("issue state is neither open nor closed")
	errInvalidCursor      = errors.New("page cursor is not a page number of this listing")
	errUnexpectedStatus   = errors.New("forge answered with an unexpected status")
	errUnexpectedValue    = errors.New("forge reported a value the plugin cannot map")
	errResponseTooLarge   = errors.New("forge response exceeds the body limit")
	errJobLogTooLarge     = errors.New("job log exceeds the read limit")
	errTooManyRedirects   = errors.New("forge redirected too many times")
	errMissingCommit      = errors.New("commit is the zero value and names no commit")
)

type graphQLError struct {
	kind    string
	message string
}

func (e *graphQLError) Error() string {
	return "forge GraphQL error " + e.kind + ": " + e.message
}

func (e *graphQLError) Unwrap() error {
	if e.kind == graphQLNotFound {
		return contracts.ErrForgeNotFound
	}
	return nil
}
