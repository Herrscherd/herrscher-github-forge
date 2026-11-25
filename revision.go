package github

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	revisionVersion = "v1"
	listingFields   = 2
)

type revisionState struct {
	pull    string
	head    contracts.CommitSHA
	checks  listingState
	status  listingState
	reviews listingState
}

func (s revisionState) revision() contracts.Revision {
	fields := []string{
		revisionVersion,
		"pr=" + url.QueryEscape(s.pull),
		"head=" + headText(s.head),
		"checks=" + s.checks.encode(),
		"status=" + s.status.encode(),
		"reviews=" + s.reviews.encode(),
	}
	return contracts.Revision(strings.Join(fields, ";"))
}

func parseRevision(text contracts.Revision) (revisionState, error) {
	keys := [...]string{"pr", "head", "checks", "status", "reviews"}
	fields := strings.Split(string(text), ";")
	if len(fields) != len(keys)+1 || fields[0] != revisionVersion {
		return revisionState{}, unreadableRevision(text, nil)
	}
	values := make([]string, len(keys))
	for index, key := range keys {
		value, err := keyedField(fields[index+1], key)
		if err != nil {
			return revisionState{}, unreadableRevision(text, err)
		}
		values[index] = value
	}
	pull, err := url.QueryUnescape(values[0])
	if err != nil {
		return revisionState{}, unreadableRevision(text, err)
	}
	head, err := parseHead(values[1])
	if err != nil {
		return revisionState{}, unreadableRevision(text, err)
	}
	checks, err := parseListing(values[2])
	if err != nil {
		return revisionState{}, unreadableRevision(text, err)
	}
	status, err := parseListing(values[3])
	if err != nil {
		return revisionState{}, unreadableRevision(text, err)
	}
	reviews, err := parseListing(values[4])
	if err != nil {
		return revisionState{}, unreadableRevision(text, err)
	}
	return revisionState{pull: pull, head: head, checks: checks, status: status, reviews: reviews}, nil
}

func unreadableRevision(text contracts.Revision, cause error) error {
	if cause == nil {
		return fmt.Errorf("revision %s: %w", quotedValue(string(text)), errUnreadableRevision)
	}
	return fmt.Errorf("revision %s: %w: %w", quotedValue(string(text)), errUnreadableRevision, cause)
}

func keyedField(field, key string) (string, error) {
	name, value, found := strings.Cut(field, "=")
	if !found || name != key {
		return "", fmt.Errorf("field %q is not %s: %w", quotedValue(field), key, errUnreadableRevision)
	}
	return value, nil
}

func headText(head contracts.CommitSHA) string {
	if head == (contracts.CommitSHA{}) {
		return ""
	}
	return head.String()
}

func parseHead(text string) (contracts.CommitSHA, error) {
	if text == "" {
		return contracts.CommitSHA{}, nil
	}
	return contracts.ParseCommitSHA(text)
}

func (s listingState) encode() string {
	return url.QueryEscape(s.firstETag) + "," + strconv.Itoa(s.last)
}

func parseListing(text string) (listingState, error) {
	parts := strings.Split(text, ",")
	if len(parts) != listingFields {
		return listingState{}, fmt.Errorf("listing %s: %w", quotedValue(text), errUnreadableRevision)
	}
	first, err := url.QueryUnescape(parts[0])
	if err != nil {
		return listingState{}, err
	}
	last, err := strconv.Atoi(parts[1])
	if err != nil || last < 0 {
		return listingState{}, fmt.Errorf("listing last page %s: %w", quotedValue(parts[1]), errUnreadableRevision)
	}
	return listingState{firstETag: first, last: last}, nil
}
