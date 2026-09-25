package cosmo

import (
	"encoding/json"
	"testing"
)

// Sticker messages are type "sticker" with empty content and no mediaUrl; the
// image URL is complete on the message (no originals upgrade, unlike media).
const stickerJSON = `{"id":"20912","type":"sticker","senderType":"artistMember","senderId":25,
"senderName":"쇼니♡","content":"","reply":null,"createdAt":"2026-07-20T12:20:36.015Z",
"sticker":{"id":98,"codepointHex":"\\uE261","imageUrl":"https://resources.cosmo.fans/images/stickers/136.gif"}}`

func TestStickerMessage(t *testing.T) {
	var w wireMessage
	if err := json.Unmarshal([]byte(stickerJSON), &w); err != nil {
		t.Fatal(err)
	}
	m := w.normalize()

	want := "https://resources.cosmo.fans/images/stickers/136.gif"
	if m.StickerURL != want {
		t.Errorf("StickerURL = %q, want %q", m.StickerURL, want)
	}
	if !m.IsSticker() {
		t.Error("IsSticker() = false, want true")
	}
	// Stickers must not read as media: that would send the page looking for an
	// originalUrl the media endpoint will never have for them.
	if m.IsMedia() {
		t.Error("IsMedia() = true, want false for a sticker")
	}
	if got := m.Attachment(); got != want {
		t.Errorf("Attachment() = %q, want %q", got, want)
	}
}

// Voice messages are type "audio" with null content. Unlike photos they carry
// no thumbnail: mediaUrl already is the original, so there is nothing for the
// media endpoint to upgrade. Their metadata is a duration in milliseconds.
const audioJSON = `{"id":"26646","type":"audio","senderType":"artistMember","senderId":11,
"senderName":"琴音♡","content":null,"reply":null,"createdAt":"2026-07-27T07:03:52.419Z","sticker":null,
"mediaUrl":"https://resources.cosmo.fans/artist-member-messages/11/1e67a00a.mp4",
"mediaMetadata":{"originalUrl":"https://resources.cosmo.fans/artist-member-messages/11/1e67a00a.mp4","duration":2115}}`

func TestAudioMessage(t *testing.T) {
	var w wireMessage
	if err := json.Unmarshal([]byte(audioJSON), &w); err != nil {
		t.Fatal(err)
	}
	m := w.normalize()

	if m.Type != "audio" {
		t.Errorf("Type = %q, want %q", m.Type, "audio")
	}
	if !m.IsMedia() {
		t.Error("IsMedia() = false, want true for a voice message")
	}
	if m.DurationMs != 2115 {
		t.Errorf("DurationMs = %d, want 2115", m.DurationMs)
	}
	want := "https://resources.cosmo.fans/artist-member-messages/11/1e67a00a.mp4"
	if got := m.Attachment(); got != want {
		t.Errorf("Attachment() = %q, want %q", got, want)
	}
}

// Photos carry an aspect ratio but no duration, so the duration suffix must
// stay off them even though they share the media path with voice messages.
func TestImageMessageHasNoDuration(t *testing.T) {
	const imageJSON = `{"id":"25135","type":"image","senderType":"artistMember","senderId":11,
"content":null,"createdAt":"2026-07-25T12:43:22.834Z",
"mediaUrl":"https://x/thumb.jpeg",
"mediaMetadata":{"originalUrl":"https://x/orig.jpeg","thumbnailUrl":"https://x/thumb.jpeg","aspectRatio":0.749}}`
	var w wireMessage
	if err := json.Unmarshal([]byte(imageJSON), &w); err != nil {
		t.Fatal(err)
	}
	m := w.normalize()

	if m.DurationMs != 0 {
		t.Errorf("DurationMs = %d, want 0 for a photo", m.DurationMs)
	}
	// The photo still upgrades to its original, which audio has no need of.
	if m.MediaURL != "https://x/orig.jpeg" {
		t.Errorf("MediaURL = %q, want the originalUrl", m.MediaURL)
	}
}

// A quoted fan reply can be a sticker with no text at all. Only the fact is
// kept — the quoted sticker's URL is deliberately not carried, since the
// open/save keys act on the artist's own attachment, not the quote's.
func TestReplyStickerWithoutText(t *testing.T) {
	const replyJSON = `{"id":"26791","type":"text","senderType":"artistMember","senderId":11,
"content":"너무 귀여워…","createdAt":"2026-07-27T09:14:00.000Z",
"reply":{"id":"793478","senderId":0,"isMe":false,"content":"",
"sticker":{"id":114,"codepointHex":"\\uE271","imageUrl":"https://resources.cosmo.fans/images/stickers/1b3422cf.png"}}}`
	var w wireMessage
	if err := json.Unmarshal([]byte(replyJSON), &w); err != nil {
		t.Fatal(err)
	}
	m := w.normalize()

	if m.Reply == nil {
		t.Fatal("Reply = nil, want the quoted message")
	}
	if !m.Reply.HasSticker {
		t.Error("Reply.HasSticker = false, want true")
	}
	if m.Reply.Content != "" {
		t.Errorf("Reply.Content = %q, want empty", m.Reply.Content)
	}
	// The quote's sticker must not leak into the message's own attachment, or
	// the open/save keys would act on a file the artist never sent.
	if m.IsSticker() || m.IsMedia() {
		t.Error("a text reply to a sticker must not itself read as an attachment")
	}
	if got := m.Attachment(); got != "" {
		t.Errorf("Attachment() = %q, want empty", got)
	}
}

// A quote with text keeps it, sticker or not.
func TestReplyWithoutStickerHasFlagUnset(t *testing.T) {
	const replyJSON = `{"id":"1","type":"text","senderType":"artistMember","senderId":11,
"content":"hi","createdAt":"2026-07-27T09:14:00.000Z",
"reply":{"id":"2","senderId":0,"content":"안녕"}}`
	var w wireMessage
	if err := json.Unmarshal([]byte(replyJSON), &w); err != nil {
		t.Fatal(err)
	}
	m := w.normalize()
	if m.Reply.HasSticker {
		t.Error("Reply.HasSticker = true, want false when the quote has no sticker")
	}
	if m.Reply.Content != "안녕" {
		t.Errorf("Reply.Content = %q, want 안녕", m.Reply.Content)
	}
}

// A channel's lastMessage carries the newest message's type, and null content
// when that message is an attachment.
func TestChannelLastMessageType(t *testing.T) {
	const channelsJSON = `{"channels":[
{"artistMemberId":25,"originalName":"ShiOn","lastMessage":{"id":"20912","content":"","type":"sticker","createdAt":"2026-07-20T12:20:36.015Z"}},
{"artistMemberId":21,"originalName":"Mayu","lastMessage":{"id":"20515","content":null,"type":"image","mediaType":"image","createdAt":"2026-07-19T19:40:50.993Z"}},
{"artistMemberId":8,"originalName":"YuBin","lastMessage":null}]}`

	var resp struct {
		Channels []wireChannel `json:"channels"`
	}
	if err := json.Unmarshal([]byte(channelsJSON), &resp); err != nil {
		t.Fatal(err)
	}

	want := []struct{ name, content, msgType string }{
		{"ShiOn", "", "sticker"},
		{"Mayu", "", "image"},
		// No messages at all: the type must stay empty so the sidebar can tell
		// this apart from a channel whose newest message is an attachment.
		{"YuBin", "", ""},
	}
	for i, w := range want {
		got := resp.Channels[i].normalize()
		if got.OriginalName != w.name || got.LastMessage != w.content || got.LastMessageType != w.msgType {
			t.Errorf("channel %d = %q/%q/%q, want %q/%q/%q", i,
				got.OriginalName, got.LastMessage, got.LastMessageType, w.name, w.content, w.msgType)
		}
	}
}

func TestAttachment(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{"text", Message{Type: "text", Content: "hi"}, ""},
		{"media", Message{Type: "image", MediaURL: "https://x/photo.jpg"}, "https://x/photo.jpg"},
		{"sticker", Message{Type: "sticker", StickerURL: "https://x/1.gif"}, "https://x/1.gif"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.msg.Attachment(); got != tt.want {
				t.Errorf("Attachment() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A live allowance for a channel with a window already running. Captured from
// /bff/v3/channels/messages/23/reply-count on 2026-07-30.
const replyCountJSON = `{"remainingCount":5,"maxCount":5,"canReply":true,
"nextResetAt":"2026-08-02T09:01:19.188Z","textMaxLength":200}`

func TestReplyLimits(t *testing.T) {
	var w wireReplyLimits
	if err := json.Unmarshal([]byte(replyCountJSON), &w); err != nil {
		t.Fatal(err)
	}
	got := w.normalize()

	want := ReplyLimits{
		RemainingCount: 5,
		MaxCount:       5,
		CanReply:       true,
		NextResetAt:    "2026-08-02T09:01:19.188Z",
		TextMaxLength:  200,
	}
	if got != want {
		t.Errorf("normalize() = %+v, want %+v", got, want)
	}
}

// nextResetAt is null on a channel with no window running — nothing has been
// sent recently enough to start one. It must decode to "" rather than fail the
// whole allowance, since the count beside it is still the real one.
func TestReplyLimitsNullReset(t *testing.T) {
	const j = `{"remainingCount":5,"maxCount":5,"canReply":true,"nextResetAt":null,"textMaxLength":200}`
	var w wireReplyLimits
	if err := json.Unmarshal([]byte(j), &w); err != nil {
		t.Fatal(err)
	}
	got := w.normalize()
	if got.NextResetAt != "" {
		t.Errorf("NextResetAt = %q, want empty", got.NextResetAt)
	}
	if !got.CanReply || got.RemainingCount != 5 {
		t.Errorf("allowance lost alongside the null reset: %+v", got)
	}
}

// An exhausted channel: the count is spent and the window names when it comes
// back. canReply is the field to gate on, so it must survive decoding as false.
func TestReplyLimitsExhausted(t *testing.T) {
	const j = `{"remainingCount":0,"maxCount":5,"canReply":false,
"nextResetAt":"2026-08-02T09:01:19.188Z","textMaxLength":200}`
	var w wireReplyLimits
	if err := json.Unmarshal([]byte(j), &w); err != nil {
		t.Fatal(err)
	}
	got := w.normalize()
	if got.CanReply {
		t.Error("CanReply = true, want false")
	}
	if got.RemainingCount != 0 || got.NextResetAt == "" {
		t.Errorf("got %+v, want 0 remaining with a reset time", got)
	}
}

// A live welcome message. Captured from
// /bff/v3/channels/messages/8/welcome-message on 2026-07-30. Note the negative
// id, and the senderProfileImageUrl/mentionIndexList fields the message
// endpoints don't carry.
const welcomeJSON = `{"id":"-8","type":"text","senderType":"artistMember","senderId":8,
"senderName":"YuBin","senderProfileImageUrl":"https://static.cosmo.fans/uploads/member-profile/5b4.jpg",
"content":"안녕하세요 공유빈입니다 우리 서로 잘 알아가 보아요","isDeleted":false,
"createdAt":"2026-07-01T09:22:29.668Z","mentionIndexList":null}`

func TestWelcomeMessageDecodes(t *testing.T) {
	var w struct {
		wireMessage
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal([]byte(welcomeJSON), &w); err != nil {
		t.Fatal(err)
	}
	if w.IsDeleted {
		t.Fatal("IsDeleted = true, want false")
	}
	m := w.wireMessage.normalize()
	m.IsWelcome = true

	if m.ID != "-8" {
		t.Errorf("ID = %q, want -8 (the synthetic negative id)", m.ID)
	}
	if !m.IsArtist() {
		t.Error("IsArtist() = false; the welcome message is the artist's")
	}
	if !m.IsWelcome {
		t.Error("IsWelcome = false")
	}
	// It has to translate like any other artist message, which needs content and
	// a type the translate path doesn't skip.
	if m.Content == "" || m.Type != "text" {
		t.Errorf("content/type = %q/%q, want translatable text", m.Content, m.Type)
	}
	// No cursor is served for it: it sits outside the paged history entirely.
	if m.Cursor != "" {
		t.Errorf("Cursor = %q, want empty", m.Cursor)
	}
	if m.CreatedMs() == 0 {
		t.Error("CreatedMs() = 0; it must sort above real history")
	}
}

// A deleted welcome message is not shown, so the decode has to surface the flag
// rather than quietly returning a message the caller would render.
func TestWelcomeMessageDeletedFlag(t *testing.T) {
	const j = `{"id":"-8","type":"text","senderType":"artistMember","senderId":8,
"content":"gone","isDeleted":true,"createdAt":"2026-07-01T09:22:29.668Z"}`
	var w struct {
		wireMessage
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal([]byte(j), &w); err != nil {
		t.Fatal(err)
	}
	if !w.IsDeleted {
		t.Fatal("IsDeleted = false, want true")
	}
}

// IsWelcome is ours, not the wire's: nothing in a normal message payload should
// ever set it, or real messages would start being treated as the greeting.
func TestNormalMessageIsNotWelcome(t *testing.T) {
	var w wireMessage
	if err := json.Unmarshal([]byte(stickerJSON), &w); err != nil {
		t.Fatal(err)
	}
	if w.normalize().IsWelcome {
		t.Error("IsWelcome = true for an ordinary message")
	}
}

// Media-list items are not messages: they carry mediaType instead of type, no
// senderType or content at all, and their URLs live only in the metadata. This
// is a real page (member 21, newest first) with a video and a photo.
const mediaListJSON = `{"afterCursor":"27262","beforeCursor":"29136","hasMoreAfter":true,"items":[
{"id":"29136","cursor":"29136","createdAt":"2026-07-28T23:28:58.337Z","mediaType":"video",
 "mediaMetadata":{"aspectRatio":0.562,"duration":2791,
  "originalUrl":"https://resources.cosmo.fans/artist-member-messages/21/579eb.mp4",
  "thumbnailUrl":"https://resources.cosmo.fans/artist-member-messages/21/af0b3.jpg"}},
{"id":"27262","cursor":"27262","createdAt":"2026-07-27T12:10:39.658Z","mediaType":"image",
 "mediaMetadata":{"aspectRatio":0.75,
  "originalUrl":"https://resources.cosmo.fans/artist-member-messages/21/62090.jpeg",
  "thumbnailUrl":"https://resources.cosmo.fans/artist-member-messages/21/ffe7a.jpeg"}}]}`

func TestMediaItemNormalize(t *testing.T) {
	var resp struct {
		Items []wireMediaItem `json:"items"`
	}
	if err := json.Unmarshal([]byte(mediaListJSON), &resp); err != nil {
		t.Fatal(err)
	}
	// The page arrives newest-first and is turned around, so index 0 is the
	// oldest item — the order the messages endpoint serves and the pane expects.
	page := normalizeMedia(resp.Items)
	if len(page) != 2 || page[0].ID != "27262" || page[1].ID != "29136" {
		t.Fatalf("page order = %v, want oldest first", page)
	}
	video := resp.Items[0].normalize()
	if video.Type != "video" {
		t.Errorf("Type = %q, want video (from mediaType)", video.Type)
	}
	// The original, not the thumbnail: serving thumbnails is what the messages
	// endpoint already does.
	if want := "https://resources.cosmo.fans/artist-member-messages/21/579eb.mp4"; video.MediaURL != want {
		t.Errorf("MediaURL = %q, want the original %q", video.MediaURL, want)
	}
	if video.DurationMs != 2791 {
		t.Errorf("DurationMs = %d, want 2791", video.DurationMs)
	}
	if !video.IsMedia() || !video.IsArtist() {
		t.Error("a media item should read as the artist's media")
	}
	if video.Cursor != "29136" || video.CreatedAt == "" {
		t.Errorf("cursor/time = %q/%q, want both carried over", video.Cursor, video.CreatedAt)
	}
	// Photos carry no duration; only an aspect ratio, which is nothing to show.
	if got := resp.Items[1].normalize(); got.DurationMs != 0 || got.Type != "image" {
		t.Errorf("photo = %q/%dms, want image with no duration", got.Type, got.DurationMs)
	}
}
