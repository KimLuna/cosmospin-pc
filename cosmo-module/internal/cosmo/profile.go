package cosmo

import (
	"context"
	"net/url"
	"strconv"
)

// Profile is the aggregated account profile for one artist (group), assembled
// from the several endpoints the app's Profile tab loads: /users/me (identity),
// /comos (per-artist COMO balance), /users/{id}?artistId (the public card: bio,
// fandom, follow duration), /user-stats-daily (activity counters, incl. the
// objekt count), and /activities/my-objekts (owned-objekt breakdown).
type Profile struct {
	ID                 int
	Nickname           string
	Address            string // wallet address
	StatusMessage      string // bio; empty when unset
	FandomName         string
	FollowDurationDays int
	CurrentStreak      int
	CreatedAt          string // ISO-8601; render via LocalDate

	TotalComo   int
	TotalObjekt int

	Stats          DailyStats
	ObjektsByClass []ObjektGroup
	Channels       []MembershipChannel

	// Self is true for the logged-in user's own profile (fetched via /users/me),
	// which always exposes every section and carries wallet totals. For another
	// user (fetched via UserProfile) it is false: COMO is unavailable (HasComo
	// false) and Visibility governs which sections that user has made public.
	Self       bool
	HasComo    bool
	Visibility ProfileVisibility
}

// ProfileVisibility is GET /users/{id}/profile-visibility: the per-section
// privacy flags another user has set. Own profiles ignore it (all sections
// show). Distinct sections map to the profile view's blocks.
type ProfileVisibility struct {
	Activity         bool `json:"activity"`
	FavoritedObjekt  bool `json:"favoritedObjekt"`
	Overview         bool `json:"overview"`
	ConnectedChannel bool `json:"connectedChannel"`
	Badge            bool `json:"badge"`
	Ranking          bool `json:"ranking"`
	ObjektStatistics bool `json:"objektStatistics"`
}

// ShowOverview reports whether the headline stats block should render (always
// for one's own profile; per the visibility flag for others).
func (p Profile) ShowOverview() bool { return p.Self || p.Visibility.Overview }

// ShowObjektStats reports whether the owned-objekt breakdown should render.
func (p Profile) ShowObjektStats() bool { return p.Self || p.Visibility.ObjektStatistics }

// ShowChannels reports whether the joined-channels block should render.
func (p Profile) ShowChannels() bool { return p.Self || p.Visibility.ConnectedChannel }

// UserSearchResult is one hit from GET /users/search (nickname lookup).
type UserSearchResult struct {
	ID       int    `json:"id"`
	Nickname string `json:"nickname"`
	Address  string `json:"address"`
}

// userSearchResponse is the shape of GET /users/search.
type userSearchResponse struct {
	Results []UserSearchResult `json:"results"`
}

// MembershipChannel is one member's subscription channel for the current user.
// IsConnected reports whether the user holds that member's membership (the flag
// that gates replay downloads and the talk feature); DaysTogether is the
// per-member streak. Distinct from talk.go's Channel (a messaging conversation).
type MembershipChannel struct {
	ID           int    `json:"id"` // artistMemberId (matches members.APIID)
	Name         string `json:"name"`
	IsConnected  bool   `json:"isConnected"`
	DaysTogether int    `json:"daysTogether"`
}

// ConnectedChannels returns only the channels the user has joined.
func (p Profile) ConnectedChannels() []MembershipChannel {
	var out []MembershipChannel
	for _, ch := range p.Channels {
		if ch.IsConnected {
			out = append(out, ch)
		}
	}
	return out
}

// DailyStats are the headline activity counters (GET /user-stats-daily).
type DailyStats struct {
	ObjektCount        int `json:"objektCount"`
	JoinedLiveCount    int `json:"joinedLiveCount"`
	JoinedGravityCount int `json:"joinedGravityCount"`
	OfflineBadgeCount  int `json:"offlineBadgeCount"`
	CompletedGridCount int `json:"completedGridCount"`
}

// ObjektGroup is one bucket in an owned-objekt breakdown (by class, member, …).
type ObjektGroup struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Color string `json:"color"`
}

// meResponse is the subset of GET /users/me we use. Note the per-artist
// assetBalance it also carries is a dead field (server always returns 0 for
// both COMO and objekt); the live balances come from /comos and
// /user-stats-daily instead.
type meResponse struct {
	Profile struct {
		ID            int    `json:"id"`
		Nickname      string `json:"nickname"`
		Address       string `json:"address"`
		CurrentStreak int    `json:"currentStreak"`
		CreatedAt     string `json:"createdAt"`
	} `json:"profile"`
}

// comosResponse is the subset of GET /comos we use: the per-artist COMO wallet
// balance (totalComo) plus a paged earn/use ledger we ignore. take and skip are
// required query params even though we only read the aggregate total.
type comosResponse struct {
	TotalComo int `json:"totalComo"`
}

// userPublic is the subset of GET /users/{id}?artistId we use (the public card).
type userPublic struct {
	Nickname           string `json:"nickname"`
	Address            string `json:"address"`
	StatusMessage      string `json:"statusMessage"`
	FandomName         string `json:"fandomName"`
	FollowDurationDays int    `json:"followDurationDays"`
	CurrentStreak      int    `json:"currentStreak"`
	CreatedAt          string `json:"createdAt"`
}

// objektSummary is the shape of GET /bff/v4/activities/my-objekts.
type objektSummary struct {
	TotalCount int           `json:"totalCount"`
	Result     []ObjektGroup `json:"result"`
}

// Profile fetches the aggregated account profile for a group (artistId). The
// calls are sequential because the public-card and stats endpoints need the
// numeric user id that only /users/me returns.
func (c *Client) Profile(ctx context.Context, group string) (Profile, error) {
	var me meResponse
	if err := c.getJSON(ctx, "/users/me", nil, &me); err != nil {
		return Profile{}, err
	}
	p := Profile{
		ID:            me.Profile.ID,
		Nickname:      me.Profile.Nickname,
		Address:       me.Profile.Address,
		CurrentStreak: me.Profile.CurrentStreak,
		CreatedAt:     me.Profile.CreatedAt,
		Self:          true,
		HasComo:       true,
	}

	// COMO comes from its own ledger endpoint; take/skip are required even
	// though we only want the aggregate balance.
	comoParams := url.Values{"artistId": {group}, "category": {"ALL"}, "take": {"1"}, "skip": {"0"}}
	var comos comosResponse
	if err := c.getJSON(ctx, "/comos", comoParams, &comos); err != nil {
		return Profile{}, err
	}
	p.TotalComo = comos.TotalComo

	artist := url.Values{"artistId": {group}}

	var pub userPublic
	if err := c.getJSON(ctx, "/users/"+strconv.Itoa(p.ID), artist, &pub); err != nil {
		return Profile{}, err
	}
	p.StatusMessage = pub.StatusMessage
	p.FandomName = pub.FandomName
	p.FollowDurationDays = pub.FollowDurationDays
	// The per-artist card carries the values the tab actually shows; prefer them
	// when present.
	if pub.CurrentStreak != 0 {
		p.CurrentStreak = pub.CurrentStreak
	}
	if pub.Nickname != "" {
		p.Nickname = pub.Nickname
	}

	statsParams := url.Values{"userId": {strconv.Itoa(p.ID)}, "artistId": {group}}
	if err := c.getJSON(ctx, "/user-stats-daily", statsParams, &p.Stats); err != nil {
		return Profile{}, err
	}
	p.TotalObjekt = p.Stats.ObjektCount

	objParams := url.Values{"kind": {"class"}, "artistId": {group}}
	var summary objektSummary
	if err := c.getJSONV4(ctx, "/activities/my-objekts", objParams, &summary); err != nil {
		return Profile{}, err
	}
	p.ObjektsByClass = summary.Result

	chParams := url.Values{"userId": {strconv.Itoa(p.ID)}, "artistId": {group}}
	if err := c.getJSON(ctx, "/channels", chParams, &p.Channels); err != nil {
		return Profile{}, err
	}

	return p, nil
}

// SearchUsers looks up users by nickname (GET /users/search). The match is a
// prefix search ranked with the closest match first; a nil/empty slice means no
// user was found. Not an id lookup - the API offers no numeric-id search.
func (c *Client) SearchUsers(ctx context.Context, nickname string) ([]UserSearchResult, error) {
	params := url.Values{"nickname": {nickname}, "skip": {"0"}, "take": {"100"}}
	var resp userSearchResponse
	if err := c.getJSON(ctx, "/users/search", params, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// UserProfile fetches another user's public profile for a group. It mirrors
// Profile but keys on the given user id and skips /users/me, so wallet totals
// (COMO) are unavailable; the per-section privacy flags in profile-visibility
// decide which of the stats/objekt/channel blocks that user exposes, and hidden
// sections are not fetched.
func (c *Client) UserProfile(ctx context.Context, group string, userID int) (Profile, error) {
	idStr := strconv.Itoa(userID)
	artist := url.Values{"artistId": {group}}

	var pub userPublic
	if err := c.getJSON(ctx, "/users/"+idStr, artist, &pub); err != nil {
		return Profile{}, err
	}
	p := Profile{
		ID:                 userID,
		Nickname:           pub.Nickname,
		Address:            pub.Address,
		StatusMessage:      pub.StatusMessage,
		FandomName:         pub.FandomName,
		FollowDurationDays: pub.FollowDurationDays,
		CurrentStreak:      pub.CurrentStreak,
		CreatedAt:          pub.CreatedAt,
	}

	if err := c.getJSON(ctx, "/users/"+idStr+"/profile-visibility", artist, &p.Visibility); err != nil {
		return Profile{}, err
	}

	idParams := url.Values{"userId": {idStr}, "artistId": {group}}

	if p.Visibility.Overview {
		if err := c.getJSON(ctx, "/user-stats-daily", idParams, &p.Stats); err != nil {
			return Profile{}, err
		}
		p.TotalObjekt = p.Stats.ObjektCount
	}

	if p.Visibility.ObjektStatistics {
		objParams := url.Values{"kind": {"class"}, "userId": {idStr}, "artistId": {group}}
		var summary objektSummary
		if err := c.getJSONV4(ctx, "/activities/my-objekts", objParams, &summary); err != nil {
			return Profile{}, err
		}
		p.ObjektsByClass = summary.Result
	}

	if p.Visibility.ConnectedChannel {
		if err := c.getJSON(ctx, "/channels", idParams, &p.Channels); err != nil {
			return Profile{}, err
		}
	}

	return p, nil
}
