package cosmo

import (
	"encoding/json"
	"testing"
)

// decodeNotifications unmarshals a /notification-center body.
func decodeNotifications(t *testing.T, body string) []Notification {
	t.Helper()
	var resp notificationsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp.Notifications
}

// TestNotificationsWirePayload checks a real captured feed decodes: the
// {"notifications":[...]} envelope is unique among the news endpoints (/notices
// wraps in "result", /artist-schedules in "items"), and id arrives as a JSON
// string despite being numeric.
func TestNotificationsWirePayload(t *testing.T) {
	body := `{"notifications":[
	  {"id":"417455527","category":"Room","title":"New Story: Kaede 💌",
	   "content":"너의 스타일은 뭐야","url":"cosmo://tripleS/room/post-detail?postId=2226",
	   "isRead":false,"sentAt":"2026-07-16T07:20:12.434Z"},
	  {"id":"410381147","category":"Etc","title":"New Announcement from tripleS",
	   "content":"2026-07-14 COSMO 2.42.0 Update Note","url":"cosmo://tripleS/notice?id=262",
	   "isRead":false,"sentAt":"2026-07-14T07:02:36.941Z"},
	  {"id":"377615702","category":"Shop","title":"Hyper-Ego Aura ver. DCO OPEN ✧˖°",
	   "content":"Curious about ARTMS’ new look in Hyper-Ego? Check it out now on COSMO ✧˖°",
	   "url":"cosmo://artms/shop","isRead":false,"sentAt":"2026-07-09T07:10:12.093Z"}]}`

	ns := decodeNotifications(t, body)
	if len(ns) != 3 {
		t.Fatalf("len = %d, want 3", len(ns))
	}

	want := Notification{
		ID:       "417455527",
		Category: "Room",
		Title:    "New Story: Kaede 💌",
		Content:  "너의 스타일은 뭐야",
		SentAt:   "2026-07-16T07:20:12.434Z",
	}
	if ns[0] != want {
		t.Errorf("ns[0] = %+v, want %+v", ns[0], want)
	}

	// Category is passed through verbatim rather than mapped to an enum, so an
	// uncommon one like Shop has to survive the round trip.
	if ns[1].Category != "Etc" || ns[2].Category != "Shop" {
		t.Errorf("categories = %q, %q; want Etc, Shop", ns[1].Category, ns[2].Category)
	}
}

// TestNotificationsEmptyFeed checks an unknown artistId's response decodes to no
// entries: the endpoint answers 200 with an empty list rather than erroring.
func TestNotificationsEmptyFeed(t *testing.T) {
	if ns := decodeNotifications(t, `{"notifications":[]}`); len(ns) != 0 {
		t.Fatalf("len = %d, want 0", len(ns))
	}
}
