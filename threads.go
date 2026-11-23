package github

import (
	"context"
	"fmt"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const reviewThreadsQuery = `query($owner: String!, $name: String!, $number: Int!, $first: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: $first, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id isResolved path line
          comments(first: 50) {
            nodes {
              databaseId body createdAt authorAssociation
              author { __typename login ... on User { databaseId } ... on Bot { databaseId } }
            }
          }
        }
      }
    }
  }
}`

const replyMutation = `mutation($input: AddPullRequestReviewThreadReplyInput!) {
  addPullRequestReviewThreadReply(input: $input) { comment { id } }
}`

type threadsResponse struct {
	Repository *struct {
		PullRequest *struct {
			ReviewThreads struct {
				PageInfo pageInfoWire     `json:"pageInfo"`
				Nodes    []threadNodeWire `json:"nodes"`
			} `json:"reviewThreads"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

type threadNodeWire struct {
	ID         string             `json:"id"`
	IsResolved bool               `json:"isResolved"`
	Path       string             `json:"path"`
	Line       int                `json:"line"`
	Comments   threadCommentsWire `json:"comments"`
}

type threadCommentsWire struct {
	Nodes []commentNodeWire `json:"nodes"`
}

type commentNodeWire struct {
	DatabaseID        int64     `json:"databaseId"`
	Body              string    `json:"body"`
	CreatedAt         time.Time `json:"createdAt"`
	AuthorAssociation string    `json:"authorAssociation"`
	Author            *struct {
		Typename   string `json:"__typename"`
		Login      string `json:"login"`
		DatabaseID int64  `json:"databaseId"`
	} `json:"author"`
}

func (c commentNodeWire) toComment() contracts.ReviewComment {
	var author actorWire
	typename := ""
	if c.Author != nil {
		author = actorWire{Login: c.Author.Login, ID: c.Author.DatabaseID}
		typename = c.Author.Typename
	}
	author.Type = typename
	return contracts.ReviewComment{
		Author:    author.forgeActor(c.AuthorAssociation),
		Body:      c.Body,
		CreatedAt: c.CreatedAt,
	}
}

func (n threadNodeWire) toThread() contracts.ReviewThread {
	comments := make([]contracts.ReviewComment, 0, len(n.Comments.Nodes))
	for _, comment := range n.Comments.Nodes {
		comments = append(comments, comment.toComment())
	}
	return contracts.ReviewThread{
		ID:       n.ID,
		Path:     n.Path,
		Line:     n.Line,
		Resolved: n.IsResolved,
		Comments: comments,
	}
}

func (f *forge) ReviewThreads(ctx context.Context, ref contracts.PullRequestRef, page contracts.PageRequest) (contracts.Page[contracts.ReviewThread], error) {
	if err := f.checkRepo(ref.Repo()); err != nil {
		return contracts.Page[contracts.ReviewThread]{}, err
	}
	limit, err := pageLimit(page)
	if err != nil {
		return contracts.Page[contracts.ReviewThread]{}, err
	}
	variables := map[string]any{
		"owner":  ref.Repo().Namespace(),
		"name":   ref.Repo().Name(),
		"number": ref.Number(),
		"first":  limit,
		"after":  cursorVariable(page.Cursor),
	}
	var response threadsResponse
	if err := f.graphQL(ctx, reviewThreadsQuery, variables, &response); err != nil {
		return contracts.Page[contracts.ReviewThread]{}, err
	}
	if response.Repository == nil || response.Repository.PullRequest == nil {
		return contracts.Page[contracts.ReviewThread]{}, fmt.Errorf("pull request %d of %s: %w", ref.Number(), ref.Repo(), contracts.ErrForgeNotFound)
	}
	listing := response.Repository.PullRequest.ReviewThreads
	items := make([]contracts.ReviewThread, 0, len(listing.Nodes))
	for _, node := range listing.Nodes {
		items = append(items, node.toThread())
	}
	return contracts.Page[contracts.ReviewThread]{Items: items, Next: listing.PageInfo.cursor()}, nil
}

func (f *forge) ReplyToThread(ctx context.Context, thread contracts.ThreadRef, body string) error {
	if err := f.checkRepo(thread.PullRequest().Repo()); err != nil {
		return err
	}
	variables := map[string]any{
		"input": map[string]any{
			"pullRequestReviewThreadId": thread.ID(),
			"body":                      body,
		},
	}
	var ignored struct{}
	return f.graphQL(ctx, replyMutation, variables, &ignored)
}
