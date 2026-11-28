package github

import (
	"errors"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const graphQLNotFound = "NOT_FOUND"

var (
	// ErrNamesPullRequest reports that the number given names a pull request, which Issue does not return as an issue.
	ErrNamesPullRequest = errors.New("the number names a pull request, not an issue")
	// ErrInvalidLabel reports a label that holds a comma, which an issue listing cannot filter by.
	ErrInvalidLabel = errors.New("label contains a comma, which an issue listing cannot filter by")
	// ErrFeedSilent reports that the pull request event feed sent no heartbeat for about three heartbeat periods, and that the stream is treated as dead.
	ErrFeedSilent = errors.New("pull request event feed went silent")
	// ErrFeedEnded reports that the forge ended the pull request event feed, which the caller reads on a cadence after.
	ErrFeedEnded = errors.New("pull request event feed ended")
)

var (
	errNoCredentials       = errors.New("no forge credentials were given to the GitHub plugin")
	errInvalidMergeMethod  = errors.New("merge method names no method GitHub offers")
	errUnknownIssueState   = errors.New("issue state is neither open nor closed")
	errInvalidCursor       = errors.New("page cursor is not a page number of this listing")
	errUnexpectedStatus    = errors.New("forge answered with an unexpected status")
	errUnexpectedValue     = errors.New("forge reported a value the plugin cannot map")
	errResponseTooLarge    = errors.New("forge response exceeds the body limit")
	errJobLogTooLarge      = errors.New("job log exceeds the read limit")
	errTooManyRedirects    = errors.New("forge redirected too many times")
	errMissingCommit       = errors.New("commit is the zero value and names no commit")
	errMissingBranch       = errors.New("branch is the zero value and names no branch")
	errBranchNotProtected  = errors.New("forge answered that the branch has no classic protection")
	errProtectionUnread    = errors.New("classic protection answered not found on a protected branch")
	errTooManyCommentPages = errors.New("review thread has more comment pages than the plugin reads")
	errRefusedRepo         = errors.New("repository is refused by the contracts port and left out of the listing")
	errTooManyPages        = errors.New("listing has more pages than the plugin reads")
	errUnreadableRevision  = errors.New("revision is not one this plugin wrote")
	errFeedEventTooLarge   = errors.New("pull request event exceeds the read limit")
	errNoSubscription      = errors.New("forge created no event subscription")
	errNotEventStream      = errors.New("forge answered the event feed with a body that is not an event stream")
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
