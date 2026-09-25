package cosmo

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Objekt collection endpoint (/objekt-summaries): a user's owned objekts for one
// artist, grouped by collection. The response groups by collection *type* (a
// card design) and nests every owned copy under it with its serial number, so
// the payload scales with distinct collections owned, not raw copy count.
//
// Probed sort/paging behavior: order is "newest" | "oldest" only (anything else
// is a 400); size is honored with no server-side cap observed, and page paginates
// on top of it. collectionCount is returned on every page as the total distinct
// collections, so ObjektCollections pages until that many are gathered.

// ObjektSortNewest and ObjektSortOldest are the only accepted order values.
const (
	ObjektSortNewest = "newest"
	ObjektSortOldest = "oldest"
)

// objektPageSize is the collections-per-request take. One page covers most
// users; heavy collectors span a few pages (see AllObjektCollections).
const objektPageSize = 250

// ObjektCollection is the card design shared by every copy of a collection: its
// identity (Season/CollectionNo/Class/Member), the COMO it grants, and the
// accent color the app tints the card with. FavoritedAt is non-empty when the
// user has pinned the collection (the pin shows on the app loading screen).
type ObjektCollection struct {
	CollectionNo string // "101Z"
	Season       string // "Binary02"
	Class        string // "First" | "Special" | "Welcome" | ...
	Member       string // "ChaeWon"
	ArtistName   string // "tripleS"
	ComoAmount   int
	Transferable bool // transferable by default (a specific copy may still be locked)
	Gridable     bool
	AccentColor  string // hex, e.g. "#75FB4C"
	FrontImage   string
	BackImage    string
	FavoritedAt  string // ISO-8601; empty when not pinned
}

// Favorited reports whether the collection is pinned.
func (c ObjektCollection) Favorited() bool { return c.FavoritedAt != "" }

// slug is the "Season Member CollectionNo" key the favorite endpoint identifies
// a collection by, e.g. "Binary02 ChaeWon 101Z". Matching is case-insensitive.
func (c ObjektCollection) slug() string {
	return c.Season + " " + c.Member + " " + c.CollectionNo
}

// comoGeneratingClasses are the objekt classes that drop COMO monthly. Every
// collection carries a comoAmount, but generation is a fixed per-class property:
// only Special and Premier objekts actually drop COMO (First/Welcome/Double/Unit
// never do). Match the API's exact class casing.
var comoGeneratingClasses = map[string]bool{"Special": true, "Premier": true}

// GeneratesComo reports whether each copy of this collection drops COMO monthly.
// When true, ComoAmount is the monthly rate per copy and OwnedObjekt.MintedAtDay
// is the day of the month it drops.
func (c ObjektCollection) GeneratesComo() bool { return comoGeneratingClasses[c.Class] }

// OwnedObjekt is one owned copy of a collection. ObjektNo is its serial number;
// Transferable is this copy's own flag (e.g. false while used for a grid or if
// it was a challenge reward). Owner is the on-chain wallet holding it.
type OwnedObjekt struct {
	ObjektNo     int64 // serial number
	ObjektID     int64 // inventory objektId; the on-chain tokenId (== TokenID) the send flow transfers
	TokenID      int64
	TokenAddress string // the ERC-721 contract holding this copy (transferFrom target)
	Transferable bool
	Status       string // inventory status, e.g. "minted"
	AcquiredAt   string // ISO-8601
	MintedAt     string // ISO-8601
	MintedAtDay  int    // day of month COMO drops (for generating classes)
	UsedForGrid  bool
	Owner        string // on-chain address
}

// OwnedCollection is a collection the user owns plus every copy of it (Count ==
// len(Objekts)).
type OwnedCollection struct {
	Collection ObjektCollection
	Count      int
	Objekts    []OwnedObjekt
}

// ObjektCollections is a user's full objekt collection for one artist.
// CollectionCount is the number of distinct collections; FavoritedCount is how
// many objekts are pinned.
type ObjektCollections struct {
	CollectionCount int
	FavoritedCount  int
	Collections     []OwnedCollection
}

// ObjektCollectionsPage fetches one page of a user's owned collections for an
// artist. order must be ObjektSortNewest or ObjektSortOldest; page is 1-based.
func (c *Client) ObjektCollectionsPage(ctx context.Context, artist, order string, page, size int) (ObjektCollections, error) {
	params := url.Values{}
	params.Set("artistId", artist)
	params.Set("order", order)
	params.Set("page", strconv.Itoa(page))
	params.Set("size", strconv.Itoa(size))

	var wire objektSummariesResponse
	if err := c.getJSON(ctx, "/objekt-summaries", params, &wire); err != nil {
		return ObjektCollections{}, err
	}
	return wire.toCollections(), nil
}

// AllObjektCollections fetches a user's complete owned collection for an artist,
// paging on objektPageSize until CollectionCount entries are gathered (or a
// short page ends the run). order must be ObjektSortNewest or ObjektSortOldest.
func (c *Client) AllObjektCollections(ctx context.Context, artist, order string) (ObjektCollections, error) {
	var out ObjektCollections
	for page := 1; ; page++ {
		got, err := c.ObjektCollectionsPage(ctx, artist, order, page, objektPageSize)
		if err != nil {
			return ObjektCollections{}, err
		}
		out.CollectionCount = got.CollectionCount
		out.FavoritedCount = got.FavoritedCount
		out.Collections = append(out.Collections, got.Collections...)

		if len(got.Collections) < objektPageSize || len(out.Collections) >= got.CollectionCount {
			break
		}
		select {
		case <-ctx.Done():
			return ObjektCollections{}, ctx.Err()
		case <-time.After(time.Second): // be gentle between pages
		}
	}
	return out, nil
}

// MaxFavoritedObjekts is the server's cap on pinned collections. Going over it
// is not an error: the API answers 201 and silently unpins whichever collection
// has the oldest favoritedAt, with nothing in the response to say so. The app
// warns before evicting, so callers enforce the cap themselves rather than let
// a pin disappear.
const MaxFavoritedObjekts = 9

// favoriteConflictTries is how many times a favorite write is attempted, and
// favoriteConflictDelay the pause between attempts. Cosmo answers 409 ("Request
// Conflict") when a favorite write lands while another is still settling — it
// rejects rather than queues — which pinning several collections in a row walks
// straight into. They are variables so tests can drop the delay.
var (
	favoriteConflictTries = 3
	favoriteConflictDelay = 400 * time.Millisecond
)

// SetObjektFavorite pins or unpins a collection (pins show on the app loading
// screen and float to the top of /objekt-summaries under either sort order).
// Pinning an already-pinned collection is a no-op; one the user does not own
// fails with OBJEKT_SUMMARY_NOT_FOUND. The endpoint answers 201 with an empty
// body, so there is nothing to decode. Callers must keep the pin count within
// MaxFavoritedObjekts.
//
// A 409 conflict is retried (see favoriteConflictTries): the user's favorite
// list takes one write at a time, so back-to-back pins have to be spaced out
// rather than failed.
func (c *Client) SetObjektFavorite(ctx context.Context, col ObjektCollection, favorite bool) error {
	path := "/objekt-summary/" + url.PathEscape(col.slug()) + "/favorite"
	body := map[string]bool{"isFavorite": favorite}
	for attempt := 1; ; attempt++ {
		err := c.sendJSON(ctx, http.MethodPost, path, body, nil)
		var ae *APIError
		if attempt >= favoriteConflictTries || !errors.As(err, &ae) || ae.Status != http.StatusConflict {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(favoriteConflictDelay):
		}
	}
}

// objektSummariesResponse is the wire shape of GET /objekt-summaries. Only the
// fields cosmo-tui renders are decoded.
type objektSummariesResponse struct {
	CollectionCount      int `json:"collectionCount"`
	FavoritedObjektCount int `json:"favoritedObjektCount"`
	Collections          []struct {
		Collection struct {
			Season                string `json:"season"`
			CollectionNo          string `json:"collectionNo"`
			Class                 string `json:"class"`
			Member                string `json:"member"`
			ArtistName            string `json:"artistName"`
			ComoAmount            int    `json:"comoAmount"`
			TransferableByDefault bool   `json:"transferableByDefault"`
			GridableByDefault     bool   `json:"gridableByDefault"`
			AccentColor           string `json:"accentColor"`
			FrontImage            string `json:"frontImage"`
			BackImage             string `json:"backImage"`
			FavoritedAt           string `json:"favoritedAt"`
		} `json:"collection"`
		Count   int `json:"count"`
		Objekts []struct {
			Metadata struct {
				ObjektNo     int64  `json:"objektNo"`
				Transferable bool   `json:"transferable"`
				TokenID      int64  `json:"tokenId"`
				TokenAddress string `json:"tokenAddress"`
			} `json:"metadata"`
			Inventory struct {
				Status      string `json:"status"`
				AcquiredAt  string `json:"acquiredAt"`
				MintedAt    string `json:"mintedAt"`
				MintedAtDay int    `json:"mintedAtDay"`
				UsedForGrid bool   `json:"usedForGrid"`
				Owner       string `json:"owner"`
				ObjektID    int64  `json:"objektId"`
			} `json:"inventory"`
		} `json:"objekts"`
	} `json:"collections"`
}

// toCollections maps the wire response into the exported types.
func (r objektSummariesResponse) toCollections() ObjektCollections {
	out := ObjektCollections{
		CollectionCount: r.CollectionCount,
		FavoritedCount:  r.FavoritedObjektCount,
		Collections:     make([]OwnedCollection, 0, len(r.Collections)),
	}
	for _, c := range r.Collections {
		col := ObjektCollection{
			CollectionNo: c.Collection.CollectionNo,
			Season:       c.Collection.Season,
			Class:        c.Collection.Class,
			Member:       c.Collection.Member,
			ArtistName:   c.Collection.ArtistName,
			ComoAmount:   c.Collection.ComoAmount,
			Transferable: c.Collection.TransferableByDefault,
			Gridable:     c.Collection.GridableByDefault,
			AccentColor:  c.Collection.AccentColor,
			FrontImage:   c.Collection.FrontImage,
			BackImage:    c.Collection.BackImage,
			FavoritedAt:  c.Collection.FavoritedAt,
		}
		objekts := make([]OwnedObjekt, 0, len(c.Objekts))
		for _, o := range c.Objekts {
			objekts = append(objekts, OwnedObjekt{
				ObjektNo:     o.Metadata.ObjektNo,
				ObjektID:     o.Inventory.ObjektID,
				TokenID:      o.Metadata.TokenID,
				TokenAddress: o.Metadata.TokenAddress,
				Transferable: o.Metadata.Transferable,
				Status:       o.Inventory.Status,
				AcquiredAt:   o.Inventory.AcquiredAt,
				MintedAt:     o.Inventory.MintedAt,
				MintedAtDay:  o.Inventory.MintedAtDay,
				UsedForGrid:  o.Inventory.UsedForGrid,
				Owner:        o.Inventory.Owner,
			})
		}
		out.Collections = append(out.Collections, OwnedCollection{
			Collection: col,
			Count:      c.Count,
			Objekts:    objekts,
		})
	}
	return out
}
