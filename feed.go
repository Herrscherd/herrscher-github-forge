package github

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const (
	mediaTypeEventStream = "text/event-stream"
	subscriptionsElement = "event-subscriptions"
	eventsElement        = "events"
	feedEndEvent         = "github.stream.end"
	feedEndRevoked       = "access_revoked"
	feedPreviewMessage   = "Event subscriptions are not enabled for this account"
	feedHeartbeat        = 9 * time.Second
	feedMissedBeats      = 3
	feedLineLimit        = 64 << 10
	feedEventLimit       = 256 << 10
	feedScanBuffer       = 4 << 10
)

type subscriptionRequest struct {
	ResourceURL string   `json:"resource_url"`
	EventTypes  []string `json:"event_types"`
}

type subscriptionWire struct {
	ID string `json:"id"`
}

type feedEndWire struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

type feedEventWire struct {
	Type string `json:"type"`
}

type feedFrame struct {
	kind contracts.PullRequestEventKind
	beat bool
	err  error
}

func feedEventTypes() []string {
	return []string{
		"com.github.pull_request.review.submitted.v1",
		"com.github.pull_request.head.changed.v1",
		"com.github.pull_request.state.changed.v1",
		"com.github.pull_request.merge_conflict.v1",
		"com.github.pull_request.check_suite.status.changed.v1",
	}
}

func feedKindOf(eventType string) (contracts.PullRequestEventKind, bool) {
	switch eventType {
	case "com.github.pull_request.review.submitted.v1":
		return contracts.PullRequestReviewSubmitted, true
	case "com.github.pull_request.head.changed.v1":
		return contracts.PullRequestNewCommit, true
	case "com.github.pull_request.state.changed.v1":
		return contracts.PullRequestStateChanged, true
	case "com.github.pull_request.merge_conflict.v1":
		return contracts.PullRequestMergeConflict, true
	case "com.github.pull_request.check_suite.status.changed.v1":
		return contracts.PullRequestChecksChanged, true
	}
	return 0, false
}

var _ contracts.PullRequestFeed = (*forge)(nil)

func (f *forge) PullRequestEvents(ctx context.Context, ref contracts.PullRequestRef) (<-chan contracts.PullRequestSignal, error) {
	if err := f.checkRepo(ref.Repo()); err != nil {
		return nil, err
	}
	if f.host.String() != publicHost {
		return nil, fmt.Errorf("%s has no pull request event feed: %w", f.host, contracts.ErrFeedRefused)
	}
	subscription, err := f.subscribe(ctx, ref)
	if err != nil {
		return nil, err
	}
	streamCtx, stopStream := context.WithCancel(ctx)
	body, err := f.openFeed(streamCtx, subscription)
	if err != nil {
		stopStream()
		return nil, err
	}
	out := make(chan contracts.PullRequestSignal)
	go f.pump(streamCtx, stopStream, body, out)
	return out, nil
}

func (f *forge) subscribe(ctx context.Context, ref contracts.PullRequestRef) (string, error) {
	payload, err := encodeJSON(subscriptionRequest{ResourceURL: pullResourceURL(ref), EventTypes: feedEventTypes()})
	if err != nil {
		return "", err
	}
	spec := request{method: http.MethodPost, target: f.restURL(nil, subscriptionsElement), body: payload}
	var sub subscriptionWire
	decode := func(_ contracts.ForgeToken, resp *http.Response) error {
		return decodeJSON(resp.Body, &sub)
	}
	if err := f.exchangeChecked(ctx, spec, f.feedCheck(ctx), decode); err != nil {
		return "", err
	}
	if sub.ID == "" {
		return "", errNoSubscription
	}
	return sub.ID, nil
}

func (f *forge) openFeed(ctx context.Context, subscription string) (io.ReadCloser, error) {
	if err := f.quota.refusal(f.now()); err != nil {
		return nil, err
	}
	if err := f.acquire(ctx); err != nil {
		return nil, err
	}
	spec := request{method: http.MethodGet, target: f.restURL(nil, subscriptionsElement, subscription, eventsElement), accept: mediaTypeEventStream}
	resp, _, err := f.send(ctx, spec)
	f.release()
	if err != nil {
		return nil, err
	}
	f.quota.observe(resp.Header)
	if err := f.feedCheck(ctx)(resp); err != nil {
		return nil, errors.Join(err, resp.Body.Close())
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), mediaTypeEventStream) {
		return nil, errors.Join(errNotEventStream, resp.Body.Close())
	}
	return resp.Body, nil
}

func (f *forge) feedCheck(ctx context.Context) statusVerifier {
	return func(resp *http.Response) error {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			if previewRefused(resp.Body) {
				return fmt.Errorf("forge answered %d: %w", resp.StatusCode, contracts.ErrFeedRefused)
			}
		}
		return f.statusError(ctx, resp, nil)
	}
}

func previewRefused(body io.Reader) bool {
	data, err := readBounded(body)
	if err != nil {
		return false
	}
	var refusal struct {
		Message string `json:"message"`
	}
	return json.Unmarshal(data, &refusal) == nil && refusal.Message == feedPreviewMessage
}

func pullResourceURL(ref contracts.PullRequestRef) string {
	repo := ref.Repo()
	return fmt.Sprintf("https://%s/%s/%s/pull/%d", publicHost, repo.Namespace(), repo.Name(), ref.Number())
}

func (f *forge) pump(ctx context.Context, stopStream context.CancelFunc, body io.Reader, out chan<- contracts.PullRequestSignal) {
	frames := make(chan feedFrame)
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		readFeed(body, frames, stop)
	}()
	defer func() {
		close(stop)
		stopStream()
		<-readerDone
		close(out)
	}()
	f.fanOut(ctx, frames, out)
}

func (f *forge) fanOut(ctx context.Context, frames <-chan feedFrame, out chan<- contracts.PullRequestSignal) {
	var queue feedQueue
	silence := time.NewTimer(f.feedSilence)
	defer silence.Stop()
	incoming := frames
	for {
		if ctx.Err() != nil {
			return
		}
		next, pending := queue.peek()
		var sendOut chan<- contracts.PullRequestSignal
		if pending {
			sendOut = out
		}
		select {
		case <-ctx.Done():
			return
		case sendOut <- next:
			if queue.pop() {
				return
			}
		case frame := <-incoming:
			silence.Reset(f.feedSilence)
			switch {
			case frame.err != nil:
				queue.fail(frame.err)
				incoming = nil
				silence.Stop()
			case frame.beat:
			default:
				queue.add(frame.kind)
			}
		case <-silence.C:
			queue.fail(ErrFeedSilent)
			incoming = nil
		}
	}
}

type feedQueue struct {
	kinds  []contracts.PullRequestEventKind
	err    error
	failed bool
}

func (q *feedQueue) add(kind contracts.PullRequestEventKind) {
	if !slices.Contains(q.kinds, kind) {
		q.kinds = append(q.kinds, kind)
	}
}

func (q *feedQueue) fail(err error) {
	q.err = err
	q.failed = true
}

func (q *feedQueue) peek() (contracts.PullRequestSignal, bool) {
	if len(q.kinds) > 0 {
		return contracts.PullRequestSignal{Kind: q.kinds[0]}, true
	}
	if q.failed {
		return contracts.PullRequestSignal{Err: q.err}, true
	}
	return contracts.PullRequestSignal{}, false
}

func (q *feedQueue) pop() bool {
	if len(q.kinds) > 0 {
		q.kinds = q.kinds[1:]
		return false
	}
	return true
}

type feedParser struct {
	name    string
	data    []byte
	hasData bool
}

func (p *feedParser) line(line string) (feedFrame, bool) {
	switch {
	case line == "":
		return p.dispatch()
	case strings.HasPrefix(line, ":"):
		return feedFrame{beat: true}, true
	case strings.HasPrefix(line, "event:"):
		p.name = fieldValue(line, "event:")
	case strings.HasPrefix(line, "data:"):
		return p.appendData(fieldValue(line, "data:"))
	}
	return feedFrame{}, false
}

func (p *feedParser) dispatch() (feedFrame, bool) {
	if p.name == "" && !p.hasData {
		return feedFrame{}, false
	}
	frame := dispatchFeed(p.name, p.data)
	p.name, p.data, p.hasData = "", nil, false
	return frame, true
}

func (p *feedParser) appendData(value string) (feedFrame, bool) {
	if p.hasData {
		p.data = append(p.data, '\n')
	}
	p.data = append(p.data, value...)
	p.hasData = true
	if len(p.data) > feedEventLimit {
		return feedFrame{err: errFeedEventTooLarge}, true
	}
	return feedFrame{}, false
}

func readFeed(body io.Reader, frames chan<- feedFrame, stop <-chan struct{}) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, feedScanBuffer), feedLineLimit)
	var parser feedParser
	for scanner.Scan() {
		frame, ready := parser.line(scanner.Text())
		if !ready {
			continue
		}
		if !emitFeed(frames, stop, frame) || frame.err != nil {
			return
		}
	}
	emitFeed(frames, stop, streamEndFrame(scanner.Err()))
}

func emitFeed(frames chan<- feedFrame, stop <-chan struct{}, frame feedFrame) bool {
	select {
	case frames <- frame:
		return true
	case <-stop:
		return false
	}
}

func streamEndFrame(err error) feedFrame {
	switch {
	case errors.Is(err, bufio.ErrTooLong):
		return feedFrame{err: errFeedEventTooLarge}
	case err != nil:
		return feedFrame{err: fmt.Errorf("read pull request event feed: %w", err)}
	}
	return feedFrame{err: ErrFeedEnded}
}

func fieldValue(line, field string) string {
	return strings.TrimPrefix(strings.TrimPrefix(line, field), " ")
}

func dispatchFeed(name string, data []byte) feedFrame {
	if name == feedEndEvent {
		return feedEndFrame(data)
	}
	if len(data) == 0 {
		return feedFrame{beat: true}
	}
	var event feedEventWire
	if err := json.Unmarshal(data, &event); err != nil {
		return feedFrame{err: fmt.Errorf("decode pull request event: %w", err)}
	}
	if kind, known := feedKindOf(event.Type); known {
		return feedFrame{kind: kind}
	}
	return feedFrame{beat: true}
}

func feedEndFrame(data []byte) feedFrame {
	var end feedEndWire
	if err := json.Unmarshal(data, &end); err != nil {
		return feedFrame{err: fmt.Errorf("decode event stream end: %w", err)}
	}
	if end.Reason == feedEndRevoked {
		return feedFrame{err: fmt.Errorf("event stream ended (%s): %w", end.Reason, contracts.ErrFeedRefused)}
	}
	return feedFrame{err: fmt.Errorf("event stream ended (%s, %s): %w", end.Outcome, end.Reason, ErrFeedEnded)}
}
