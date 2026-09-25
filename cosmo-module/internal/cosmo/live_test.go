package cosmo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// decodeSessions unmarshals a /live-sessions body (a bare JSON array).
func decodeSessions(t *testing.T, body string) []liveSession {
	t.Helper()
	var sessions []liveSession
	if err := json.Unmarshal([]byte(body), &sessions); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return sessions
}

// TestLiveMemberIDsWirePayload checks a real captured single-member session
// decodes and yields its participant.
func TestLiveMemberIDsWirePayload(t *testing.T) {
	body := `[{"id":1309,"thumbnailImage":"https://static.cosmo.fans/uploads/broadcaster/x.jpg",
	  "startedAt":"2026-07-16T14:54:53.000Z","endedAt":null,
	  "videoCallId":"cosmo-video-jiwoo-rirn837vvf","chatChannelId":"cosmo-chat-jiwoo-58xirroxjo",
	  "slowModeSecond":0,"status":"in_progress","orientation":"portrait",
	  "createdAt":"2026-07-16T14:53:40.404Z","updatedAt":"2026-07-16T14:54:53.104Z",
	  "channels":[{"id":3,"name":"JiWoo","profileImageUrl":"https://static.cosmo.fans/x.jpg",
	    "primaryColorHex":"#FFF800","isConnected":true}],
	  "participantArtistMemberIds":[3],
	  "artistLogoUrl":"https://static.cosmo.fans/assets/triples-logo.png","title":"와!!"}]`

	ids := liveMemberIDs(decodeSessions(t, body))
	if len(ids) != 1 || !ids[3] {
		t.Fatalf("ids = %v, want {3}", ids)
	}
}

// TestLiveMemberIDsStreamTogether checks every participant of a multi-member
// live is marked, not just the broadcasting channel.
func TestLiveMemberIDsStreamTogether(t *testing.T) {
	body := `[{"endedAt":null,"status":"in_progress",
	  "channels":[{"id":3,"name":"JiWoo","isConnected":true}],
	  "participantArtistMemberIds":[3,19,7]}]`

	ids := liveMemberIDs(decodeSessions(t, body))
	for _, want := range []int{3, 19, 7} {
		if !ids[want] {
			t.Errorf("member %d not marked live; ids = %v", want, ids)
		}
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want exactly the 3 participants", ids)
	}
}

// TestLiveMemberIDsSkipsEnded checks a session with an endedAt is dropped while
// a concurrent live one survives.
func TestLiveMemberIDsSkipsEnded(t *testing.T) {
	body := `[{"endedAt":"2026-07-16T15:20:00.000Z","status":"ended",
	   "participantArtistMemberIds":[3]},
	  {"endedAt":null,"status":"in_progress","participantArtistMemberIds":[8]}]`

	ids := liveMemberIDs(decodeSessions(t, body))
	if ids[3] {
		t.Errorf("ended session marked member 3 live")
	}
	if !ids[8] {
		t.Errorf("in-progress session did not mark member 8; ids = %v", ids)
	}
}

// TestLiveMemberIDsChannelFallback checks that a session without a participant
// list falls back to its channels.
func TestLiveMemberIDsChannelFallback(t *testing.T) {
	body := `[{"endedAt":null,"status":"in_progress",
	  "channels":[{"id":3,"name":"JiWoo","isConnected":true},{"id":19,"name":"Xinyu"}]}]`

	ids := liveMemberIDs(decodeSessions(t, body))
	if !ids[3] || !ids[19] || len(ids) != 2 {
		t.Fatalf("ids = %v, want {3,19} from the channels fallback", ids)
	}
}

// decodePost unmarshals one room post.
func decodePost(t *testing.T, body string) Post {
	t.Helper()
	var p Post
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return p
}

// TestOwnerChannelStreamTogether checks a multi-member live is credited to the
// author's channel, not channels[0]. The payload mirrors a real clip whose
// channels[] listed a participant before the owner.
func TestOwnerChannelStreamTogether(t *testing.T) {
	post := decodePost(t, `{"id":2201,"author":{"nickname":"ChaeWon"},
	  "kind":"live-clip","content":"[Behind] Looking for HaYeon to Help with MCO!",
	  "videoItem":{"id":1288,"duration":600,"accessType":"connected",
	    "channels":[{"id":24,"name":"HaYeon","isConnected":false},
	      {"id":26,"name":"ChaeWon","isConnected":true}]},
	  "createdAt":"2026-07-16T06:00:01.116Z"}`)

	ch, ok := ownerChannel(post)
	if !ok || ch.ID != 26 || ch.Name != "ChaeWon" {
		t.Fatalf("ownerChannel = %+v, %v; want ChaeWon (id 26)", ch, ok)
	}
}

// TestOwnerChannelFallback checks an author missing from channels[] falls back
// to the first channel, and an empty channels[] reports no owner.
func TestOwnerChannelFallback(t *testing.T) {
	post := decodePost(t, `{"author":{"nickname":"SeoYeon"},
	  "videoItem":{"id":1,"channels":[{"id":13,"name":"Nien"}]}}`)
	ch, ok := ownerChannel(post)
	if !ok || ch.ID != 13 {
		t.Fatalf("ownerChannel = %+v, %v; want the channels[0] fallback (id 13)", ch, ok)
	}

	post = decodePost(t, `{"author":{"nickname":"SeoYeon"},"videoItem":{"id":1,"channels":[]}}`)
	if ch, ok := ownerChannel(post); ok {
		t.Fatalf("ownerChannel = %+v, want none for empty channels", ch)
	}
}

// TestLiveMemberIDsEmpty checks the common case — nobody streaming — yields an
// empty, non-nil set.
func TestLiveMemberIDsEmpty(t *testing.T) {
	ids := liveMemberIDs(decodeSessions(t, `[]`))
	if ids == nil {
		t.Fatal("ids = nil, want an empty set")
	}
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty", ids)
	}
}

// emptyResponse is the caption endpoint's "none available": a 200 carrying no
// body at all, which the shared JSON decoder reports as io.EOF.
func emptyResponse() *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

// TestCaptionEmptyBody checks the endpoint's zero-length 200 reads as "no
// captions" rather than an io.EOF failure: it is how COSMO says a replay has
// none, and surfacing it as an error would put a bare "EOF" on the status line.
func TestCaptionEmptyBody(t *testing.T) {
	c := testClient(func(r *http.Request) (*http.Response, error) {
		if got, want := r.URL.Path, "/bff/v3/live-clips/1537/caption"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		return emptyResponse(), nil
	})
	caption, err := c.Caption(context.Background(), "1537")
	if err != nil {
		t.Fatalf("Caption: %v", err)
	}
	if caption.Available() {
		t.Errorf("Available() = true, want false (got %+v)", caption)
	}
}

// TestCaptionServed checks a populated response yields the language COSMO
// chose. The language is not requestable, so Lang is the only thing that can
// name the saved file.
func TestCaptionServed(t *testing.T) {
	c := testClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"lang":"ko","captionUrl":"https://resources.cosmo.fans/subtitles/live-clip/1531/ko-1789528583276.vtt"}`), nil
	})
	caption, err := c.Caption(context.Background(), "1531")
	if err != nil {
		t.Fatalf("Caption: %v", err)
	}
	if !caption.Available() {
		t.Fatal("Available() = false, want true")
	}
	if caption.Lang != "ko" {
		t.Errorf("Lang = %q, want %q", caption.Lang, "ko")
	}
	if !strings.HasSuffix(caption.URL, "ko-1789528583276.vtt") {
		t.Errorf("URL = %q, want the served .vtt", caption.URL)
	}
}

// TestClipCaptionGate checks the case the subtitle gate exists for: a replay in
// its public window, playable without a membership (accessType "all", so a
// videoUrl is served), that is captioned and still withholds them because the
// account does not hold the broadcasting member's membership.
func TestClipCaptionGate(t *testing.T) {
	c := testClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{
		  "videoUrl": "https://customer-x.cloudflarestream.com/abc/manifest/video.m3u8",
		  "hasCaption": true,
		  "channel": {"id": 24, "name": "HaYeon", "isConnected": false}
		}`), nil
	})
	clip, err := c.Clip(context.Background(), "1533")
	if err != nil {
		t.Fatalf("Clip: %v", err)
	}
	if clip.VideoURL == "" {
		t.Error("VideoURL is empty; the video itself is not gated here")
	}
	if !clip.HasCaption {
		t.Error("HasCaption = false, want true")
	}
	if clip.Connected {
		t.Error("Connected = true, want false — the subtitles are gated")
	}
}

// TestClipNoMembership checks a null videoUrl still reports ErrNoMembership,
// the gate on the video rather than on its subtitles.
func TestClipNoMembership(t *testing.T) {
	c := testClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"videoUrl": null, "hasCaption": true}`), nil
	})
	if _, err := c.Clip(context.Background(), "1473"); !errors.Is(err, ErrNoMembership) {
		t.Fatalf("Clip err = %v, want ErrNoMembership", err)
	}
}
