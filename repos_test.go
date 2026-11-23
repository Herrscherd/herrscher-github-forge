package github

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestConnectionReportsAccountAndQuota(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerOAuthScopes, "repo, read:org")
		for key, value := range rateHeader(4000, testNow().Add(time.Hour)) {
			w.Header()[key] = value
		}
		writeJSON(t, w, http.StatusOK, map[string]string{"login": "octocat"})
	})
	f, _ := newTestForge(t, mux)
	got, err := f.Connection(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Login != "octocat" || got.Source != contracts.TokenSourceSaved {
		t.Errorf("connection = %+v", got)
	}
	if len(got.Permissions) != 2 || got.Permissions[0] != "repo" || got.Permissions[1] != "read:org" {
		t.Errorf("permissions = %q", got.Permissions)
	}
	if got.RateLimit.Remaining != 4000 || !got.RateLimit.Reset.Equal(testNow().Add(time.Hour)) {
		t.Errorf("rate limit = %+v", got.RateLimit)
	}
}

func TestRepoAccessMapsPermissions(t *testing.T) {
	tests := []struct {
		name  string
		perms map[string]bool
		want  contracts.RepoPermission
	}{
		{name: "admin", perms: map[string]bool{"admin": true, "maintain": true, "push": true, "triage": true, "pull": true}, want: contracts.PermissionAdmin},
		{name: "maintain", perms: map[string]bool{"maintain": true, "push": true, "pull": true}, want: contracts.PermissionMaintain},
		{name: "write", perms: map[string]bool{"push": true, "pull": true}, want: contracts.PermissionWrite},
		{name: "triage", perms: map[string]bool{"triage": true, "pull": true}, want: contracts.PermissionTriage},
		{name: "read", perms: map[string]bool{"pull": true}, want: contracts.PermissionRead},
		{name: "none", perms: make(map[string]bool), want: contracts.PermissionNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /repos/acme/widgets", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, map[string]any{"permissions": test.perms})
			})
			f, _ := newTestForge(t, mux)
			got, err := f.RepoAccess(t.Context(), testRepo(t))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("permission = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRepoAccessRefusesNestedNamespace(t *testing.T) {
	var calls atomic.Int32
	f, _ := newTestForge(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	nested := mustRepo(t, "github.com/acme/team/widgets")
	if _, err := f.RepoAccess(t.Context(), nested); !errors.Is(err, contracts.ErrInvalidRepoRef) {
		t.Fatalf("err = %v, want ErrInvalidRepoRef", err)
	}
	if calls.Load() != 0 {
		t.Error("a request was sent for a nested namespace")
	}
}

func TestScopesOfSplitsAndTrims(t *testing.T) {
	got := scopesOf(" repo ,, read:org ,")
	if len(got) != 2 || got[0] != "repo" || got[1] != "read:org" {
		t.Errorf("scopes = %q", got)
	}
	if scopesOf("") != nil {
		t.Error("an empty header gave scopes")
	}
}
