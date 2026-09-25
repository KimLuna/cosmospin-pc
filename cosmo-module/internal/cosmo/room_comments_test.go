package cosmo

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const commentsFixture = `{
  "comments": [
    {
      "id": 316709,
      "author": {"userId": null, "nickname": "JiYeon", "profileImage": "x"},
      "content": "artist top-level",
      "createdAt": "2026-07-13T05:58:43.659Z",
      "isArtist": true, "isBlinded": false, "isDeleted": false,
      "replies": []
    },
    {
      "id": 316699,
      "author": {"userId": 49914, "nickname": "fan", "profileImage": ""},
      "content": "nice",
      "createdAt": "2026-07-13T05:56:54.450Z",
      "isArtist": false, "isBlinded": false, "isDeleted": false,
      "replies": [
        {"id": 9248, "content": "thanks", "createdAt": "2026-07-13T05:59:30.400Z",
         "author": {"nickname": "JiYeon", "profileImage": "x"}}
      ]
    }
  ]
}`

// TestRoomPostComments checks the request the client builds and that it decodes
// the thread, including the nested reply. The artist filter param appears only
// when requested.
func TestRoomPostComments(t *testing.T) {
	var gotPath, gotQuery string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Encode()
		return jsonResponse(commentsFixture), nil
	})

	comments, err := c.RoomPostComments(context.Background(), "2215", false)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bff/v3/room-posts/2215/comments" {
		t.Fatalf("path = %q", gotPath)
	}
	for _, want := range []string{"order=desc", "skip=0", "take=1000"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query %q missing %q", gotQuery, want)
		}
	}
	if strings.Contains(gotQuery, "filter=") {
		t.Fatalf("unfiltered request should not set filter: %q", gotQuery)
	}
	if len(comments) != 2 {
		t.Fatalf("got %d comments, want 2", len(comments))
	}
	if !comments[0].IsArtist || comments[0].Author.Nickname != "JiYeon" {
		t.Fatalf("first comment = %+v, want the artist top-level comment", comments[0])
	}
	if len(comments[1].Replies) != 1 || comments[1].Replies[0].Author.Nickname != "JiYeon" {
		t.Fatalf("reply not decoded: %+v", comments[1])
	}

	// The artist-only filter adds the filter parameter.
	if _, err := c.RoomPostComments(context.Background(), "2215", true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "filter=artist_comment_only") {
		t.Fatalf("artist-only request should set filter: %q", gotQuery)
	}
}

// TestRoomPostTranslation checks the translated-contents request (no query) and
// that the translated text and detected source language decode.
func TestRoomPostTranslation(t *testing.T) {
	var gotPath, gotQuery string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		return jsonResponse(`{"translatedContent":"hi","detectedSourceLanguage":"KO"}`), nil
	})

	tr, err := c.RoomPostTranslation(context.Background(), "2194")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bff/v3/room-posts/2194/translated-contents" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "" {
		t.Fatalf("translated-contents takes no params, got query %q", gotQuery)
	}
	if tr.TranslatedContent != "hi" || tr.DetectedSourceLanguage != "KO" {
		t.Fatalf("translation = %+v", tr)
	}
}
