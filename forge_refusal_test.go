package github

import (
	"net/http"
	"testing"

	"github.com/Herrscherd/herrscher-contracts/forgetest"
)

func TestRefusesForeignHostForEveryMethod(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a foreign-host call reached the server: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusInternalServerError)
	})
	f, _ := newTestForge(t, handler)
	forgetest.CheckRefusesForeignHost(t, f, mustRepo(t, "ghe.example.com/acme/widgets"))
}
