package cosmo

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Gravity is a COMO-voting event: users spend COMO to vote among candidates
// (songs, concepts, etc.) that decide something the artist will do. The list
// endpoints (/bff/v4/gravities) split events into upcoming/ongoing/past; the
// detail endpoint (/bff/v3/gravities/{id}) adds the structured Body and, once a
// poll has finished, its ranked Result and the top-spender Leaderboard. Voting
// itself is an on-chain COMO transfer (see the send flow in abstract.go/wallet),
// not covered here - these methods are the read/browse surface.

// Gravity types and poll types. A gravity is either a standalone event or a
// multi-day "grand" (battle-royale) series whose days are separate gravity
// records; its polls are single-choice or the legacy combination form. The
// combination form only appears on old Polygon-era gravities.
const (
	GravityEvent = "event-gravity"
	GravityGrand = "grand-gravity"

	PollSingle      = "single-poll"
	PollCombination = "combination-poll"
)

// Gravity is one voting event. Result and Leaderboard are populated only for
// finished gravities; Polls carries the poll(s) - one for single/grand events,
// and each poll's own Result once it is finalized. Choices on a poll are filled
// separately by PollDetail (the list/detail payloads omit them for ongoing polls).
type Gravity struct {
	ID              int64
	Type            string // GravityEvent | GravityGrand
	PollType        string // PollSingle | PollCombination
	Artist          string
	Title           string
	Description     string
	StartDate       string // entireStartDate (ISO-8601)
	EndDate         string // entireEndDate (ISO-8601)
	BannerImageURL  string
	ContractOutlink string // block explorer link (abscan for Abstract, polygonscan for legacy)
	Body            []BodyBlock
	Polls           []Poll
	Result          *GravityResult     // overall winner, once finalized
	Leaderboard     []LeaderboardEntry // top COMO spenders, once finalized
}

// GravityState is where a gravity sits in its lifecycle. Neither Finalized nor
// the poll window tells them apart alone: Finalized flips partway through the
// post-voting gap, and the window says nothing about whether the tally has been
// published. Only the arrival of a poll's Result marks a gravity as finished.
type GravityState int

const (
	// StateFinished: every poll's result is in.
	StateFinished GravityState = iota
	// StateUpcoming: voting has not opened yet.
	StateUpcoming
	// StateCounted: the tally is locked (Finalized) but the result has not been
	// published. This is the second half of the gap below, and on the wire it is
	// the one shape that never occurs anywhere else: across tripleS's whole
	// 102-gravity history every finalized poll carries a result, so a finalized
	// poll without one can only be a reveal that has not landed yet.
	StateCounted
	// StateCounting: voting has closed, but the result is not revealed yet.
	// Cosmo leaves a gap here on purpose - poll 235 closed at 01:00 UTC and
	// revealed at 04:00 - and throughout it the poll refuses votes. Finalized
	// does not span the whole gap: 235 was still unfinalized at 01:00 and had
	// flipped by 03:07, an hour before its reveal, with no result attached.
	StateCounting
	// StateVoting: a poll is accepting votes right now.
	StateVoting
)

func (s GravityState) String() string {
	switch s {
	case StateUpcoming:
		return "upcoming"
	case StateCounted:
		return "counted"
	case StateCounting:
		return "counting"
	case StateVoting:
		return "voting"
	default:
		return "finished"
	}
}

// State reports the gravity's lifecycle state. For a grand gravity, whose days
// are separate polls, the most live state wins: a day still open makes the
// whole gravity StateVoting, and a day awaiting its reveal outranks one merely
// scheduled.
func (g Gravity) State() GravityState { return g.stateAt(time.Now()) }

// stateAt is State against a fixed instant, for testing.
//
// Result, not Finalized, is what retires a poll: Finalized is checked first of
// the unrevealed cases so a tally locked early can never be read as still
// taking votes, whatever the clock says about its window.
func (g Gravity) stateAt(now time.Time) GravityState {
	var voting, counting, counted, upcoming bool
	for _, p := range g.Polls {
		switch {
		case p.Result != nil:
		case p.Finalized:
			counted = true
		case !p.startedAt(now):
			upcoming = true
		case p.endedAt(now):
			counting = true
		default:
			voting = true
		}
	}
	switch {
	case voting:
		return StateVoting
	case counting:
		return StateCounting
	case counted:
		return StateCounted
	case upcoming:
		return StateUpcoming
	}
	return StateFinished
}

// Started reports whether the poll's voting window has opened.
func (p Poll) Started() bool { return p.startedAt(time.Now()) }

// Ended reports whether the poll's voting window has closed. A poll can be
// ended and still unfinalized: that is the counting gap before the reveal.
func (p Poll) Ended() bool { return p.endedAt(time.Now()) }

// startedAt is Started against a fixed instant, for testing. A poll whose
// startDate is missing or unparsable counts as started: the API always sends
// one, and reading a bad value as "not yet" would hide a live vote, which is
// the worse of the two failures.
func (p Poll) startedAt(now time.Time) bool {
	t, err := time.Parse(time.RFC3339, p.StartDate)
	if err != nil {
		return true
	}
	return !now.Before(t)
}

// endedAt is Ended against a fixed instant. It mirrors startedAt's fallback: an
// unusable endDate reads as "not ended", so a bad date never hides a vote that
// may still be open. The server has the final say either way - a ballot it
// refuses to authorize never reaches the chain.
func (p Poll) endedAt(now time.Time) bool {
	t, err := time.Parse(time.RFC3339, p.EndDate)
	if err != nil {
		return false
	}
	return !now.Before(t)
}

// BodyBlock is one block of a gravity's rich description. Type is one of
// heading, text, image, video, or spacing; the relevant fields are set per type
// (Text/Align for heading|text, ImageURL for image, VideoURL/ThumbnailImageURL
// for video, Height for spacing and media sizing).
type BodyBlock struct {
	Type              string
	Text              string
	Align             string
	ImageURL          string
	VideoURL          string
	ThumbnailImageURL string
	Height            float64
}

// Poll is one round of a gravity. Choices is the candidate list (filled by
// PollDetail); Result is the ranked outcome, present once the reveal lands -
// which trails Finalized, so the two are not interchangeable (see StateCounted).
type Poll struct {
	ID             int64
	Type           string
	GravityID      int64
	Title          string
	StartDate      string
	EndDate        string
	RevealDate     string
	Finalized      bool
	IndexInGravity int
	Choices        []Choice
	Result         *PollResult
}

// Choice is one candidate in a poll.
type Choice struct {
	ID          string
	Title       string
	Description string
	ImageURL    string
}

// PollResult is a finalized poll's ranked tally.
type PollResult struct {
	TotalComoUsed int64
	Results       []VoteResult
}

// VoteResult is one candidate's standing in a finalized poll.
type VoteResult struct {
	Rank           int
	ChoiceName     string
	ChoiceImageURL string
	ComoUsed       int64
}

// GravityResult is a finished gravity's overall winner.
type GravityResult struct {
	TotalComoUsed  int64
	ResultTitle    string
	ResultImageURL string
}

// LeaderboardEntry is one user's total COMO spend on a gravity (top 10).
type LeaderboardEntry struct {
	Rank     int
	Nickname string
	Address  string
	ComoUsed int64
}

// GravityStatus is the signed-in user's own participation in a gravity: their
// overall rank and per-poll COMO spend.
type GravityStatus struct {
	Rank          int
	TotalComoUsed int64
	Votes         []PollVoteStatus
}

// PollVoteStatus is the user's spend on one poll of a gravity, plus each vote
// they cast in it. Votes is populated once a poll's result is revealed; during
// an open vote Cosmo reports ComoUsed 0 and no votes even for a ballot that is
// already on-chain, so an empty record does not mean the user did not vote.
type PollVoteStatus struct {
	PollID   int64
	ComoUsed int64
	Votes    []Vote
}

// Vote is one ballot the user cast: which candidate, how much COMO, and when.
type Vote struct {
	ChoiceID   string
	ChoiceName string // "Trust No One"
	ComoUsed   int64
	At         string // ISO-8601
}

// GravityLists is the upcoming/ongoing/past split returned by /v4/gravities.
// PastCount is the server's total count of past gravities, for paging Past.
type GravityLists struct {
	Upcoming  []Gravity
	Ongoing   []Gravity
	Past      []Gravity
	PastCount int
}

// gravityPastPageSize is the take used when paging past gravities.
const gravityPastPageSize = 30

// OngoingGravities fetches what is open for voting now plus what is scheduled
// next for an artist. Past is dropped; PastCount carries the artist's full
// history count.
//
// It asks for category=all, not category=ongoing: the ongoing category always
// returns an empty upcoming[], however many gravities are queued up. Verified
// on 2026-07-25, when tripleS had three upcoming gravities (196/197/198) that
// category=ongoing omitted entirely and category=all returned. take=1 is the
// smallest page the endpoint accepts - take=0 is a 400 - so a single past entry
// rides along on the response and is discarded here.
func (c *Client) OngoingGravities(ctx context.Context, artist string) (GravityLists, error) {
	params := url.Values{}
	params.Set("artistId", artist)
	params.Set("category", "all")
	params.Set("skip", "0")
	params.Set("take", "1")

	var wire gravityListResponse
	if err := c.getJSONV4(ctx, "/gravities", params, &wire); err != nil {
		return GravityLists{}, err
	}
	lists := wire.toLists()
	lists.Past = nil
	return lists, nil
}

// PastGravities fetches one page of an artist's past gravities, newest first
// (/v4/gravities?category=all&sortBy=endDate&sort=desc). skip/take paginate;
// take<=0 uses gravityPastPageSize. The returned PastCount is the total, for
// deciding whether more pages remain.
func (c *Client) PastGravities(ctx context.Context, artist string, skip, take int) (GravityLists, error) {
	if take <= 0 {
		take = gravityPastPageSize
	}
	params := url.Values{}
	params.Set("artistId", artist)
	params.Set("category", "all")
	params.Set("skip", strconv.Itoa(skip))
	params.Set("take", strconv.Itoa(take))
	params.Set("sortBy", "endDate")
	params.Set("sort", "desc")

	var wire gravityListResponse
	if err := c.getJSONV4(ctx, "/gravities", params, &wire); err != nil {
		return GravityLists{}, err
	}
	return wire.toLists(), nil
}

// AllPastGravities fetches an artist's complete past-gravity list, newest
// first, paging on gravityPastPageSize until PastCount entries are gathered (or
// a short page ends the run). It mirrors AllObjektCollections: one call for the
// whole browsable history, with a gentle pause between pages.
func (c *Client) AllPastGravities(ctx context.Context, artist string) ([]Gravity, error) {
	var out []Gravity
	for skip := 0; ; skip += gravityPastPageSize {
		got, err := c.PastGravities(ctx, artist, skip, gravityPastPageSize)
		if err != nil {
			return nil, err
		}
		out = append(out, got.Past...)
		if len(got.Past) < gravityPastPageSize || len(out) >= got.PastCount {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second): // be gentle between pages
		}
	}
	return out, nil
}

// GravityDetail fetches one gravity's full record (/v3/gravities/{id}): the
// structured Body, its polls, and - once finished - the Result and Leaderboard.
func (c *Client) GravityDetail(ctx context.Context, id int64) (Gravity, error) {
	var wire struct {
		Gravity gravityWire `json:"gravity"`
	}
	if err := c.getJSON(ctx, "/gravities/"+strconv.FormatInt(id, 10), nil, &wire); err != nil {
		return Gravity{}, err
	}
	return wire.Gravity.toGravity(), nil
}

// PollDetail fetches a poll's candidate list (/v3/polls/{id}), used to show the
// choices for an ongoing poll that the gravity payload leaves empty.
func (c *Client) PollDetail(ctx context.Context, pollID int64) (Poll, error) {
	var wire struct {
		PollDetail pollDetailWire `json:"pollDetail"`
	}
	if err := c.getJSON(ctx, "/polls/"+strconv.FormatInt(pollID, 10), nil, &wire); err != nil {
		return Poll{}, err
	}
	return wire.PollDetail.toPoll(), nil
}

// GravityStatus fetches the signed-in user's participation in a gravity
// (/v3/gravities/{id}/status): overall rank and per-poll COMO spend.
func (c *Client) GravityStatus(ctx context.Context, id int64) (GravityStatus, error) {
	var wire struct {
		Status struct {
			Rank          int   `json:"rank"`
			TotalComoUsed int64 `json:"totalComoUsed"`
			VoteStatuses  []struct {
				PollID   int64 `json:"pollId"`
				ComoUsed int64 `json:"comoUsed"`
				Votes    []struct {
					ChoiceID string `json:"choiceId"`
					VoteTo   string `json:"voteTo"`
					ComoUsed int64  `json:"comoUsed"`
					At       string `json:"at"`
				} `json:"votes"`
			} `json:"voteStatuses"`
		} `json:"status"`
	}
	if err := c.getJSON(ctx, "/gravities/"+strconv.FormatInt(id, 10)+"/status", nil, &wire); err != nil {
		return GravityStatus{}, err
	}
	out := GravityStatus{Rank: wire.Status.Rank, TotalComoUsed: wire.Status.TotalComoUsed}
	for _, v := range wire.Status.VoteStatuses {
		ps := PollVoteStatus{PollID: v.PollID, ComoUsed: v.ComoUsed}
		for _, b := range v.Votes {
			ps.Votes = append(ps.Votes, Vote{
				ChoiceID:   b.ChoiceID,
				ChoiceName: b.VoteTo,
				ComoUsed:   b.ComoUsed,
				At:         b.At,
			})
		}
		out.Votes = append(out.Votes, ps)
	}
	return out, nil
}

// pollContractRe pulls the contract address out of a gravity's block-explorer
// link, e.g. "https://abscan.org/address/0xF1A7…#readProxyContract".
var pollContractRe = regexp.MustCompile(`/address/(0x[0-9a-fA-F]{40})`)

// PollContract returns the on-chain contract a vote for this gravity is cast
// to, parsed from ContractOutlink. onAbstract reports whether the link points at
// Abstract (abscan): pre-migration gravities link to Polygon instead, and those
// are long closed, so a vote can only ever be cast against an Abstract one.
func (g Gravity) PollContract() (addr string, onAbstract bool) {
	m := pollContractRe.FindStringSubmatch(g.ContractOutlink)
	if m == nil {
		return "", false
	}
	return m[1], strings.Contains(g.ContractOutlink, "abscan.org")
}

// Votable reports whether a vote can actually be cast on this gravity: a poll
// must be accepting votes right now (not scheduled, not closed and counting),
// it must be a single-choice poll (the legacy combination form is only found on
// closed Polygon-era events), and it must live on Abstract.
func (g Gravity) Votable() bool {
	if g.State() != StateVoting || g.PollType == PollCombination {
		return false
	}
	_, onAbstract := g.PollContract()
	return onAbstract
}

// OpenPoll returns the poll currently accepting votes, or false when none is.
// A poll outside its window does not count, in either direction: a ballot
// fabricated against one that has not opened, or that has closed and is waiting
// to be revealed, would be rejected. Finalized and Result are both refused on
// top of the window check - either one means the vote is over regardless of
// what the dates say, and this is the gate a real COMO spend passes through.
func (g Gravity) OpenPoll() (Poll, bool) {
	for _, p := range g.Polls {
		if !p.Finalized && p.Result == nil && p.Started() && !p.Ended() {
			return p, true
		}
	}
	return Poll{}, false
}

// CountingPoll returns a poll whose voting is over but whose result is not
// published yet, or false when none is. It spans both halves of that gap -
// still counting, and counted but unrevealed - because the deadline they share
// is the same RevealDate, which is all the callers want from it.
func (g Gravity) CountingPoll() (Poll, bool) {
	for _, p := range g.Polls {
		if p.Result != nil {
			continue
		}
		if p.Finalized || (p.Started() && p.Ended()) {
			return p, true
		}
	}
	return Poll{}, false
}

// NextPoll returns the poll due to open next, or false when none is scheduled.
// It is what a gravity shows before its voting starts.
func (g Gravity) NextPoll() (Poll, bool) {
	var next Poll
	found := false
	for _, p := range g.Polls {
		if p.Finalized || p.Started() {
			continue
		}
		if !found || p.StartDate < next.StartDate {
			next, found = p, true
		}
	}
	return next, found
}

// FabricateVote asks Cosmo to authorize a ballot, returning the opaque voteData
// payload to embed in the on-chain COMO transfer. The payload carries the poll
// id, the chosen candidate, and a server signature over both, so a client
// cannot construct a valid vote for a candidate on its own - this call is a
// required step, not a convenience.
//
// It does not spend anything by itself; the COMO only moves when the transfer
// carrying this payload is broadcast.
func (c *Client) FabricateVote(ctx context.Context, pollID int64, choiceID string, comoAmount int64) ([]byte, error) {
	body := map[string]any{"choiceId": choiceID, "comoAmount": comoAmount}
	var wire struct {
		VoteData string `json:"voteData"`
	}
	path := "/gravity-poll/" + strconv.FormatInt(pollID, 10) + "/fabricate-vote"
	if err := c.sendJSON(ctx, http.MethodPost, path, body, &wire); err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(wire.VoteData, "0x"))
	if err != nil {
		return nil, fmt.Errorf("vote payload: %w", err)
	}
	if len(raw) == 0 {
		return nil, errors.New("vote payload was empty")
	}
	return raw, nil
}

// ComoBalance returns the user's spendable COMO for an artist (/comos), the
// figure the vote screens budget against. Profile() also reports it, but that
// aggregates several endpoints; a vote only needs this one number.
func (c *Client) ComoBalance(ctx context.Context, artist string) (int64, error) {
	params := url.Values{"artistId": {artist}, "category": {"ALL"}, "take": {"1"}, "skip": {"0"}}
	var comos comosResponse
	if err := c.getJSON(ctx, "/comos", params, &comos); err != nil {
		return 0, err
	}
	return int64(comos.TotalComo), nil
}

// --- wire shapes ---

// gravityListResponse is the shared envelope of both /v4/gravities calls
// (category=ongoing fills ongoing/upcoming; category=all fills past).
type gravityListResponse struct {
	PastGravityCount int           `json:"pastGravityCount"`
	Upcoming         []gravityWire `json:"upcoming"`
	Ongoing          []gravityWire `json:"ongoing"`
	Past             []gravityWire `json:"past"`
}

func (r gravityListResponse) toLists() GravityLists {
	return GravityLists{
		Upcoming:  mapGravities(r.Upcoming),
		Ongoing:   mapGravities(r.Ongoing),
		Past:      mapGravities(r.Past),
		PastCount: r.PastGravityCount,
	}
}

func mapGravities(ws []gravityWire) []Gravity {
	if len(ws) == 0 {
		return nil
	}
	out := make([]Gravity, len(ws))
	for i, w := range ws {
		out[i] = w.toGravity()
	}
	return out
}

// gravityWire is one gravity as returned by both the list and detail endpoints;
// list entries simply omit the finished-only fields (result/leaderboard).
type gravityWire struct {
	ID              int64      `json:"id"`
	Type            string     `json:"type"`
	PollType        string     `json:"pollType"`
	Artist          string     `json:"artist"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	EntireStartDate string     `json:"entireStartDate"`
	EntireEndDate   string     `json:"entireEndDate"`
	BannerImageURL  string     `json:"bannerImageUrl"`
	ContractOutlink string     `json:"contractOutlink"`
	Body            []bodyWire `json:"body"`
	Polls           []pollWire `json:"polls"`
	Result          *struct {
		TotalComoUsed  int64  `json:"totalComoUsed"`
		ResultTitle    string `json:"resultTitle"`
		ResultImageURL string `json:"resultImageUrl"`
	} `json:"result"`
	Leaderboard *struct {
		UserRanking []struct {
			Rank          int   `json:"rank"`
			TotalComoUsed int64 `json:"totalComoUsed"`
			User          struct {
				Nickname string `json:"nickname"`
				Address  string `json:"address"`
			} `json:"user"`
		} `json:"userRanking"`
	} `json:"leaderboard"`
}

func (w gravityWire) toGravity() Gravity {
	g := Gravity{
		ID:              w.ID,
		Type:            w.Type,
		PollType:        w.PollType,
		Artist:          w.Artist,
		Title:           w.Title,
		Description:     w.Description,
		StartDate:       w.EntireStartDate,
		EndDate:         w.EntireEndDate,
		BannerImageURL:  w.BannerImageURL,
		ContractOutlink: w.ContractOutlink,
	}
	for _, b := range w.Body {
		g.Body = append(g.Body, BodyBlock(b))
	}
	for _, p := range w.Polls {
		g.Polls = append(g.Polls, p.toPoll())
	}
	if w.Result != nil {
		g.Result = &GravityResult{
			TotalComoUsed:  w.Result.TotalComoUsed,
			ResultTitle:    w.Result.ResultTitle,
			ResultImageURL: w.Result.ResultImageURL,
		}
	}
	if w.Leaderboard != nil {
		for _, e := range w.Leaderboard.UserRanking {
			g.Leaderboard = append(g.Leaderboard, LeaderboardEntry{
				Rank:     e.Rank,
				Nickname: e.User.Nickname,
				Address:  e.User.Address,
				ComoUsed: e.TotalComoUsed,
			})
		}
	}
	return g
}

// bodyWire mirrors BodyBlock field-for-field, so BodyBlock(b) converts it.
type bodyWire struct {
	Type              string  `json:"type"`
	Text              string  `json:"text"`
	Align             string  `json:"align"`
	ImageURL          string  `json:"imageUrl"`
	VideoURL          string  `json:"videoUrl"`
	ThumbnailImageURL string  `json:"thumbnailImageUrl"`
	Height            float64 `json:"height"`
}

// pollWire is a poll inside a gravity payload, carrying its result once finished.
type pollWire struct {
	ID             int64  `json:"id"`
	Type           string `json:"type"`
	GravityID      int64  `json:"gravityId"`
	Title          string `json:"title"`
	StartDate      string `json:"startDate"`
	EndDate        string `json:"endDate"`
	RevealDate     string `json:"revealDate"`
	Finalized      bool   `json:"finalized"`
	IndexInGravity int    `json:"indexInGravity"`
	Result         *struct {
		TotalComoUsed int64 `json:"totalComoUsed"`
		VoteResults   []struct {
			Rank        int `json:"rank"`
			VotedChoice struct {
				ChoiceName     string `json:"choiceName"`
				ChoiceImageURL string `json:"choiceImageUrl"`
				ComoUsed       int64  `json:"comoUsed"`
			} `json:"votedChoice"`
		} `json:"voteResults"`
	} `json:"result"`
}

func (w pollWire) toPoll() Poll {
	p := Poll{
		ID:             w.ID,
		Type:           w.Type,
		GravityID:      w.GravityID,
		Title:          w.Title,
		StartDate:      w.StartDate,
		EndDate:        w.EndDate,
		RevealDate:     w.RevealDate,
		Finalized:      w.Finalized,
		IndexInGravity: w.IndexInGravity,
	}
	if w.Result != nil {
		res := &PollResult{TotalComoUsed: w.Result.TotalComoUsed}
		for _, r := range w.Result.VoteResults {
			res.Results = append(res.Results, VoteResult{
				Rank:           r.Rank,
				ChoiceName:     r.VotedChoice.ChoiceName,
				ChoiceImageURL: r.VotedChoice.ChoiceImageURL,
				ComoUsed:       r.VotedChoice.ComoUsed,
			})
		}
		p.Result = res
	}
	return p
}

// pollDetailWire is /v3/polls/{id}: the standalone poll with its candidate list.
type pollDetailWire struct {
	ID             int64  `json:"id"`
	Type           string `json:"type"`
	GravityID      int64  `json:"gravityId"`
	Title          string `json:"title"`
	StartDate      string `json:"startDate"`
	EndDate        string `json:"endDate"`
	RevealDate     string `json:"revealDate"`
	Finalized      bool   `json:"finalized"`
	IndexInGravity int    `json:"indexInGravity"`
	Choices        []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		TxImageURL  string `json:"txImageUrl"`
	} `json:"choices"`
}

func (w pollDetailWire) toPoll() Poll {
	p := Poll{
		ID:             w.ID,
		Type:           w.Type,
		GravityID:      w.GravityID,
		Title:          w.Title,
		StartDate:      w.StartDate,
		EndDate:        w.EndDate,
		RevealDate:     w.RevealDate,
		Finalized:      w.Finalized,
		IndexInGravity: w.IndexInGravity,
	}
	for _, c := range w.Choices {
		p.Choices = append(p.Choices, Choice{
			ID:          c.ID,
			Title:       c.Title,
			Description: c.Description,
			ImageURL:    c.TxImageURL,
		})
	}
	return p
}
