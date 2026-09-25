package cosmo

import (
	"bufio"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The Talk realtime feed is a long-lived Server-Sent-Events stream. It has no
// id: lines (so no Last-Event-ID resume): after a drop, once the stream has
// reconnected we emit a synthetic "reconnected" event and leave it to the
// caller to backfill via FetchMessages(after=). Emitting only after the new
// connection is live means the backfill covers the whole outage — anything
// arriving from then on comes over the stream (the caller dedupes overlap) —
// and failed reconnect attempts don't fire useless backfills while offline.
// Ported from talk_api.py's _stream/_read_events.

const (
	sseReadTimeout  = 45 * time.Second // two missed ~20s pings => reconnect
	sseBackoffStart = time.Second
	sseBackoffMax   = 30 * time.Second
)

// streamHTTP has no total timeout so a healthy stream stays open; idle death is
// detected by the per-read watchdog instead.
var streamHTTP = &http.Client{Timeout: 0}

// SSEEvent is one decoded event from the feed.
type SSEEvent struct {
	Event string
	Data  string
}

// Stream is a running SSE subscription. Read events from Events(); call Close()
// to stop. Events() is closed when the stream is Closed or its parent context
// is cancelled.
type Stream struct {
	ch     chan SSEEvent
	cancel context.CancelFunc
}

// Events returns the channel of decoded events (plus synthetic "reconnected").
func (s *Stream) Events() <-chan SSEEvent { return s.ch }

// Close stops the stream and releases its connection.
func (s *Stream) Close() { s.cancel() }

// StreamMessages subscribes to realtime events for one member's chat.
func (c *Client) StreamMessages(parent context.Context, memberID int) *Stream {
	return c.stream(parent, url.Values{
		"channel":        {"user-message"},
		"artistMemberId": {strconv.Itoa(memberID)},
	})
}

// StreamChannelList subscribes to realtime events for the whole channel list.
func (c *Client) StreamChannelList(parent context.Context, group string) *Stream {
	return c.stream(parent, url.Values{
		"channel":  {"user-message-list"},
		"artistId": {group},
	})
}

func (c *Client) stream(parent context.Context, params url.Values) *Stream {
	ctx, cancel := context.WithCancel(parent)
	s := &Stream{ch: make(chan SSEEvent, 32), cancel: cancel}
	go c.streamLoop(ctx, params, s.ch)
	return s
}

// streamLoop reconnects for the stream's lifetime, backing off between attempts.
func (c *Client) streamLoop(ctx context.Context, params url.Values, ch chan<- SSEEvent) {
	defer close(ch)
	backoff := sseBackoffStart
	reconnect := false // becomes true once the first connection has been used up
	for {
		if ctx.Err() != nil {
			return
		}
		// readEvents resets backoff to the start on the first live event.
		_ = c.readEvents(ctx, params, ch, &backoff, reconnect)
		reconnect = true
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, sseBackoffMax)
	}
}

// readEvents runs one SSE connection, parsing events until it ends or errors. A
// watchdog cancels the request if no line arrives within sseReadTimeout. When
// reconnect is set, a successful connection emits the synthetic "reconnected"
// event before any stream event, so the caller backfills the outage with the
// new connection already delivering.
func (c *Client) readEvents(ctx context.Context, params url.Values, ch chan<- SSEEvent, backoff *time.Duration, reconnect bool) error {
	token, err := c.EnsureToken(ctx)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, apiBase+"/event-stream?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", UserAgent)

	resp, err := streamHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return &APIError{Status: resp.StatusCode, Body: "event-stream failed"}
	}
	if reconnect {
		select {
		case ch <- SSEEvent{Event: "reconnected", Data: "{}"}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	watchdog := time.AfterFunc(sseReadTimeout, cancel)
	defer watchdog.Stop()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	event := "message"
	var data []string
	for scanner.Scan() {
		watchdog.Reset(sseReadTimeout)
		*backoff = sseBackoffStart // a live line = healthy again

		line := scanner.Text()
		switch {
		case line == "": // blank line terminates an event
			if len(data) > 0 {
				select {
				case ch <- SSEEvent{Event: event, Data: strings.Join(data, "\n")}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			event, data = "message", nil
		case strings.HasPrefix(line, ":"): // comment / keep-alive
			continue
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[len("data:"):], " "))
		}
	}
	return scanner.Err()
}
