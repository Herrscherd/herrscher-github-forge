package github

import (
	"context"
	"errors"
	"net/http"
	"testing"

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
