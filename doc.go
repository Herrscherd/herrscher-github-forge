// Package github is the forge plugin of herrscher for GitHub.com and GitHub
// Enterprise Server. A blank import registers it under the kind "github", in
// the category forge, and the host builds one forge per forge host from its
// factory. It calls the REST and GraphQL APIs directly and takes every token
// from the host's ForgeCredentials, never from the environment or from the
// command line of gh.
package github
