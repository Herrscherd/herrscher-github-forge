package github

import (
	"context"
	"net/http"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const kind = "github"

func init() {
	contracts.Register(contracts.Plugin{
		Manifest: manifest(),
		Forge: func(_ context.Context, _ contracts.PluginConfig, host contracts.ForgeHost, creds contracts.ForgeCredentials) (contracts.Forge, error) {
			return newForgeFor(host, creds)
		},
	})
}

func manifest() contracts.Manifest {
	return contracts.Manifest{Kind: kind, Category: contracts.CategoryForge}
}

func newForgeFor(host contracts.ForgeHost, creds contracts.ForgeCredentials) (contracts.Forge, error) {
	if host == (contracts.ForgeHost{}) {
		return nil, contracts.ErrInvalidForgeHost
	}
	if creds == nil {
		return nil, errNoCredentials
	}
	api, err := endpointsFor(host)
	if err != nil {
		return nil, err
	}
	return newForge(host, creds, api, &http.Client{}, time.Now), nil
}
