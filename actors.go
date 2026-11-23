package github

import contracts "github.com/Herrscherd/herrscher-contracts"

const (
	botType = "Bot"

	associationOwner            = "OWNER"
	associationMember           = "MEMBER"
	associationCollaborator     = "COLLABORATOR"
	associationContributor      = "CONTRIBUTOR"
	associationFirstTimer       = "FIRST_TIMER"
	associationFirstTimeContrib = "FIRST_TIME_CONTRIBUTOR"
	associationNone             = "NONE"
)

type actorWire struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

func (a actorWire) forgeActor(association string) contracts.ForgeActor {
	return contracts.ForgeActor{
		Login:       a.Login,
		ID:          a.ID,
		Association: associationOf(association),
		Bot:         a.Type == botType,
	}
}

func associationOf(text string) contracts.AuthorAssociation {
	switch text {
	case "", associationNone:
		return contracts.AssociationNone
	case associationOwner:
		return contracts.AssociationOwner
	case associationMember:
		return contracts.AssociationMember
	case associationCollaborator:
		return contracts.AssociationCollaborator
	case associationContributor:
		return contracts.AssociationContributor
	case associationFirstTimer, associationFirstTimeContrib:
		return contracts.AssociationFirstTimer
	}
	return contracts.AssociationOther
}
