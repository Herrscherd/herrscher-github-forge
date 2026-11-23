package github

import (
	"context"
	"net/http"
	"strings"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type userWire struct {
	Login string `json:"login"`
}

type permissionsWire struct {
	Admin    bool `json:"admin"`
	Maintain bool `json:"maintain"`
	Push     bool `json:"push"`
	Triage   bool `json:"triage"`
	Pull     bool `json:"pull"`
}

type repoWire struct {
	Permissions permissionsWire `json:"permissions"`
}

func (p permissionsWire) repoPermission() contracts.RepoPermission {
	switch {
	case p.Admin:
		return contracts.PermissionAdmin
	case p.Maintain:
		return contracts.PermissionMaintain
	case p.Push:
		return contracts.PermissionWrite
	case p.Triage:
		return contracts.PermissionTriage
	case p.Pull:
		return contracts.PermissionRead
	}
	return contracts.PermissionNone
}

func (f *forge) Connection(ctx context.Context) (contracts.ForgeConnection, error) {
	var connection contracts.ForgeConnection
	spec := request{method: http.MethodGet, target: f.restURL(nil, "user")}
	err := f.exchange(ctx, spec, func(token contracts.ForgeToken, resp *http.Response) error {
		var user userWire
		if err := decodeJSON(resp.Body, &user); err != nil {
			return err
		}
		connection = contracts.ForgeConnection{
			Login:       user.Login,
			Source:      token.Source(),
			Permissions: scopesOf(resp.Header.Get(headerOAuthScopes)),
			RateLimit:   readRateLimit(resp.Header),
		}
		return nil
	})
	return connection, err
}

func scopesOf(header string) []string {
	var scopes []string
	for _, scope := range strings.Split(header, ",") {
		if trimmed := strings.TrimSpace(scope); trimmed != "" {
			scopes = append(scopes, trimmed)
		}
	}
	return scopes
}

func (f *forge) RepoAccess(ctx context.Context, repo contracts.ForgeRepoRef) (contracts.RepoPermission, error) {
	if err := f.checkRepo(repo); err != nil {
		return contracts.PermissionNone, err
	}
	var repository repoWire
	if err := f.getJSON(ctx, f.repoURL(repo, nil), &repository); err != nil {
		return contracts.PermissionNone, err
	}
	return repository.Permissions.repoPermission(), nil
}
