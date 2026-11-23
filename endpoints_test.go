package github

import (
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func TestEndpointsFor(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		rest    string
		graphql string
	}{
		{name: "github.com", host: "github.com", rest: "https://api.github.com", graphql: "https://api.github.com/graphql"},
		{name: "enterprise server", host: "ghe.example.com", rest: "https://ghe.example.com/api/v3", graphql: "https://ghe.example.com/api/graphql"},
		{name: "enterprise with port", host: "ghe.example.com:8443", rest: "https://ghe.example.com:8443/api/v3", graphql: "https://ghe.example.com:8443/api/graphql"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host, err := contracts.ParseForgeHost(test.host)
			if err != nil {
				t.Fatal(err)
			}
			api, err := endpointsFor(host)
			if err != nil {
				t.Fatal(err)
			}
			if got := api.rest.String(); got != test.rest {
				t.Errorf("REST = %q, want %q", got, test.rest)
			}
			if got := api.graphql.String(); got != test.graphql {
				t.Errorf("GraphQL = %q, want %q", got, test.graphql)
			}
		})
	}
}

func TestParseEndpointsRejectsBadURL(t *testing.T) {
	if _, err := parseEndpoints("http://[::1", "https://api.example.com/graphql"); err == nil {
		t.Error("a malformed REST base was accepted")
	}
}
