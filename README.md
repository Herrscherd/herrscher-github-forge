# herrscher-github-forge

The GitHub forge plugin of [herrscher](https://github.com/Herrscherd/herrscher). It implements the `Forge` port of [herrscher-contracts](https://github.com/Herrscherd/herrscher-contracts) for GitHub.com and GitHub Enterprise Server.

The plugin registers itself under the kind `github`, in the category `forge`. The host builds one forge per forge host from the registered factory. The plugin calls the REST and GraphQL APIs directly.

## What it does

- Connection: who the token belongs to, where the token came from, its scopes and the rate limit left.
- Repository access: the permission the account holds on one repository.
- Pull requests: open, read (with ETag revisions, so an unchanged pull request is not re-read), and merge with a chosen method.
- Checks: the check runs and commit statuses of a commit, paged. A check run that comes from GitHub Actions carries the identifier of its job, whose log can be read. A status or check run state the plugin does not know reads as queued, without a conclusion.
- Job logs: the tail of a CI job log, up to 1 MiB.
- Review threads: the threads of a pull request, and replies to them.
- Issues: one issue, and a filtered listing of issues (pull requests are left out of the listing and refused by number).

## Endpoints

| Host | REST | GraphQL |
|---|---|---|
| `github.com` | `https://api.github.com` | `https://api.github.com/graphql` |
| any other host (GitHub Enterprise Server) | `https://<host>/api/v3` | `https://<host>/api/graphql` |

## Credentials

The plugin never reads a token from the environment, from a file, or from the `gh` command line. For each request it asks the host's `ForgeCredentials` for the token of its forge host and sends it as `Authorization: Bearer`. When GitHub answers 401, the plugin calls `Invalidate` so the host drops the token it cached.
Every request also sends `Accept: application/vnd.github+json` and `X-GitHub-Api-Version: 2022-11-28`.

## Errors

Failures wrap the sentinels and error types of the contracts port:

| Answer | Error |
|---|---|
| no credential for the host | `ErrForgeNotConnected` |
| 401 | `ErrForgeUnauthorized` (and `Invalidate` is called) |
| 403 without a throttle signal | `ErrForgeForbidden` |
| 403 with `x-ratelimit-remaining: 0` or `Retry-After`, or 429 | `ForgeRateLimitedError` |
| 404, or a GraphQL `NOT_FOUND` | `ErrForgeNotFound` |
| 304 on a pull request read with a revision | `ErrForgeNotModified` |
| merge refused with 405 or 409 | `ForgeMergeRefusedError`, with the merge state read back from the forge |
| `Issue` called with the number of a pull request | `ErrNamesPullRequest`, which also matches `ErrForgeNotFound` |
| issue listing with a label that holds a comma | `ErrInvalidLabel`, and no request is sent |

A repository on another host is refused with `ErrForgeWrongHost`, and a namespace of more than one segment with `ErrInvalidRepoRef`.

## Limits and quota

- Response bodies are read up to 8 MiB. A larger body is refused.
- At most 4 requests are in flight at once. A caller waiting for a slot stops when its context is done.
- Every response updates the quota the plugin holds for the token (`x-ratelimit-limit`, `-remaining`, `-reset`). The quota is only taken from a response that carries both the limit and the remaining count. When the remaining budget is below 10% of the limit and the reset is still ahead, further calls are refused with `ForgeRateLimitedError` without being sent.
- The rate limit a connection reports has `Remaining` set to -1 when the response carries no `x-ratelimit-remaining` header, since the contract has no unknown state.
- A throttle `Retry-After` is read as a number of seconds or as an HTTP date.
- Job logs: the plugin keeps only the last bytes asked for. If the log is larger than 256 MiB it refuses with an error rather than returning a partial tail.
- Redirects: the plugin follows at most 10 redirects. The `Authorization` header is removed when a redirect changes the scheme, the host or the port of the origin it was sent to, so a job log served from storage on another host, or from a plain HTTP address, never receives the token.
- Timeouts: the TLS handshake may take 10 s, the response headers 30 s, and an idle connection is kept 90 s. There is no timeout on the whole request, so a job log can stream.
- Listings take a page of at most 100 items (default 30). A larger limit is refused with `ErrInvalidPageLimit`.
- Review threads: each thread returns all its comments, read 100 per request. A thread with more than 20 pages of comments is refused with an error rather than returned partly.

## Development

The quality chain is `scripts/check.sh`: tidy, the golangci-lint set, the comment check (`tools/commentcheck`), the dash check, the tests with `-race`, the build with `CGO_ENABLED=0`, and `govulncheck`. CI runs the same script.

Tests run against `httptest` servers only. No test reaches the real GitHub.

## License

See [LICENSE](LICENSE).
