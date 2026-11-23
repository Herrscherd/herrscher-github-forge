package github

import (
	"fmt"
	"net/url"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	publicHost            = "github.com"
	publicRESTBase        = "https://api.github.com"
	publicGraphQLEndpoint = "https://api.github.com/graphql"
	enterpriseRESTPath    = "/api/v3"
	enterpriseGraphQLPath = "/api/graphql"
)

type endpoints struct {
	rest    *url.URL
	graphql *url.URL
}

func endpointsFor(host contracts.ForgeHost) (endpoints, error) {
	if host.String() == publicHost {
		return parseEndpoints(publicRESTBase, publicGraphQLEndpoint)
	}
	base := "https://" + host.String()
	return parseEndpoints(base+enterpriseRESTPath, base+enterpriseGraphQLPath)
}

func parseEndpoints(restBase, graphqlEndpoint string) (endpoints, error) {
	rest, err := url.Parse(restBase)
	if err != nil {
		return endpoints{}, fmt.Errorf("REST endpoint %q: %w", restBase, err)
	}
	graphql, err := url.Parse(graphqlEndpoint)
	if err != nil {
		return endpoints{}, fmt.Errorf("GraphQL endpoint %q: %w", graphqlEndpoint, err)
	}
	return endpoints{rest: rest, graphql: graphql}, nil
}
