package cosmo

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Talk is Cosmo's per-member messaging: fans read the short messages an artist
// posts to their channel, translate them, and reply. These endpoints live
// under the same /bff/v3 host and use the same Bearer token as everything else
// (ported from cosmo-util talk_api.py).

// flexString unmarshals a JSON string OR number into a Go string, so id/cursor
// fields decode regardless of how the API types them. null becomes "".
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	s := string(bytes.Trim(b, `"`))
	if s == "null" {
		s = ""
	}
	*f = flexString(s)
	return nil
}

// Reply is the fan message an artist message quotes.
type Reply struct {
	ID       string
	SenderID int
	Content  string
	// HasSticker reports whether the quoted message carried a sticker. Fans can
	// send one with no text at all, which would otherwise quote as a bare arrow.
	// Only the fact is kept, not the URL: the quoted sticker is not what the
	// open/save keys act on, and holding its URL here would only blur that.
	HasSticker bool
}

// Message is one Talk message. SenderType is "artistMember" or "user" (you).
type Message struct {
	ID         string
	Type       string
	SenderType string
	SenderID   int
	Content    string
	CreatedAt  string
	SenderName string
	Reply      *Reply
	Cursor     string
	MediaURL   string
	// DurationMs is how long an audio/video attachment runs, in milliseconds;
	// 0 for stills and for feeds that omit the metadata. Photos carry no
	// comparable field — their metadata is only an aspect ratio.
	DurationMs int
	// StickerURL is the image for a sticker message (type "sticker"). Stickers
	// are shared app assets, not per-member media: the URL arrives complete on
	// the message and has no original to upgrade to.
	StickerURL string
	// IsWelcome marks the channel's welcome message (see WelcomeMessage). It is
	// not a wire field — nothing in the payload distinguishes one, short of the
	// negative id — so only that endpoint sets it, and it is how a caller tells
	// the channel's fixed greeting apart from the artist's actual posts. It does
	// not restrict what can be done with the message: it translates and accepts
	// replies exactly as a real one does.
	IsWelcome bool
}

// IsArtist reports whether the message came from the artist.
func (m Message) IsArtist() bool { return m.SenderType == "artistMember" }

// IsMedia reports whether the message carries an image, video, or voice
// message.
func (m Message) IsMedia() bool {
	return m.MediaURL != "" || m.Type == "image" || m.Type == "video" || m.Type == "audio"
}

// IsSticker reports whether the message is a sticker.
func (m Message) IsSticker() bool { return m.StickerURL != "" }

// Attachment is the file the message carries — its media or its sticker image —
// or "" when it carries neither. This is what opening and downloading act on.
func (m Message) Attachment() string {
	if m.IsMedia() {
		return m.MediaURL
	}
	return m.StickerURL
}

// CreatedMs is CreatedAt as an epoch-millisecond int (the cursor unit the API
// uses); 0 if unparseable.
func (m Message) CreatedMs() int64 {
	t, err := time.Parse(time.RFC3339, m.CreatedAt)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// wireMessage is the on-the-wire message shape; normalize() maps it to Message.
type wireMessage struct {
	ID             flexString `json:"id"`
	Type           string     `json:"type"`
	SenderType     string     `json:"senderType"`
	SenderID       int        `json:"senderId"`
	ArtistMemberID int        `json:"artistMemberId"`
	Content        string     `json:"content"`
	CreatedAt      string     `json:"createdAt"`
	SenderName     string     `json:"senderName"`
	Cursor         flexString `json:"cursor"`
	MediaURL       string     `json:"mediaUrl"`
	MediaMetadata  *struct {
		OriginalURL  string `json:"originalUrl"`
		ThumbnailURL string `json:"thumbnailUrl"`
		Duration     int    `json:"duration"`
	} `json:"mediaMetadata"`
	Sticker *struct {
		ImageURL string `json:"imageUrl"`
	} `json:"sticker"`
	Reply *struct {
		ID       flexString `json:"id"`
		SenderID int        `json:"senderId"`
		Content  string     `json:"content"`
		Sticker  *struct {
			ImageURL string `json:"imageUrl"`
		} `json:"sticker"`
	} `json:"reply"`
}

func (w wireMessage) normalize() Message {
	// Prefer the full-resolution originalUrl; history/SSE otherwise carry only
	// the thumbnail mediaUrl (upgraded later via MediaOriginals).
	mediaURL := w.MediaURL
	durationMs := 0
	if w.MediaMetadata != nil {
		if w.MediaMetadata.OriginalURL != "" {
			mediaURL = w.MediaMetadata.OriginalURL
		}
		durationMs = w.MediaMetadata.Duration
	}
	m := Message{
		ID:         string(w.ID),
		Type:       orDefault(w.Type, "text"),
		SenderType: w.SenderType,
		SenderID:   w.SenderID,
		Content:    w.Content,
		CreatedAt:  w.CreatedAt,
		SenderName: w.SenderName,
		Cursor:     string(w.Cursor),
		MediaURL:   mediaURL,
		DurationMs: durationMs,
	}
	if w.Sticker != nil {
		m.StickerURL = w.Sticker.ImageURL
	}
	if w.SenderID == 0 && w.ArtistMemberID != 0 {
		m.SenderID = w.ArtistMemberID
	}
	if w.Reply != nil {
		m.Reply = &Reply{
			ID:         string(w.Reply.ID),
			SenderID:   w.Reply.SenderID,
			Content:    w.Reply.Content,
			HasSticker: w.Reply.Sticker != nil,
		}
	}
	return m
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// MessageFromEvent builds a Message from an SSE user-message.* payload. These
// compact payloads use artistMemberId and omit senderType, so the caller infers
// the sender from the event name and passes isArtist. Returns false if the
// payload has no id.
func MessageFromEvent(data string, isArtist bool) (Message, bool) {
	var w wireMessage
	if err := json.Unmarshal([]byte(data), &w); err != nil || string(w.ID) == "" {
		return Message{}, false
	}
	m := w.normalize()
	if isArtist {
		m.SenderType = "artistMember"
	} else {
		m.SenderType = "user"
	}
	return m, true
}

// Channel is a member's Talk channel as it appears in the list. MemberName is
// the artist-set nickname when one is set; OriginalName is always the member's
// real name (empty on API versions that predate nicknames).
type Channel struct {
	MemberID        int
	MemberName      string
	OriginalName    string
	ProfileImageURL string
	IsConnected     bool
	UnreadCount     int
	LastMessage     string
	LastMessageAt   string
	// LastMessageType is the newest message's Type. Attachment messages carry
	// no content, so it is the only thing that tells a channel whose newest
	// message is a photo apart from one with no messages at all.
	LastMessageType string
}

type wireChannel struct {
	ArtistMemberID   int    `json:"artistMemberId"`
	ArtistMemberName string `json:"artistMemberName"`
	OriginalName     string `json:"originalName"`
	ProfileImageURL  string `json:"profileImageUrl"`
	IsConnected      bool   `json:"isConnected"`
	UnreadCount      int    `json:"unreadCount"`
	LastMessage      *struct {
		Content   string `json:"content"`
		Type      string `json:"type"`
		CreatedAt string `json:"createdAt"`
	} `json:"lastMessage"`
}

func (w wireChannel) normalize() Channel {
	c := Channel{
		MemberID:        w.ArtistMemberID,
		MemberName:      w.ArtistMemberName,
		OriginalName:    w.OriginalName,
		ProfileImageURL: w.ProfileImageURL,
		IsConnected:     w.IsConnected,
		UnreadCount:     w.UnreadCount,
	}
	if w.LastMessage != nil {
		c.LastMessage = w.LastMessage.Content
		c.LastMessageAt = w.LastMessage.CreatedAt
		c.LastMessageType = orDefault(w.LastMessage.Type, "text")
	}
	return c
}

// Translation is the auto-translation of one message.
type Translation struct {
	MessageID              string
	TranslatedContent      string
	DetectedSourceLanguage string
	ReplyTranslatedContent string
}

// CodeInvalidArtist is the API error code the channels endpoints return for
// artists that have no Talk feature (currently everyone but tripleS).
const CodeInvalidArtist = "CHANNEL_MESSAGE_INVALID_ARTIST"

// ListChannels lists the group's member channels with last message + unread.
func (c *Client) ListChannels(ctx context.Context, group string) ([]Channel, error) {
	var resp struct {
		Channels []wireChannel `json:"channels"`
		Items    []wireChannel `json:"items"`
	}
	params := url.Values{"artistId": {group}}
	if err := c.getJSON(ctx, "/channels/messages", params, &resp); err != nil {
		return nil, err
	}
	wires := resp.Channels
	if wires == nil {
		wires = resp.Items
	}
	out := make([]Channel, len(wires))
	for i, w := range wires {
		out[i] = w.normalize()
	}
	return out, nil
}

// FetchMessages fetches a page of a member's messages. before/after are
// epoch-ms cursors (see Message.CreatedMs); 0 means unset. Pass before to page
// into older history.
func (c *Client) FetchMessages(ctx context.Context, memberID int, take int, before, after int64) ([]Message, error) {
	params := url.Values{}
	params.Set("take", strconv.Itoa(take))
	params.Set("includeCursor", "true")
	if before > 0 {
		params.Set("beforeCursor", strconv.FormatInt(before, 10))
	}
	if after > 0 {
		params.Set("afterCursor", strconv.FormatInt(after, 10))
	}
	var resp struct {
		Messages []wireMessage `json:"messages"`
	}
	if err := c.getJSON(ctx, "/channels/messages/"+strconv.Itoa(memberID), params, &resp); err != nil {
		return nil, err
	}
	out := make([]Message, len(resp.Messages))
	for i, w := range resp.Messages {
		out[i] = w.normalize()
	}
	return out, nil
}

// WelcomeMessage fetches a channel's welcome message: the artist's fixed
// greeting that sits above all real history. It is served only here — the
// messages endpoint never returns it, and paging history to its very end still
// stops short of it — so it is the only way to see the start of a channel.
//
// Its id is a negative synthetic one ("-8", "-21"), which is what keeps it from
// colliding with real message ids. Translation accepts that id like any other,
// so the returned Message needs no special handling to be translated.
//
// Reports false when the channel has no welcome message to show, which covers
// both an empty payload and one the artist has since deleted.
func (c *Client) WelcomeMessage(ctx context.Context, memberID int) (Message, bool, error) {
	var w struct {
		wireMessage
		IsDeleted bool `json:"isDeleted"`
	}
	path := "/channels/messages/" + strconv.Itoa(memberID) + "/welcome-message"
	if err := c.getJSON(ctx, path, nil, &w); err != nil {
		return Message{}, false, err
	}
	if string(w.ID) == "" || w.IsDeleted {
		return Message{}, false, nil
	}
	msg := w.wireMessage.normalize()
	msg.IsWelcome = true
	return msg, true, nil
}

// wireMediaItem is one item of the media list. Its shape is not a message's:
// there is no senderType, content or reply, the type is named mediaType, and
// the metadata carries the only URLs — so it normalizes to a Message on its
// own rather than sharing wireMessage.
type wireMediaItem struct {
	ID            flexString `json:"id"`
	Cursor        flexString `json:"cursor"`
	CreatedAt     string     `json:"createdAt"`
	MediaType     string     `json:"mediaType"`
	MediaMetadata *struct {
		OriginalURL string `json:"originalUrl"`
		Duration    int    `json:"duration"`
	} `json:"mediaMetadata"`
}

func (w wireMediaItem) normalize() Message {
	m := Message{
		ID:        string(w.ID),
		Type:      orDefault(w.MediaType, "image"),
		CreatedAt: w.CreatedAt,
		Cursor:    string(w.Cursor),
		// The endpoint lists the artist's media and only theirs: fans can send
		// text and stickers, neither of which lands here. Nothing on the item
		// says so, so the sender is filled in rather than read.
		SenderType: "artistMember",
	}
	if w.MediaMetadata != nil {
		// Only the original is kept. The item also carries a thumbnailUrl, which
		// is what the messages feed already serves — the point of this endpoint
		// is the full-resolution file.
		m.MediaURL = w.MediaMetadata.OriginalURL
		m.DurationMs = w.MediaMetadata.Duration
	}
	return m
}

// MediaMessages fetches a page of a member's media: every photo, video and
// voice message they have posted, with the text messages between them left
// out. The items carry no content or sender of their own (see wireMediaItem),
// so they are messages only in the sense that they share ids with the ones the
// messages endpoint serves.
//
// after ("" for the newest page) pages into older media. Unlike the messages
// endpoint, whose cursors are epoch-ms timestamps, this endpoint's cursors are
// message ids: afterCursor returns media at-or-older than that id, and any
// message id works (a non-media id returns the media strictly older than it).
// It is inclusive — the page opens on the item whose id was passed — so a walk
// re-reads one item per page and ends when a page carries nothing but that
// repeat. hasMoreAfter cannot end it: the flag is pinned true even on the last
// page.
//
// take is capped at 30 server-side (as it is on the messages endpoint) and
// larger values are silently truncated rather than rejected, so reaching
// further back means another call with a cursor, not a bigger take.
//
// The page is returned oldest-first, the order FetchMessages uses, rather than
// the newest-first order it arrives in.
func (c *Client) MediaMessages(ctx context.Context, memberID, take int, after string) ([]Message, error) {
	params := url.Values{"take": {strconv.Itoa(take)}, "includeCursor": {"true"}}
	if after != "" {
		params.Set("afterCursor", after)
	}
	var resp struct {
		Items []wireMediaItem `json:"items"`
	}
	if err := c.getJSON(ctx, "/channels/messages/"+strconv.Itoa(memberID)+"/media", params, &resp); err != nil {
		return nil, err
	}
	return normalizeMedia(resp.Items), nil
}

// normalizeMedia turns a media page into messages, reversing the endpoint's
// newest-first order into the oldest-first one the messages endpoint serves.
func normalizeMedia(items []wireMediaItem) []Message {
	out := make([]Message, len(items))
	for i, it := range items {
		out[len(items)-1-i] = it.normalize()
	}
	return out
}

// MediaOriginals maps message id -> full-resolution originalUrl for a member's
// media. The history/SSE feeds only carry thumbnails; this is the only source
// of the original (mirrors talk_api.media_originals). take and after are as
// MediaMessages describes them.
func (c *Client) MediaOriginals(ctx context.Context, memberID, take int, after string) (map[string]string, error) {
	items, err := c.MediaMessages(ctx, memberID, take, after)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, it := range items {
		if it.MediaURL != "" {
			out[it.ID] = it.MediaURL
		}
	}
	return out, nil
}

// Translate auto-translates one message (mirrors the app's "See translation").
func (c *Client) Translate(ctx context.Context, memberID int, messageID string) (Translation, error) {
	var resp struct {
		MessageID              flexString `json:"messageId"`
		TranslatedContent      string     `json:"translatedContent"`
		DetectedSourceLanguage string     `json:"detectedSourceLanguage"`
		UserReply              *struct {
			TranslatedContent string `json:"translatedContent"`
		} `json:"userReply"`
	}
	path := "/channels/messages/" + strconv.Itoa(memberID) + "/" + messageID + "/translated-contents"
	if err := c.getJSON(ctx, path, nil, &resp); err != nil {
		return Translation{}, err
	}
	t := Translation{
		MessageID:              string(resp.MessageID),
		TranslatedContent:      resp.TranslatedContent,
		DetectedSourceLanguage: resp.DetectedSourceLanguage,
	}
	if resp.UserReply != nil {
		t.ReplyTranslatedContent = resp.UserReply.TranslatedContent
	}
	return t, nil
}

// ReplyLimits is what a member's channel allows a fan right now: how many
// replies are left, when the allowance resets, and how long one reply may be.
//
// The allowance is MaxCount replies per 144-hour window, and it also refills
// whenever the artist posts — so RemainingCount can be back at MaxCount while
// NextResetAt still names the running window's expiry. The two fields are
// independent; neither can be derived from the other.
type ReplyLimits struct {
	RemainingCount int
	MaxCount       int
	// CanReply is the server's own verdict on whether a reply would be accepted.
	// Prefer it over comparing RemainingCount to zero: it is what the channel
	// gates on, and it costs nothing to be told rather than to infer.
	CanReply bool
	// NextResetAt is when the running window expires (RFC3339), or "" when no
	// window is open — nothing has been sent recently enough to start one.
	NextResetAt string
	// TextMaxLength is the longest reply the channel accepts. It is served
	// rather than fixed at 200 so a policy change needs no client release.
	TextMaxLength int
}

// wireReplyLimits is the on-the-wire allowance; normalize() maps it to
// ReplyLimits. nextResetAt is null until a window opens, which decodes to "".
type wireReplyLimits struct {
	RemainingCount int    `json:"remainingCount"`
	MaxCount       int    `json:"maxCount"`
	CanReply       bool   `json:"canReply"`
	NextResetAt    string `json:"nextResetAt"`
	TextMaxLength  int    `json:"textMaxLength"`
}

func (w wireReplyLimits) normalize() ReplyLimits {
	return ReplyLimits{
		RemainingCount: w.RemainingCount,
		MaxCount:       w.MaxCount,
		CanReply:       w.CanReply,
		NextResetAt:    w.NextResetAt,
		TextMaxLength:  w.TextMaxLength,
	}
}

// ReplyCount fetches a member channel's current reply allowance. Members with
// no Talk channel answer with CodeInvalidArtist, the same as the channel
// endpoints.
func (c *Client) ReplyCount(ctx context.Context, memberID int) (ReplyLimits, error) {
	var w wireReplyLimits
	path := "/channels/messages/" + strconv.Itoa(memberID) + "/reply-count"
	if err := c.getJSON(ctx, path, nil, &w); err != nil {
		return ReplyLimits{}, err
	}
	return w.normalize(), nil
}

// SendReply replies to a specific artist message. Fans reply to a message
// rather than composing freely; sending is rate-limited server-side (see
// ReplyCount, which reports the allowance before it is spent).
func (c *Client) SendReply(ctx context.Context, memberID int, messageID, content string) (Message, error) {
	var w wireMessage
	path := "/channels/messages/" + strconv.Itoa(memberID) + "/" + messageID + "/replies"
	if err := c.sendJSON(ctx, http.MethodPost, path, map[string]string{"content": content}, &w); err != nil {
		return Message{}, err
	}
	return w.normalize(), nil
}

// MarkRead marks a member's channel read up to messageID.
func (c *Client) MarkRead(ctx context.Context, memberID int, messageID string) error {
	path := "/channels/messages/" + strconv.Itoa(memberID) + "/last-read"
	return c.sendJSON(ctx, http.MethodPut, path, map[string]string{"messageId": messageID}, nil)
}
