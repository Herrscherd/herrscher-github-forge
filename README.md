# herrscher-github-forge

The GitHub forge plugin of [herrscher](https://github.com/Herrscherd/herrscher). It implements the `Forge` port of [herrscher-contracts](https://github.com/Herrscherd/herrscher-contracts) for GitHub.com and GitHub Enterprise Server.

The plugin registers itself under the kind `github`, in the category `forge`. The host builds one forge per forge host from the registered factory. The plugin calls the REST and GraphQL APIs directly.

## What it does

- Connection: who the token belongs to, where the token came from, its scopes and the rate limit left.
- Repository access: the permission the account holds on one repository.
- Repositories: the repositories the account can read (owned, collaborated on, or through an organization membership), listed a page at a time by name. A repository is left out of the page, without failing it, in three cases: its owner or name is refused by `ParseRepoRef`, its default branch name is refused by `ParseBranchName`, or `NewForgeRepo` refuses it, which happens when its clone URL is not an https URL of that repository on the forge host (a clone URL naming another repository, or served from another host). The contracts port asks for this skip, and the plugin does not report it: one bad repository must not fail the other repositories of the page, and the plugin has no logger to report a skipped one. A page can be empty while `Next` is not. When every repository of a page is skipped, the page comes back with no items and the `Next` the forge gave; the plugin does not request further pages to fill it, so the caller follows `Next` until it is empty.
- Pull requests: open, read, and merge with a chosen method. A read returns a revision, an opaque composite of the validators of the pull request and of every listing the read covers (see Revisions below). Passed back as `since`, it lets an unchanged pull request cost only conditional requests that answer 304, and the read returns `ErrForgeNotModified` when all of them do. `OpenPullRequest` returns no revision, so the first read after opening is a full read.
- Checks: the check runs and commit statuses of a commit, paged. On the pull request path, the checks come from the REST check runs (`filter=latest`) and the combined commit status, which accept `If-None-Match`, not from the GraphQL rollup. A check run or status that the forge refuses (403, or 404 for a missing permission) does not fail the pull request: it is returned with `ChecksUnknown`. Any other failure is returned as an error. `Checks` reads the same two listings through the same mapping: the check runs first, then the statuses, cut into pages by an offset cursor. Each call reads both listings in full (up to 20 pages each) and returns the requested slice, and a refused listing is an error rather than unknown checks. A check run that comes from GitHub Actions carries the identifier of its job, whose log can be read. A check run status, a status context state or a check kind the plugin does not know fails the read of the checks with an error that quotes the value, and no checks are returned. A check is never reported as waiting or finished by default, so a caller waiting for completion cannot loop on a state the plugin never mapped.
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
| 304 on every request of a pull request read with a revision (the pull request and each listing the revision covers) | `ErrForgeNotModified` |
| 403 or 404 on the check runs or the commit statuses of a pull request | the pull request is returned with `ChecksUnknown` and no error |
| 403 or 404 on the reviews of a pull request | the pull request is returned with an empty `LatestReview` and no error |
| 403 on the check runs or the commit statuses of `Checks` | `ErrForgeForbidden`, with no checks returned |
| a revision that is empty, malformed or of another version | read in full, with no conditional header |
| merge refused with 405 or 409 | `ForgeMergeRefusedError`, with the merge state read back from the forge |
| `Issue` called with the number of a pull request | `ErrNamesPullRequest`, which also matches `ErrForgeNotFound` |
| issue listing with a label that holds a comma | `ErrInvalidLabel`, and no request is sent |

A repository on another host is refused with `ErrForgeWrongHost`, and a namespace of more than one segment with `ErrInvalidRepoRef`.

## Limits and quota

- Response bodies are read up to 8 MiB. A larger body is refused.
- At most 4 requests are in flight at once. A caller waiting for a slot stops when its context is done.
- Only REST core responses update the quota the plugin holds for the token (`x-ratelimit-limit`, `-remaining`, `-reset`, when `x-ratelimit-resource` is `core` or absent). A GraphQL response carries its own budget and is ignored for the quota, and GraphQL calls are not refused by the REST budget. Each REST core response that is not a 304 costs one request of that budget, and a 304 costs none. The quota is only taken from a response that carries both the limit and the remaining count. When the remaining budget is below 10% of the limit and the reset is still ahead, further calls are refused with `ForgeRateLimitedError` without being sent. A response with `x-ratelimit-remaining: 0` and no reset header does not refuse calls locally: the forge did not say when the budget refills, so the call is sent and the answer is taken as usual.
- Responses that arrive out of order cannot roll the quota back. Within one reset window the lowest remaining count is kept. A window that resets later replaces the earlier one, and a response of an earlier window is ignored.
- The rate limit a connection reports is the zero `ForgeRateLimit` (`Remaining` 0, `Reset` the zero time) when the response carries no `x-ratelimit-remaining` header: the forge did not say, as the contract documents for a missing reset.
- A throttle `Retry-After` is read as a number of seconds or as an HTTP date.
- Job logs: the plugin keeps only the last bytes asked for. If the log is larger than 256 MiB it refuses with an error rather than returning a partial tail.
- Redirects: the plugin follows at most 10 redirects. The `Authorization` header is removed when a redirect changes the scheme, the host or the port of the origin it was sent to, so a job log served from storage on another host, or from a plain HTTP address, never receives the token.
- Timeouts: the TLS handshake may take 10 s, the response headers 30 s, and an idle connection is kept 90 s. There is no timeout on the whole request, so a job log can stream.
- Revisions: the revision of a pull request is `v1;pr=…;head=…;checks=…;status=…;reviews=…`, where `pr` is the ETag of the pull request, `head` its head commit, and each listing holds the ETag of its first page and its page count from `Link` `rel="last"`. A revision that does not parse as this version is treated as absent. A listing is never read partly: on the pull request path, a listing that spans more than 20 pages degrades like a refused one, and `Checks` returns an error for it. On a 304 of the pull request, the plugin probes the check runs, the commit statuses and the reviews of the revision's head with their validators, and re-reads the pull request in full when any of them changed; a head that changed invalidates the validators of the old head because the pull request itself then answers 200. A listing that the revision records as spanning more than one page never takes the 304 shortcut: its probe is skipped and the read is full, so a change on a middle page is always seen. A dismissed review still counts, because its id does not change.
- Request cost of a read with a revision. When nothing changed and every listing is one page, the read sends 4 requests, all answered 304, which cost no quota. A listing that spans more than one page is never probed with a 304, so the probes stop at the first such listing. With P, S and R the page counts of the check runs, the statuses and the reviews, and the pull request unchanged, a read where the check runs span more than one page sends 2 + P + S + R requests: one 304 for the pull request, one full read of the pull request, then every page of the three listings. Of these, P + S + R + 1 cost quota (the 304 does not). Example: 5 check runs at 2 per page (P = 3), 1 status (S = 1) and 1 review (R = 1) make 7 requests, 6 of which cost quota.
- Listings take a page of at most 100 items (default 30). A larger limit is refused with `ErrInvalidPageLimit`.
- A page cursor must be a page number of at least 1, and an empty or malformed cursor is refused before any request is sent. A `Link` header whose `next` target does not carry such a page number fails the page that carries it, with an error that quotes the value, instead of handing the bad cursor to the next call.
- Review threads: each thread returns all its comments, read 100 per request. A thread with more than 20 pages of comments is refused with an error rather than returned partly. A thread deleted while its later comment pages are read is left out of the listing. A later comment page that answers with a node which is not a review thread is refused with an error instead, since only an absent node counts as deleted.

## Development

The quality chain is `scripts/check.sh`: tidy, the golangci-lint set, the comment check (`tools/commentcheck`), the dash check, the tests with `-race`, the build with `CGO_ENABLED=0`, and `govulncheck`. CI runs the same script.

Tests run against `httptest` servers only. No test reaches the real GitHub.

## License

See [LICENSE](LICENSE).
