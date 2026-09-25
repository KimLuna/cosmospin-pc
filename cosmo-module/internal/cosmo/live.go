package cosmo

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strconv"
)

// ErrNoMembership means the member's live clip is gated behind a membership the
// account doesn't hold (the API returns a null videoUrl).
var ErrNoMembership = errors.New("no membership for this member")

// AllMembersID is the VodsByMember bucket holding the group's whole replay
// history, each clip once; no member has APIID 0.
const AllMembersID = 0

// liveSessionPageSize is the take= for /live-sessions.
const liveSessionPageSize = 30

// Vod is one live-stream replay in a member's history.
type Vod struct {
	VideoID      string
	Duration     int // seconds
	Title        string
	Member       string // the streaming member (the post's author)
	Date         string // YYYY-MM-DD in the system's local timezone
	Playable     bool   // false when the clip is gated behind a membership we lack
	ThumbnailURL string // public CDN, viewable even without membership
}

// VodsByMember fetches a group's whole replay history in one request and
// buckets it per member APIID. A multi-member live is credited only to the
// member whose channel broadcast it, so each replay appears in exactly one
// member's list. Bucket AllMembersID holds every clip once. Lists are
// newest-first, matching the wire order.
func (c *Client) VodsByMember(ctx context.Context, group string) (map[int][]Vod, error) {
	posts, err := c.fetchRoomPosts(ctx, group, "live-clip")
	if err != nil {
		return nil, err
	}
	byMember := make(map[int][]Vod)
	for _, p := range posts {
		vi := p.VideoItem
		if vi == nil {
			continue
		}
		playable := vi.AccessType == "all"
		for _, ch := range vi.Channels {
			playable = playable || ch.IsConnected
		}
		vod := Vod{
			VideoID:      string(vi.ID),
			Duration:     vi.Duration,
			Title:        p.Content,
			Member:       p.Author.Nickname,
			Date:         LocalDate(p.CreatedAt),
			Playable:     playable,
			ThumbnailURL: vi.ThumbnailURL,
		}
		byMember[AllMembersID] = append(byMember[AllMembersID], vod)
		if ch, ok := ownerChannel(p); ok {
			byMember[ch.ID] = append(byMember[ch.ID], vod)
		}
	}
	return byMember, nil
}

// ownerChannel finds the channel that broadcast a live clip. channels[] lists
// every participant of a multi-member live in no reliable order, but the post
// author is always the owning channel (the v3 live-clips detail exposes it as
// a singular channel field, which always matches the author). The v4 author
// carries no id, so the name match recovers the member APIID. Falls back to
// the first channel should the author ever be absent from the list.
func ownerChannel(p Post) (ClipChannel, bool) {
	channels := p.VideoItem.Channels
	for _, ch := range channels {
		if ch.Name == p.Author.Nickname {
			return ch, true
		}
	}
	if len(channels) > 0 {
		return channels[0], true
	}
	return ClipChannel{}, false
}

// liveSession is one element of GET /live-sessions. A session is broadcast from
// a single account but can credit several members ("stream together"), listed
// in participantArtistMemberIds.
type liveSession struct {
	EndedAt                    string        `json:"endedAt"`
	Status                     string        `json:"status"` // "in_progress"
	ParticipantArtistMemberIDs []int         `json:"participantArtistMemberIds"`
	Channels                   []ClipChannel `json:"channels"`
}

// LiveMemberIDs reports which of a group's members are streaming right now, as
// a set of artistMemberIds (== members.Member.APIID). One page suffices: a
// group is never 30 sessions live at once, and an undercount only costs a
// marker.
func (c *Client) LiveMemberIDs(ctx context.Context, group string) (map[int]bool, error) {
	params := url.Values{}
	params.Set("skip", "0")
	params.Set("take", strconv.Itoa(liveSessionPageSize))
	params.Set("artistId", group)

	var sessions []liveSession
	if err := c.getJSONV4(ctx, "/live-sessions", params, &sessions); err != nil {
		return nil, err
	}
	return liveMemberIDs(sessions), nil
}

// liveMemberIDs is the pure half of LiveMemberIDs. It keeps only sessions that
// haven't ended: the endpoint appears to return just the active ones, but an
// endedAt check can't wrongly hide a live session, whereas matching status
// against "in_progress" would hide every one if that vocabulary ever grows.
func liveMemberIDs(sessions []liveSession) map[int]bool {
	ids := make(map[int]bool)
	for _, s := range sessions {
		if s.EndedAt != "" {
			continue
		}
		participants := s.ParticipantArtistMemberIDs
		if len(participants) == 0 {
			// Fall back to the broadcasting channels, so a session still marks
			// its members if the participant list is ever absent.
			for _, ch := range s.Channels {
				ids[ch.ID] = true
			}
			continue
		}
		for _, id := range participants {
			ids[id] = true
		}
	}
	return ids
}

// liveClipResponse is the shape of GET /live-clips/{id}. The singular channel
// is the broadcasting member, and always matches the post author ownerChannel
// recovers from the listing.
type liveClipResponse struct {
	VideoURL   string      `json:"videoUrl"`
	HasCaption bool        `json:"hasCaption"`
	Channel    ClipChannel `json:"channel"`
}

// Clip is a resolved replay: the stream URL, plus what is known about its
// subtitles. The two subtitle fields are separate because they fail
// differently. HasCaption false means COSMO generated none for this live,
// while Connected false means it did and this account may not have them.
type Clip struct {
	VideoURL   string
	HasCaption bool // drives the CC button in the app
	Connected  bool // account holds the broadcasting member's membership
}

// Clip resolves a replay's playable/downloadable stream URL and subtitle
// availability, returning ErrNoMembership when the video itself is gated.
// Stays on /bff/v3: v4 has no live-clips detail endpoint.
func (c *Client) Clip(ctx context.Context, videoID string) (Clip, error) {
	var resp liveClipResponse
	if err := c.getJSON(ctx, "/live-clips/"+videoID, nil, &resp); err != nil {
		return Clip{}, err
	}
	if resp.VideoURL == "" {
		return Clip{}, ErrNoMembership
	}
	return Clip{
		VideoURL:   resp.VideoURL,
		HasCaption: resp.HasCaption,
		Connected:  resp.Channel.IsConnected,
	}, nil
}

// Caption is a replay's subtitle track: the language COSMO served and the CDN
// URL it lives at. The zero value means the replay has none available.
type Caption struct {
	Lang string `json:"lang"`
	URL  string `json:"captionUrl"`
}

// Available reports whether a caption track was actually offered.
func (c Caption) Available() bool { return c.URL != "" }

// Caption resolves a replay's AI auto-translated subtitles, answering with the
// zero Caption when there are none to be had.
//
// Which language comes back is not ours to choose: COSMO serves whichever one
// the account last selected in the app's subtitle menu, which is a server-side
// preference distinct from the account locale, and ignores any lang
// parameter. Callers name their file from Caption.Lang for that reason.
//
// Two ways to get nothing, and the endpoint reports both the same way. Either
// the live was never captioned, or it was and the subtitles need the
// broadcasting member's membership, which gates them INDEPENDENTLY of the
// video: a replay inside its 24h public window downloads fine and still
// withholds them. Clip.HasCaption and Clip.Connected tell the two apart.
func (c *Client) Caption(ctx context.Context, videoID string) (Caption, error) {
	var resp Caption
	err := c.getJSON(ctx, "/live-clips/"+videoID+"/caption", nil, &resp)
	// "None available" is a 200 with a zero-length body, which the shared JSON
	// decoder reports as io.EOF. That is an answer, not a failure, and it is
	// handled here rather than in decodeResponse, where treating an empty body
	// as success would hand every other endpoint a silent zero value.
	if errors.Is(err, io.EOF) {
		return Caption{}, nil
	}
	if err != nil {
		return Caption{}, err
	}
	return resp, nil
}
