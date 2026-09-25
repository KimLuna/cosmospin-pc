package cosmo

import "time"

// Shared response types for the room/live endpoints (the talk endpoints add
// their own in talk.go). Field sets mirror the JSON the app receives; only the
// fields cosmo-tui uses are decoded.

// LocalDate renders the YYYY-MM-DD of an ISO-8601 timestamp in the system's
// local timezone (the API sends UTC or +09:00 instants). Falls back to the
// raw date prefix if the string doesn't parse.
func LocalDate(iso string) string {
	if t, err := time.Parse(time.RFC3339, iso); err == nil {
		return t.Local().Format("2006-01-02")
	}
	if len(iso) >= 10 {
		return iso[:10]
	}
	return iso
}

// LocalDateTime renders "YYYY-MM-DD HH:MM" of an ISO-8601 instant in the
// system's local timezone. Falls back to the raw string if it doesn't parse.
func LocalDateTime(iso string) string {
	if t, err := time.Parse(time.RFC3339, iso); err == nil {
		return t.Local().Format("2006-01-02 15:04")
	}
	return iso
}

// Author is the member who wrote a room post. nickname maps to a member name.
type Author struct {
	ID       int    `json:"id"`
	Nickname string `json:"nickname"`
}

// Media is one attachment on a room post.
type Media struct {
	URL  string `json:"url"`
	Kind string `json:"kind"` // "image" | "video"
}

// ClipChannel is one member participating in a live clip (v4 lists every
// member of a multi-member live). IsConnected reports whether this account
// holds that member's membership.
type ClipChannel struct {
	ID          int    `json:"id"` // == the member's artistMemberId
	Name        string `json:"name"`
	IsConnected bool   `json:"isConnected"`
}

// VideoItem is the video payload on a live-clip post (replay list). The id
// arrives as a JSON number, so it uses flexString (see talk.go).
type VideoItem struct {
	ID           flexString    `json:"id"`
	Duration     int           `json:"duration"`   // seconds
	AccessType   string        `json:"accessType"` // "connected" (membership-gated) | "all"
	ThumbnailURL string        `json:"thumbnailUrl"`
	Channels     []ClipChannel `json:"channels"`
}

// Comment is a top-level comment on a room post. IsArtist marks a comment
// written by a group member (vs. a fan).
type Comment struct {
	ID        flexString     `json:"id"`
	Author    Author         `json:"author"`
	Content   string         `json:"content"`
	CreatedAt string         `json:"createdAt"`
	IsArtist  bool           `json:"isArtist"`
	IsDeleted bool           `json:"isDeleted"`
	IsBlinded bool           `json:"isBlinded"`
	Replies   []CommentReply `json:"replies"`
}

// CommentReply is a nested reply under a Comment. (The API omits isArtist here.)
type CommentReply struct {
	ID        flexString `json:"id"`
	Author    Author     `json:"author"`
	Content   string     `json:"content"`
	CreatedAt string     `json:"createdAt"`
}

// Post is one room post. For kind=post it carries Media; for kind=live-clip it
// carries VideoItem instead. The id arrives as a JSON number (flexString).
type Post struct {
	ID               flexString `json:"id"`
	Content          string     `json:"content"`
	CreatedAt        string     `json:"createdAt"`
	Author           Author     `json:"author"`
	Media            []Media    `json:"media"`
	MediaAspectRatio string     `json:"mediaAspectRatio"`
	Comments         []Comment  `json:"comments"`
	VideoItem        *VideoItem `json:"videoItem"`
}

// roomPostsResponse is the shape of GET /room-posts.
type roomPostsResponse struct {
	Posts []Post `json:"posts"`
}
