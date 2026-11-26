package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	headerAuthorization = "Authorization"
	headerAccept        = "Accept"
	headerAPIVersion    = "X-Github-Api-Version"
	headerContentType   = "Content-Type"
	headerETag          = "ETag"
	headerIfNoneMatch   = "If-None-Match"
	headerLink          = "Link"
	headerOAuthScopes   = "X-Oauth-Scopes"

	apiVersion      = "2022-11-28"
	mediaTypeJSON   = "application/vnd.github+json"
	contentTypeJSON = "application/json"

	maxResponseBytes = 8 << 20
	maxInFlight      = 4
	maxRedirects     = 10
)

type request struct {
	method      string
	target      *url.URL
	body        []byte
	ifNoneMatch string
	accept      string
	accepted    []int
	graphQL     bool
}

type responder func(token contracts.ForgeToken, resp *http.Response) error

type statusVerifier func(resp *http.Response) error

func (f *forge) exchange(ctx context.Context, spec request, handle responder) error {
	check := func(resp *http.Response) error {
		return f.statusError(ctx, resp, spec.accepted)
	}
	return f.exchangeChecked(ctx, spec, check, handle)
}

func (f *forge) exchangeChecked(ctx context.Context, spec request, check statusVerifier, handle responder) error {
	if !spec.graphQL {
		if err := f.quota.refusal(f.now()); err != nil {
			return err
		}
	}
	if err := f.acquire(ctx); err != nil {
		return err
	}
	defer f.release()
	resp, token, err := f.send(ctx, spec)
	if err != nil {
		return err
	}
	return f.finish(resp, check, token, handle)
}

func (f *forge) send(ctx context.Context, spec request) (*http.Response, contracts.ForgeToken, error) {
	token, err := f.token(ctx)
	if err != nil {
		return nil, contracts.ForgeToken{}, err
	}
	req, err := newRequest(ctx, spec, token)
	if err != nil {
		return nil, token, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, token, fmt.Errorf("forge request: %w", err)
	}
	return resp, token, nil
}

func (f *forge) finish(resp *http.Response, check statusVerifier, token contracts.ForgeToken, handle responder) error {
	f.quota.observe(resp.Header)
	err := check(resp)
	if err == nil {
		err = handle(token, resp)
	}
	return errors.Join(err, resp.Body.Close())
}

func (f *forge) token(ctx context.Context) (contracts.ForgeToken, error) {
	token, err := f.creds.Token(ctx, f.host)
	if err != nil {
		return contracts.ForgeToken{}, fmt.Errorf("forge token of %s: %w", f.host, err)
	}
	if !token.Valid() {
		return contracts.ForgeToken{}, fmt.Errorf("forge token of %s: %w", f.host, contracts.ErrForgeNotConnected)
	}
	return token, nil
}

func (f *forge) acquire(ctx context.Context) error {
	select {
	case f.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for a forge request slot: %w", ctx.Err())
	}
}

func (f *forge) release() {
	<-f.slots
}

func newRequest(ctx context.Context, spec request, token contracts.ForgeToken) (*http.Request, error) {
	var body io.Reader
	if spec.body != nil {
		body = bytes.NewReader(spec.body)
	}
	req, err := http.NewRequestWithContext(ctx, spec.method, spec.target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("build forge request: %w", err)
	}
	req.Header.Set(headerAuthorization, "Bearer "+token.Secret())
	accept := mediaTypeJSON
	if spec.accept != "" {
		accept = spec.accept
	}
	req.Header.Set(headerAccept, accept)
	req.Header.Set(headerAPIVersion, apiVersion)
	if spec.body != nil {
		req.Header.Set(headerContentType, contentTypeJSON)
	}
	if spec.ifNoneMatch != "" {
		req.Header.Set(headerIfNoneMatch, spec.ifNoneMatch)
	}
	return req, nil
}

func (f *forge) statusError(ctx context.Context, resp *http.Response, accepted []int) error {
	code := resp.StatusCode
	switch {
	case code == http.StatusUnauthorized:
		f.creds.Invalidate(ctx, f.host)
		return fmt.Errorf("forge answered %d: %w", code, contracts.ErrForgeUnauthorized)
	case code == http.StatusForbidden || code == http.StatusTooManyRequests:
		return f.refusedError(resp)
	case code == http.StatusNotFound:
		return fmt.Errorf("forge answered %d: %w", code, contracts.ErrForgeNotFound)
	case isSuccess(code) || code == http.StatusNotModified || slices.Contains(accepted, code):
		return nil
	}
	return fmt.Errorf("forge answered %d: %w", code, errUnexpectedStatus)
}

func (f *forge) refusedError(resp *http.Response) error {
	if reset, limited := throttleReset(resp.Header, f.now()); limited || resp.StatusCode == http.StatusTooManyRequests {
		return &contracts.ForgeRateLimitedError{Reset: reset}
	}
	return fmt.Errorf("forge answered %d: %w", resp.StatusCode, contracts.ErrForgeForbidden)
}

func isSuccess(code int) bool {
	return code >= http.StatusOK && code < http.StatusMultipleChoices
}

func dropAuthorizationAcrossOrigins(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errTooManyRedirects
	}
	origin := via[0].URL
	if req.URL.Scheme != origin.Scheme || req.URL.Host != origin.Host {
		req.Header.Del(headerAuthorization)
	}
	return nil
}

func readBounded(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read forge response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, errResponseTooLarge
	}
	return data, nil
}

func decodeJSON(body io.Reader, target any) error {
	data, err := readBounded(body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode forge response: %w", err)
	}
	return nil
}

func encodeJSON(payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode forge request: %w", err)
	}
	return data, nil
}

func (f *forge) getJSON(ctx context.Context, target *url.URL, out any) error {
	spec := request{method: http.MethodGet, target: target}
	return f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		return decodeJSON(resp.Body, out)
	})
}

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphQLResponse struct {
	Data   json.RawMessage      `json:"data"`
	Errors []graphQLFailureWire `json:"errors"`
}

type graphQLFailureWire struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (f *forge) graphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	payload, err := encodeJSON(graphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return err
	}
	spec := request{method: http.MethodPost, target: f.api.graphql, body: payload, graphQL: true}
	return f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		var envelope graphQLResponse
		if err := decodeJSON(resp.Body, &envelope); err != nil {
			return err
		}
		if len(envelope.Errors) > 0 {
			return &graphQLError{kind: envelope.Errors[0].Type, message: envelope.Errors[0].Message}
		}
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode GraphQL data: %w", err)
		}
		return nil
	})
}
