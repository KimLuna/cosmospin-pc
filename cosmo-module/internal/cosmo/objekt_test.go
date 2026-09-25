package cosmo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/config"
)

// roundTripFunc lets a test stand in for the client's HTTP transport.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// testClient returns a Client whose access token is valid (so EnsureToken makes
// no network call) and whose transport is served by rt.
func testClient(rt roundTripFunc) *Client {
	c := New(config.Credentials{AccessToken: makeJWT(time.Now().Add(7 * 24 * time.Hour).Unix())}, nil)
	c.hc = &http.Client{Transport: rt}
	return c
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// one collection group mirroring the real /objekt-summaries shape, with two
// owned copies (one locked, used for a grid).
const objektFixture = `{
  "collectionCount": 1,
  "favoritedObjektCount": 1,
  "collections": [
    {
      "collection": {
        "season": "Binary02", "collectionNo": "101Z", "class": "First",
        "member": "ChaeWon", "artistName": "tripleS", "comoAmount": 1,
        "transferableByDefault": true, "gridableByDefault": true,
        "accentColor": "#75FB4C", "frontImage": "https://img/front",
        "backImage": "https://img/back", "favoritedAt": "2026-03-15T05:13:59.000Z"
      },
      "count": 2,
      "objekts": [
        {
          "metadata": {"objektNo": 4194, "transferable": false, "tokenId": 21208645},
          "inventory": {"status": "minted", "acquiredAt": "2026-03-08T07:12:20.000Z",
            "mintedAt": "2026-03-08T07:12:13.217Z", "usedForGrid": true,
            "owner": "0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A"}
        },
        {
          "metadata": {"objektNo": 8001, "transferable": true, "tokenId": 21999999},
          "inventory": {"status": "minted", "acquiredAt": "2026-04-01T00:00:00.000Z",
            "mintedAt": "2026-04-01T00:00:00.000Z", "usedForGrid": false,
            "owner": "0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A"}
        }
      ]
    }
  ]
}`

// TestToCollectionsMapping verifies the wire response maps into the exported
// types with serials, flags, and the pinned marker intact.
func TestToCollectionsMapping(t *testing.T) {
	var wire objektSummariesResponse
	if err := json.Unmarshal([]byte(objektFixture), &wire); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	got := wire.toCollections()

	if got.CollectionCount != 1 || got.FavoritedCount != 1 {
		t.Fatalf("counts = %d/%d, want 1/1", got.CollectionCount, got.FavoritedCount)
	}
	if len(got.Collections) != 1 {
		t.Fatalf("collections = %d, want 1", len(got.Collections))
	}
	oc := got.Collections[0]
	c := oc.Collection
	if c.CollectionNo != "101Z" || c.Member != "ChaeWon" || c.Class != "First" || c.Season != "Binary02" {
		t.Fatalf("collection identity wrong: %+v", c)
	}
	if c.AccentColor != "#75FB4C" || c.ComoAmount != 1 || !c.Transferable || !c.Gridable {
		t.Fatalf("collection metadata wrong: %+v", c)
	}
	if !c.Favorited() {
		t.Fatalf("expected collection to be favorited (favoritedAt set)")
	}
	if oc.Count != 2 || len(oc.Objekts) != 2 {
		t.Fatalf("count/objekts = %d/%d, want 2/2", oc.Count, len(oc.Objekts))
	}
	first := oc.Objekts[0]
	if first.ObjektNo != 4194 || first.TokenID != 21208645 {
		t.Fatalf("serial/token = %d/%d", first.ObjektNo, first.TokenID)
	}
	if first.Transferable || !first.UsedForGrid {
		t.Fatalf("first copy should be locked + used for grid: %+v", first)
	}
	if oc.Objekts[1].ObjektNo != 8001 || !oc.Objekts[1].Transferable {
		t.Fatalf("second copy wrong: %+v", oc.Objekts[1])
	}
}

// TestToCollectionsUnfavorited checks a null favoritedAt yields Favorited()==false.
func TestToCollectionsUnfavorited(t *testing.T) {
	body := `{"collectionCount":1,"favoritedObjektCount":0,"collections":[
      {"collection":{"collectionNo":"201Z","favoritedAt":null},"count":1,"objekts":[]}]}`
	var wire objektSummariesResponse
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := wire.toCollections()
	if got.Collections[0].Collection.Favorited() {
		t.Fatalf("null favoritedAt should not be favorited")
	}
}

// TestObjektCollectionsPageParams checks the request carries the expected query.
func TestObjektCollectionsPageParams(t *testing.T) {
	var gotURL string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		return jsonResponse(objektFixture), nil
	})
	if _, err := c.ObjektCollectionsPage(context.Background(), "tripleS", ObjektSortNewest, 1, 250); err != nil {
		t.Fatalf("page: %v", err)
	}
	for _, want := range []string{"/objekt-summaries", "artistId=tripleS", "order=newest", "page=1", "size=250"} {
		if !strings.Contains(gotURL, want) {
			t.Fatalf("request URL %q missing %q", gotURL, want)
		}
	}
}

// TestAllObjektCollectionsSinglePage: a short first page ends the run in one call.
func TestAllObjektCollectionsSinglePage(t *testing.T) {
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(objektFixture), nil
	})
	got, err := c.AllObjektCollections(context.Background(), "tripleS", ObjektSortNewest)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if calls != 1 {
		t.Fatalf("made %d calls, want 1", calls)
	}
	if len(got.Collections) != 1 || got.CollectionCount != 1 {
		t.Fatalf("got %d collections (count %d)", len(got.Collections), got.CollectionCount)
	}
}

// TestAllObjektCollectionsPaginates: a full first page forces a second request,
// and the loop stops once CollectionCount entries are gathered.
func TestAllObjektCollectionsPaginates(t *testing.T) {
	total := objektPageSize + 3
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		page := r.URL.Query().Get("page")
		switch page {
		case "1":
			return jsonResponse(fixturePage(total, objektPageSize)), nil
		case "2":
			return jsonResponse(fixturePage(total, 3)), nil
		default:
			t.Errorf("unexpected page %q", page)
			return jsonResponse(fixturePage(total, 0)), nil
		}
	})
	got, err := c.AllObjektCollections(context.Background(), "tripleS", ObjektSortNewest)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if calls != 2 {
		t.Fatalf("made %d calls, want 2", calls)
	}
	if len(got.Collections) != total {
		t.Fatalf("gathered %d collections, want %d", len(got.Collections), total)
	}
}

// favoriteTestCollection is a collection to pin; its slug is the path segment
// the favorite endpoint identifies it by.
var favoriteTestCollection = ObjektCollection{Season: "Binary02", Member: "ChaeWon", CollectionNo: "101Z"}

// noConflictDelay drops the retry pause so the conflict tests do not sleep.
func noConflictDelay(t *testing.T) {
	t.Helper()
	prev := favoriteConflictDelay
	favoriteConflictDelay = 0
	t.Cleanup(func() { favoriteConflictDelay = prev })
}

// conflictResponse is the 409 Cosmo answers when a favorite write overlaps
// another one.
func conflictResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusConflict,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Request Conflict"}}`)),
	}
}

// TestSetObjektFavoritePath checks the pin request targets the collection slug
// and carries the requested state.
func TestSetObjektFavoritePath(t *testing.T) {
	var gotPath, gotBody string
	c := testClient(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if err := c.SetObjektFavorite(context.Background(), favoriteTestCollection, true); err != nil {
		t.Fatalf("favorite: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/objekt-summary/Binary02 ChaeWon 101Z/favorite") {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotBody != `{"isFavorite":true}` {
		t.Fatalf("request body = %q", gotBody)
	}
}

// TestSetObjektFavoriteRetriesConflict checks a 409 is retried rather than
// surfaced: pinning several collections in a row overlaps writes to the user's
// favorite list, which Cosmo rejects instead of queueing.
func TestSetObjektFavoriteRetriesConflict(t *testing.T) {
	noConflictDelay(t)
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return conflictResponse(), nil
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if err := c.SetObjektFavorite(context.Background(), favoriteTestCollection, true); err != nil {
		t.Fatalf("a retried conflict should succeed, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("made %d calls, want 2 (one conflict, one retry)", calls)
	}
}

// TestSetObjektFavoriteConflictGivesUp checks the retries are bounded and the
// 409 is reported once they run out.
func TestSetObjektFavoriteConflictGivesUp(t *testing.T) {
	noConflictDelay(t)
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return conflictResponse(), nil
	})
	err := c.SetObjektFavorite(context.Background(), favoriteTestCollection, true)
	if err == nil {
		t.Fatal("a persistent conflict must be reported")
	}
	if !strings.Contains(err.Error(), "409") {
		t.Fatalf("error = %v, want the conflict status", err)
	}
	if calls != favoriteConflictTries {
		t.Fatalf("made %d calls, want %d", calls, favoriteConflictTries)
	}
}

// TestSetObjektFavoriteOtherErrorNotRetried checks only conflicts are retried.
func TestSetObjektFavoriteOtherErrorNotRetried(t *testing.T) {
	noConflictDelay(t)
	calls := 0
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: 404,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"OBJEKT_SUMMARY_NOT_FOUND"}}`)),
		}, nil
	})
	err := c.SetObjektFavorite(context.Background(), favoriteTestCollection, true)
	if ErrorCode(err) != "OBJEKT_SUMMARY_NOT_FOUND" {
		t.Fatalf("error = %v, want the not-found code", err)
	}
	if calls != 1 {
		t.Fatalf("made %d calls, want 1 (only a conflict is retried)", calls)
	}
}

// fixturePage builds a response reporting collectionCount=total with n
// collection entries (distinct collectionNos).
func fixturePage(total, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"collectionCount":%d,"favoritedObjektCount":0,"collections":[`, total)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"collection":{"collectionNo":"C%d"},"count":1,"objekts":[]}`, i)
	}
	b.WriteString("]}")
	return b.String()
}
