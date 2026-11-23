package github

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestRegisteredAsGitHubForgePlugin(t *testing.T) {
	var found bool
	for _, plugin := range contracts.Default.Forges() {
		if plugin.Manifest.Kind != kind {
			continue
		}
		found = true
		if plugin.Manifest.Category != contracts.CategoryForge {
			t.Errorf("category = %v, want forge", plugin.Manifest.Category)
		}
		if plugin.Forge == nil {
			t.Fatal("registered plugin has no Forge factory")
		}
		built, err := plugin.Forge(context.Background(), contracts.PluginConfig{}, mustHost(t), &fakeCreds{token: mustToken(t)})
		if err != nil {
			t.Fatal(err)
		}
		if built.Manifest().Kind != kind {
			t.Errorf("built forge kind = %q, want %q", built.Manifest().Kind, kind)
		}
	}
	if !found {
		t.Fatal("kind github is not registered as a forge plugin")
	}
}

func TestNewForgeForRefusesMissingInputs(t *testing.T) {
	if _, err := newForgeFor(contracts.ForgeHost{}, &fakeCreds{}); !errors.Is(err, contracts.ErrInvalidForgeHost) {
		t.Errorf("zero host: err = %v, want ErrInvalidForgeHost", err)
	}
	if _, err := newForgeFor(mustHost(t), nil); !errors.Is(err, errNoCredentials) {
		t.Errorf("nil credentials: err = %v, want errNoCredentials", err)
	}
}

func TestPluginClientBoundsEachPhaseWithoutWholeRequestTimeout(t *testing.T) {
	client := newPluginClient()
	if client.Timeout != 0 {
		t.Errorf("Client.Timeout = %v, want none so job logs can stream", client.Timeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 10s", transport.TLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout != 30*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 30s", transport.ResponseHeaderTimeout)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 90s", transport.IdleConnTimeout)
	}
}

func TestNewForgeClientKeepsCallerRedirectPolicy(t *testing.T) {
	client := &http.Client{}
	built := newForge(mustHost(t), &fakeCreds{}, endpoints{}, client, fixedClock)
	if client.CheckRedirect != nil {
		t.Error("newForge changed the CheckRedirect of the caller's client")
	}
	if built.client.CheckRedirect == nil {
		t.Error("the forge client has no redirect policy")
	}
}
