package github

import (
	"context"
	"fmt"
	"slices"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const maxCommentPages = 20

const afterVariable = "after"

const reviewThreadsQuery = `query($owner: String!, $name: String!, $number: Int!, $first: Int!, $after: String) {
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      reviewThreads(first: $first, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id isResolved path line originalLine
          comments(first: 100) {
            pageInfo { hasNextPage endCursor }
            nodes {
              body createdAt authorAssociation
              author { __typename login ... on User { databaseId } ... on Bot { databaseId } }
            }
          }
        }
      }
    }
  }
}`

const threadCommentsQuery = `query($threadId: ID!, $after: String) {
  node(id: $threadId) {
    ... on PullRequestReviewThread {
      comments(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          body createdAt authorAssociation
          author { __typename login ... on User { databaseId } ... on Bot { databaseId } }
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
	ID           string             `json:"id"`
	IsResolved   bool               `json:"isResolved"`
	Path         string             `json:"path"`
	Line         *int               `json:"line"`
	OriginalLine *int               `json:"originalLine"`
	Comments     threadCommentsWire `json:"comments"`
}

type threadCommentsWire struct {
	PageInfo pageInfoWire      `json:"pageInfo"`
	Nodes    []commentNodeWire `json:"nodes"`
}

type threadCommentsResponse struct {
	Node *struct {
		Comments *threadCommentsWire `json:"comments"`
	} `json:"node"`
}

type commentNodeWire struct {
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

func (n threadNodeWire) line() int {
	if n.Line != nil {
		return *n.Line
	}
	if n.OriginalLine != nil {
		return *n.OriginalLine
	}
	return 0
}

func toComments(nodes []commentNodeWire) []contracts.ReviewComment {
	comments := make([]contracts.ReviewComment, 0, len(nodes))
	for _, node := range nodes {
		comments = append(comments, node.toComment())
	}
	return comments
}

func (f *forge) allComments(ctx context.Context, threadID string, first threadCommentsWire) ([]commentNodeWire, bool, error) {
	nodes := slices.Clone(first.Nodes)
	page := first.PageInfo
	for pages := 1; page.HasNextPage; pages++ {
		if pages >= maxCommentPages {
			return nil, false, fmt.Errorf("thread %s: %w", threadID, errTooManyCommentPages)
		}
		var response threadCommentsResponse
		variables := map[string]any{"threadId": threadID, afterVariable: page.EndCursor}
		if err := f.graphQL(ctx, threadCommentsQuery, variables, &response); err != nil {
			return nil, false, err
		}
		if response.Node == nil {
			return nil, false, nil
		}
		if response.Node.Comments == nil {
			return nil, false, fmt.Errorf("thread %s: node is not a review thread: %w", threadID, errUnexpectedValue)
		}
		nodes = append(nodes, response.Node.Comments.Nodes...)
		page = response.Node.Comments.PageInfo
	}
	return nodes, true, nil
}

func (f *forge) threadOf(ctx context.Context, node threadNodeWire) (contracts.ReviewThread, bool, error) {
	nodes, found, err := f.allComments(ctx, node.ID, node.Comments)
	if err != nil || !found {
		return contracts.ReviewThread{}, false, err
	}
	return contracts.ReviewThread{
		ID:       node.ID,
		Path:     node.Path,
		Line:     node.line(),
		Resolved: node.IsResolved,
		Comments: toComments(nodes),
	}, true, nil
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
		"owner":       ref.Repo().Namespace(),
		"name":        ref.Repo().Name(),
		"number":      ref.Number(),
		"first":       limit,
		afterVariable: cursorVariable(page.Cursor),
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
		thread, found, err := f.threadOf(ctx, node)
		if err != nil {
			return contracts.Page[contracts.ReviewThread]{}, err
		}
		if found {
			items = append(items, thread)
		}
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

type pageInfoWire struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

func (p pageInfoWire) cursor() contracts.PageCursor {
	if !p.HasNextPage {
		return ""
	}
	return contracts.PageCursor(p.EndCursor)
}
