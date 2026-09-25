package cosmo

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// roomPageSize is the take per request. The endpoint has no server-side cap, so
// one large page returns a group's whole post history; we still page on skip as
// future-proofing (mirrors cosmo-room.py).
const roomPageSize = 500

// RoomPosts fetches every "post"-kind room post for a group, newest first (the
// API's order). Callers filter by member client-side via Author.Nickname, which
// maps exactly to member names.
func (c *Client) RoomPosts(ctx context.Context, group string) ([]Post, error) {
	return c.fetchRoomPosts(ctx, group, "post")
}

// postCommentsResponse is the shape of GET /room-posts/{id}/comments.
type postCommentsResponse struct {
	Comments []Comment `json:"comments"`
}

// RoomPostComments fetches a post's full comment thread, newest first. When
// artistOnly is set the API returns only artist comments and fan comments that
// received an artist reply. The take is large enough to return every comment in
// a single request (the endpoint has no server-side cap).
func (c *Client) RoomPostComments(ctx context.Context, postID string, artistOnly bool) ([]Comment, error) {
	params := url.Values{}
	params.Set("take", "1000")
	params.Set("order", "desc")
	params.Set("skip", "0")
	if artistOnly {
		params.Set("filter", "artist_comment_only")
	}
	var resp postCommentsResponse
	if err := c.getJSON(ctx, "/room-posts/"+postID+"/comments", params, &resp); err != nil {
		return nil, err
	}
	return resp.Comments, nil
}

// RoomPostTranslation fetches the fixed-English auto-translation of a post's
// text (mirrors the app's "See translation"; same no-target-language design as
// Client.Translate for Talk messages).
func (c *Client) RoomPostTranslation(ctx context.Context, postID string) (Translation, error) {
	var resp struct {
		TranslatedContent      string `json:"translatedContent"`
		DetectedSourceLanguage string `json:"detectedSourceLanguage"`
	}
	if err := c.getJSON(ctx, "/room-posts/"+postID+"/translated-contents", nil, &resp); err != nil {
		return Translation{}, err
	}
	return Translation{TranslatedContent: resp.TranslatedContent,
		DetectedSourceLanguage: resp.DetectedSourceLanguage}, nil
}

// fetchRoomPosts pages /bff/v4/room-posts for a given kind, group-wide. The v4
// response matches v3 for kind=post; for kind=live-clip it adds accessType and
// the channels[] participant list VodsByMember resolves the owning channel
// from.
func (c *Client) fetchRoomPosts(ctx context.Context, group, kind string) ([]Post, error) {
	var all []Post
	skip := 0
	for {
		params := url.Values{}
		params.Set("take", strconv.Itoa(roomPageSize))
		params.Set("kind", kind)
		params.Set("skip", strconv.Itoa(skip))
		params.Set("artistId", group)

		var resp roomPostsResponse
		if err := c.getJSONV4(ctx, "/room-posts", params, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Posts...)
		if len(resp.Posts) < roomPageSize {
			break
		}
		skip += roomPageSize
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second): // be gentle between pages
		}
	}
	return all, nil
}
