package github

import (
	"context"
	"net"
	"net/http"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const kind = "github"

const (
	dialTimeout           = 30 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 30 * time.Second
	idleConnTimeout       = 90 * time.Second
)

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

func newTransport() *http.Transport {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if standard, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = standard.Clone()
	}
	transport.DialContext = (&net.Dialer{Timeout: dialTimeout}).DialContext
	transport.TLSHandshakeTimeout = tlsHandshakeTimeout
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	transport.IdleConnTimeout = idleConnTimeout
	return transport
}

func newPluginClient() *http.Client {
	return &http.Client{Transport: newTransport()}
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
	return newForge(host, creds, api, newPluginClient(), time.Now), nil
}
