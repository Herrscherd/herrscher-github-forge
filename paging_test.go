package github

import (
	"errors"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestPageLimitRange(t *testing.T) {
	tests := []struct {
		limit   int
		want    int
		wantErr bool
	}{
		{limit: 0, want: defaultPageSize},
		{limit: 1, want: 1},
		{limit: contracts.MaxPageLimit, want: contracts.MaxPageLimit},
		{limit: contracts.MaxPageLimit + 1, wantErr: true},
		{limit: -1, wantErr: true},
	}
	for _, test := range tests {
		got, err := pageLimit(contracts.PageRequest{Limit: test.limit})
		if test.wantErr {
			if !errors.Is(err, contracts.ErrInvalidPageLimit) {
				t.Errorf("limit %d: err = %v, want ErrInvalidPageLimit", test.limit, err)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Errorf("limit %d = %d, %v; want %d", test.limit, got, err, test.want)
		}
	}
}

func TestCursorVariable(t *testing.T) {
	if cursorVariable("") != nil {
		t.Error("empty cursor should be sent as null")
	}
	if cursorVariable("abc") != "abc" {
		t.Error("cursor value not passed through")
	}
}

func TestNextPageCursorRejectsMalformedTarget(t *testing.T) {
	if _, err := nextPageCursor(`<http://[::1>; rel="next"`); !errors.Is(err, errUnexpectedValue) {
		t.Fatalf("err = %v, want errUnexpectedValue", err)
	}
}

func TestNextPageCursorRefusesNextPageThatIsNotANumber(t *testing.T) {
	links := []string{
		`<https://api.github.com/x?page=abc>; rel="next"`,
		`<https://api.github.com/x?page=0>; rel="next"`,
		`<https://api.github.com/x?page=-1>; rel="next"`,
		`<https://api.github.com/x>; rel="next"`,
	}
	for _, link := range links {
		if _, err := nextPageCursor(link); !errors.Is(err, errUnexpectedValue) {
			t.Errorf("link %q: err = %v, want errUnexpectedValue", link, err)
		}
	}
}

func TestAssociationOf(t *testing.T) {
	tests := map[string]contracts.AuthorAssociation{
		"":                       contracts.AssociationNone,
		"NONE":                   contracts.AssociationNone,
		"OWNER":                  contracts.AssociationOwner,
		"MEMBER":                 contracts.AssociationMember,
		"COLLABORATOR":           contracts.AssociationCollaborator,
		"CONTRIBUTOR":            contracts.AssociationContributor,
		"FIRST_TIMER":            contracts.AssociationFirstTimer,
		"FIRST_TIME_CONTRIBUTOR": contracts.AssociationFirstTimer,
		"MANNEQUIN":              contracts.AssociationOther,
	}
	for text, want := range tests {
		if got := associationOf(text); got != want {
			t.Errorf("associationOf(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestActorBotFlagFromType(t *testing.T) {
	if !(actorWire{Login: "dependabot", Type: botType}).forgeActor("").Bot {
		t.Error("a Bot account is not flagged as a bot")
	}
	if (actorWire{Login: "octocat", Type: "User"}).forgeActor("").Bot {
		t.Error("a User account is flagged as a bot")
	}
}
