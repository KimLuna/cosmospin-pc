package cosmo

import (
	"encoding/json"
	"testing"
	"time"
)

// TestGravityListWirePayload checks the shared /v4/gravities envelope decodes:
// category=ongoing fills ongoing[] (past empty), and a gravity summary carries
// its polls and body but not the finished-only result/leaderboard.
func TestGravityListWirePayload(t *testing.T) {
	body := `{
	  "pastGravityCount": 0,
	  "upcoming": [],
	  "ongoing": [
	    {
	      "id": 194, "type": "event-gravity", "pollType": "single-poll",
	      "artist": "tripleS",
	      "entireStartDate": "2026-07-24T01:00:00.000Z",
	      "entireEndDate": "2026-07-25T04:00:00.000Z",
	      "bannerImageUrl": "https://resources.cosmo.fans/b.webp",
	      "contractOutlink": "https://abscan.org/address/0xF1A7#readProxyContract",
	      "title": "msnz Cover Stage",
	      "description": "Select the cover song",
	      "body": [{"type":"image","imageUrl":"https://x/i.webp","height":1618.6}],
	      "polls": [
	        {"id":235,"type":"single-poll","gravityId":194,
	         "startDate":"2026-07-24T01:00:00.000Z","endDate":"2026-07-25T01:00:00.000Z",
	         "indexInGravity":1,"finalized":false,"revealDate":"2026-07-25T04:00:00.000Z"}
	      ]
	    }
	  ],
	  "past": []
	}`

	var wire gravityListResponse
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	lists := wire.toLists()
	if lists.PastCount != 0 || len(lists.Past) != 0 || len(lists.Upcoming) != 0 {
		t.Fatalf("past/upcoming = %d/%d/%d, want 0/0/0", lists.PastCount, len(lists.Past), len(lists.Upcoming))
	}
	if len(lists.Ongoing) != 1 {
		t.Fatalf("ongoing len = %d, want 1", len(lists.Ongoing))
	}
	g := lists.Ongoing[0]
	if g.ID != 194 || g.Type != GravityEvent || g.PollType != PollSingle {
		t.Fatalf("gravity id/type/pollType = %d/%q/%q", g.ID, g.Type, g.PollType)
	}
	if g.StartDate != "2026-07-24T01:00:00.000Z" || g.Title != "msnz Cover Stage" {
		t.Fatalf("startDate/title = %q/%q", g.StartDate, g.Title)
	}
	if len(g.Body) != 1 || g.Body[0].Type != "image" || g.Body[0].Height != 1618.6 {
		t.Fatalf("body = %+v", g.Body)
	}
	if len(g.Polls) != 1 || g.Polls[0].ID != 235 || g.Polls[0].Finalized {
		t.Fatalf("polls = %+v", g.Polls)
	}
	// Judged against an instant inside the poll's window: the payload alone
	// cannot say the vote is open, since the poll stays unfinalized for hours
	// after it closes.
	if s := g.stateAt(time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)); s != StateVoting {
		t.Fatalf("state mid-window = %v, want voting", s)
	}
	if g.Result != nil || len(g.Leaderboard) != 0 {
		t.Fatalf("finished-only fields set on ongoing gravity: result=%v leaderboard=%v", g.Result, g.Leaderboard)
	}
}

// TestGravityListUpcoming checks the upcoming[] arm of the same envelope: the
// category=all response carries scheduled gravities whose poll has not opened
// yet, alongside the live one. This is the arm category=ongoing never fills,
// which is why OngoingGravities asks for category=all.
func TestGravityListUpcoming(t *testing.T) {
	body := `{
	  "pastGravityCount": 102,
	  "upcoming": [
	    {"id":198,"type":"event-gravity","pollType":"single-poll","artist":"tripleS",
	     "title":"msnz Cover Stage zenith",
	     "entireStartDate":"2026-07-27T01:00:00.000Z",
	     "entireEndDate":"2026-07-28T04:00:00.000Z",
	     "contractOutlink":"https://abscan.org/address/0xF1A7#readProxyContract",
	     "polls":[{"id":239,"gravityId":198,"startDate":"2026-07-27T01:00:00.000Z","finalized":false}]},
	    {"id":196,"type":"event-gravity","pollType":"single-poll","artist":"tripleS",
	     "title":"msnz Cover Stage sun",
	     "entireStartDate":"2026-07-25T01:00:00.000Z",
	     "entireEndDate":"2026-07-26T04:00:00.000Z",
	     "contractOutlink":"https://abscan.org/address/0xF1A7#readProxyContract",
	     "polls":[{"id":237,"gravityId":196,"startDate":"2026-07-25T01:00:00.000Z","finalized":false}]}
	  ],
	  "ongoing": [
	    {"id":194,"type":"event-gravity","pollType":"single-poll","artist":"tripleS",
	     "title":"msnz Cover Stage moon",
	     "entireStartDate":"2026-07-24T01:00:00.000Z",
	     "entireEndDate":"2026-07-25T04:00:00.000Z",
	     "polls":[{"id":235,"gravityId":194,"startDate":"2026-07-24T01:00:00.000Z","finalized":false}]}
	  ],
	  "past": []
	}`

	var wire gravityListResponse
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	lists := wire.toLists()
	if len(lists.Upcoming) != 2 || len(lists.Ongoing) != 1 {
		t.Fatalf("upcoming/ongoing = %d/%d, want 2/1", len(lists.Upcoming), len(lists.Ongoing))
	}
	if lists.PastCount != 102 {
		t.Fatalf("pastCount = %d, want 102", lists.PastCount)
	}
	g := lists.Upcoming[0]
	if g.ID != 198 || g.Title != "msnz Cover Stage zenith" || g.StartDate != "2026-07-27T01:00:00.000Z" {
		t.Fatalf("upcoming[0] = %d/%q/%q", g.ID, g.Title, g.StartDate)
	}
	if len(g.Polls) != 1 || g.Polls[0].ID != 239 {
		t.Fatalf("upcoming polls = %+v", g.Polls)
	}
}

// TestGravityDetailWirePayload checks a finished gravity decodes with its poll's
// ranked voteResults, the overall result, and the leaderboard, and that a
// finalized poll leaves the gravity in StateFinished.
func TestGravityDetailWirePayload(t *testing.T) {
	body := `{"gravity":{
	  "id": 188, "type": "event-gravity", "pollType": "single-poll", "artist": "tripleS",
	  "entireStartDate": "2026-06-11T01:00:00.000Z",
	  "entireEndDate": "2026-06-12T04:00:00.000Z",
	  "bannerImageUrl": "https://x/banner.webp",
	  "contractOutlink": "https://abscan.org/address/0xF1A7#readProxyContract",
	  "title": "Badge War 4 Theme Song",
	  "description": "Decide the title and concept",
	  "body": [
	    {"type":"heading","text":"Event Gravity","align":"center"},
	    {"type":"text","text":"Each unit pitched a concept.","align":"center"}
	  ],
	  "polls": [
	    {"id":229,"type":"single-poll","gravityId":188,"indexInGravity":1,
	     "finalized":true,"revealDate":"2026-06-12T04:00:00.000Z",
	     "result":{"totalComoUsed":141071,"voteResults":[
	       {"rank":1,"votedChoice":{"choiceName":"World Wild Women","choiceImageUrl":"https://x/1.webp","comoUsed":53169}},
	       {"rank":2,"votedChoice":{"choiceName":"Youth","choiceImageUrl":"https://x/2.webp","comoUsed":52335}}
	     ]}}
	  ],
	  "result": {"totalComoUsed":141071,"resultImageUrl":"https://x/r.webp","resultTitle":"World Wild Women"},
	  "leaderboard": {"userRanking":[
	    {"rank":1,"totalComoUsed":6575,"user":{"userId":27390,"nickname":"tripleskre","address":"0xFD3b"}},
	    {"rank":2,"totalComoUsed":5923,"user":{"userId":8987,"nickname":"QuackQuack","address":"0xc17C"}}
	  ]}
	}}`

	var wire struct {
		Gravity gravityWire `json:"gravity"`
	}
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	g := wire.Gravity.toGravity()
	if g.ID != 188 || g.State() != StateFinished {
		t.Fatalf("id/state = %d/%v, want 188/finished", g.ID, g.State())
	}
	if len(g.Body) != 2 || g.Body[0].Type != "heading" || g.Body[1].Text != "Each unit pitched a concept." {
		t.Fatalf("body = %+v", g.Body)
	}
	if len(g.Polls) != 1 || g.Polls[0].Result == nil {
		t.Fatalf("poll result missing: %+v", g.Polls)
	}
	pr := g.Polls[0].Result
	if pr.TotalComoUsed != 141071 || len(pr.Results) != 2 {
		t.Fatalf("poll result = %+v", pr)
	}
	if pr.Results[0].ChoiceName != "World Wild Women" || pr.Results[0].ComoUsed != 53169 {
		t.Fatalf("top result = %+v", pr.Results[0])
	}
	if g.Result == nil || g.Result.ResultTitle != "World Wild Women" {
		t.Fatalf("gravity result = %+v", g.Result)
	}
	if len(g.Leaderboard) != 2 || g.Leaderboard[0].Nickname != "tripleskre" || g.Leaderboard[0].ComoUsed != 6575 {
		t.Fatalf("leaderboard = %+v", g.Leaderboard)
	}
}

// TestPollDetailWirePayload checks the /v3/polls/{id} candidate list decodes and
// that txImageUrl maps onto Choice.ImageURL.
func TestPollDetailWirePayload(t *testing.T) {
	body := `{"pollDetail":{
	  "id":235,"type":"single-poll","gravityId":194,"indexInGravity":1,"finalized":false,
	  "startDate":"2026-07-24T01:00:00.000Z","endDate":"2026-07-25T01:00:00.000Z",
	  "revealDate":"2026-07-25T04:00:00.000Z",
	  "choices":[
	    {"id":"dejavu","txImageUrl":"https://x/d.webp","title":"Deja Vu","description":"TXT"},
	    {"id":"psycho","txImageUrl":"https://x/p.webp","title":"Psycho","description":"Red Velvet"}
	  ]
	}}`

	var wire struct {
		PollDetail pollDetailWire `json:"pollDetail"`
	}
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	p := wire.PollDetail.toPoll()
	if p.ID != 235 || len(p.Choices) != 2 {
		t.Fatalf("poll id/choices = %d/%d", p.ID, len(p.Choices))
	}
	if p.Choices[0].ID != "dejavu" || p.Choices[0].Title != "Deja Vu" || p.Choices[0].ImageURL != "https://x/d.webp" {
		t.Fatalf("first choice = %+v", p.Choices[0])
	}
	if p.Choices[1].Description != "Red Velvet" {
		t.Fatalf("second choice desc = %q", p.Choices[1].Description)
	}
}

// statusWire mirrors the anonymous struct GravityStatus decodes into, so the
// mapping can be exercised without an HTTP round trip.
type statusWire struct {
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

func (w statusWire) toStatus() GravityStatus {
	out := GravityStatus{Rank: w.Status.Rank, TotalComoUsed: w.Status.TotalComoUsed}
	for _, v := range w.Status.VoteStatuses {
		ps := PollVoteStatus{PollID: v.PollID, ComoUsed: v.ComoUsed}
		for _, b := range v.Votes {
			ps.Votes = append(ps.Votes, Vote{ChoiceID: b.ChoiceID, ChoiceName: b.VoteTo, ComoUsed: b.ComoUsed, At: b.At})
		}
		out.Votes = append(out.Votes, ps)
	}
	return out
}

// TestGravityStatusEmpty an un-revealed (or un-voted) poll reports zeros and no
// ballots. Cosmo returns this shape even for a vote already settled on-chain, so
// callers must not read it as "did not vote".
func TestGravityStatusEmpty(t *testing.T) {
	body := `{"status":{"rank":0,"totalComoUsed":0,"voteStatuses":[{"pollId":229,"comoUsed":0,"votes":[]}]}}`
	var wire statusWire
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := wire.toStatus()
	if len(got.Votes) != 1 || got.Votes[0].PollID != 229 {
		t.Fatalf("votes = %+v", got.Votes)
	}
	if got.Votes[0].ComoUsed != 0 || len(got.Votes[0].Votes) != 0 {
		t.Fatalf("expected an empty record, got %+v", got.Votes[0])
	}
}

// TestGravityStatusWithBallots a revealed gravity carries the user's rank and
// each ballot, naming the candidate via "voteTo". Real /gravities/184/status.
func TestGravityStatusWithBallots(t *testing.T) {
	body := `{"status":{"rank":3965,"totalComoUsed":10,"voteStatuses":[
	  {"pollId":225,"comoUsed":10,"votes":[
	    {"choiceId":"trustnoone","voteTo":"Trust No One",
	     "voteImageUrl":"https://static.cosmo.fans/admin/uploads/9266.png",
	     "comoUsed":10,"at":"2026-04-23T14:45:38.127Z"}]}]}}`
	var wire statusWire
	if err := json.Unmarshal([]byte(body), &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := wire.toStatus()
	if got.Rank != 3965 || got.TotalComoUsed != 10 {
		t.Fatalf("rank/total = %d/%d", got.Rank, got.TotalComoUsed)
	}
	if len(got.Votes) != 1 || len(got.Votes[0].Votes) != 1 {
		t.Fatalf("ballots = %+v", got.Votes)
	}
	v := got.Votes[0].Votes[0]
	if v.ChoiceID != "trustnoone" || v.ChoiceName != "Trust No One" || v.ComoUsed != 10 {
		t.Fatalf("ballot = %+v", v)
	}
	if v.At != "2026-04-23T14:45:38.127Z" {
		t.Fatalf("ballot time = %q", v.At)
	}
}

// TestPollContractParsing pulls the vote target out of the explorer link and
// distinguishes Abstract (votable) from the pre-migration Polygon gravities.
func TestPollContractParsing(t *testing.T) {
	abstract := Gravity{ContractOutlink: "https://abscan.org/address/0xF1A787da84af2A6e8227aD87112a21181B7b9b39#readProxyContract"}
	addr, onAbstract := abstract.PollContract()
	if addr != "0xF1A787da84af2A6e8227aD87112a21181B7b9b39" || !onAbstract {
		t.Fatalf("abstract link -> %q/%v", addr, onAbstract)
	}

	polygon := Gravity{ContractOutlink: "https://polygonscan.com/address/0xc3E5ad11aE2F00c740E74B81f134426A3331D950#readProxyContract"}
	addr, onAbstract = polygon.PollContract()
	if addr != "0xc3E5ad11aE2F00c740E74B81f134426A3331D950" || onAbstract {
		t.Fatalf("polygon link -> %q/%v, want the address but not on Abstract", addr, onAbstract)
	}

	if a, ok := (Gravity{ContractOutlink: "nonsense"}).PollContract(); a != "" || ok {
		t.Fatalf("unparseable link -> %q/%v", a, ok)
	}
}

// TestGravityStates checks the five states are told apart by the poll window
// and the result, not by Finalized: Finalized is false while a gravity is
// scheduled and while it is counting, and true for a good while before the
// result lands, so it reads a scheduled gravity as open and a counted one as
// finished. Only a Result retires a poll.
func TestGravityStates(t *testing.T) {
	now := time.Date(2026, 7, 25, 2, 0, 0, 0, time.UTC)
	// The real msnz shape: moon closed at 01:00 and reveals at 04:00, so at
	// 02:00 it is counting; sun opened at 01:00 and runs a full day.
	moonCounting := Poll{ID: 235, StartDate: "2026-07-24T01:00:00.000Z", EndDate: "2026-07-25T01:00:00.000Z", RevealDate: "2026-07-25T04:00:00.000Z"}
	// The same poll an hour on, once Cosmo flipped Finalized ahead of the
	// reveal: the tally is locked but nothing has been published.
	moonCounted := moonCounting
	moonCounted.Finalized = true
	sunOpen := Poll{ID: 237, StartDate: "2026-07-25T01:00:00.000Z", EndDate: "2026-07-26T01:00:00.000Z"}
	zenithScheduled := Poll{ID: 239, StartDate: "2026-07-27T01:00:00.000Z", EndDate: "2026-07-28T01:00:00.000Z"}
	settled := Poll{ID: 229, StartDate: "2026-07-20T01:00:00.000Z", EndDate: "2026-07-21T01:00:00.000Z",
		Finalized: true, Result: &PollResult{TotalComoUsed: 755316}}

	for _, tc := range []struct {
		name string
		g    Gravity
		want GravityState
	}{
		{"open", Gravity{Polls: []Poll{sunOpen}}, StateVoting},
		{"scheduled", Gravity{Polls: []Poll{zenithScheduled}}, StateUpcoming},
		{"closed, awaiting reveal", Gravity{Polls: []Poll{moonCounting}}, StateCounting},
		{"counted, awaiting reveal", Gravity{Polls: []Poll{moonCounted}}, StateCounted},
		{"finished", Gravity{Polls: []Poll{settled}}, StateFinished},
		{"no polls at all", Gravity{}, StateFinished},
		// A grand gravity: the most live day wins.
		{"day open, next scheduled", Gravity{Polls: []Poll{sunOpen, zenithScheduled}}, StateVoting},
		{"day counting, next open", Gravity{Polls: []Poll{moonCounting, sunOpen}}, StateVoting},
		{"day counting, next scheduled", Gravity{Polls: []Poll{moonCounting, zenithScheduled}}, StateCounting},
		{"day counted, next open", Gravity{Polls: []Poll{moonCounted, sunOpen}}, StateVoting},
		{"day counted, next counting", Gravity{Polls: []Poll{moonCounted, moonCounting}}, StateCounting},
		{"day counted, next scheduled", Gravity{Polls: []Poll{moonCounted, zenithScheduled}}, StateCounted},
		{"day settled, next scheduled", Gravity{Polls: []Poll{settled, zenithScheduled}}, StateUpcoming},
	} {
		if got := tc.g.stateAt(now); got != tc.want {
			t.Errorf("%s: state = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Neither bound may hide a vote that might be open when its date is unusable.
	if !(Poll{}).startedAt(now) || !(Poll{StartDate: "soon"}).startedAt(now) {
		t.Error("a poll with no parsable startDate should count as started")
	}
	if (Poll{}).endedAt(now) || (Poll{EndDate: "later"}).endedAt(now) {
		t.Error("a poll with no parsable endDate should not count as ended")
	}
}

// TestNotVotableOutsideWindow checks the vote path is shut in both directions:
// a ballot fabricated before a poll opens or after it closes would be rejected,
// and the closed half is not hypothetical - Cosmo leaves hours between the
// close and the reveal, during which the poll is still unfinalized.
func TestNotVotableOutsideWindow(t *testing.T) {
	abscan := "https://abscan.org/address/0xF1A787da84af2A6e8227aD87112a21181B7b9b39"
	iso := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }

	scheduled := Gravity{PollType: PollSingle, ContractOutlink: abscan,
		Polls: []Poll{{ID: 239, StartDate: iso(48 * time.Hour), EndDate: iso(72 * time.Hour)}}}
	counting := Gravity{PollType: PollSingle, ContractOutlink: abscan,
		Polls: []Poll{{ID: 235, StartDate: iso(-26 * time.Hour), EndDate: iso(-2 * time.Hour), RevealDate: iso(time.Hour)}}}
	// The same poll once Finalized flips, still an hour short of its reveal.
	counted := Gravity{PollType: PollSingle, ContractOutlink: abscan,
		Polls: []Poll{{ID: 235, StartDate: iso(-26 * time.Hour), EndDate: iso(-2 * time.Hour), RevealDate: iso(time.Hour), Finalized: true}}}
	open := Gravity{PollType: PollSingle, ContractOutlink: abscan,
		Polls: []Poll{{ID: 237, StartDate: iso(-time.Hour), EndDate: iso(23 * time.Hour)}}}

	if s := scheduled.State(); s != StateUpcoming {
		t.Fatalf("scheduled state = %v, want upcoming", s)
	}
	if s := counting.State(); s != StateCounting {
		t.Fatalf("counting state = %v, want counting", s)
	}
	if s := counted.State(); s != StateCounted {
		t.Fatalf("counted state = %v, want counted", s)
	}
	if s := open.State(); s != StateVoting {
		t.Fatalf("open state = %v, want voting", s)
	}

	for _, tc := range []struct {
		name string
		g    Gravity
	}{{"scheduled", scheduled}, {"counting", counting}, {"counted", counted}} {
		if tc.g.Votable() {
			t.Errorf("%s: Votable = true, want false", tc.name)
		}
		if _, ok := tc.g.OpenPoll(); ok {
			t.Errorf("%s: OpenPoll returned a poll outside its window", tc.name)
		}
	}
	if !open.Votable() {
		t.Error("an open single-poll Abstract gravity should still be votable")
	}
	if p, ok := open.OpenPoll(); !ok || p.ID != 237 {
		t.Errorf("OpenPoll = %d/%v, want 237/true", p.ID, ok)
	}

	if p, ok := scheduled.NextPoll(); !ok || p.ID != 239 {
		t.Errorf("NextPoll = %d/%v, want 239/true", p.ID, ok)
	}
	// CountingPoll covers both halves of the gap, since the reveal time the
	// callers want off it is the same either side of Finalized flipping.
	if p, ok := counting.CountingPoll(); !ok || p.ID != 235 {
		t.Errorf("CountingPoll = %d/%v, want 235/true", p.ID, ok)
	}
	if p, ok := counted.CountingPoll(); !ok || p.ID != 235 {
		t.Errorf("counted CountingPoll = %d/%v, want 235/true", p.ID, ok)
	}
	if _, ok := open.CountingPoll(); ok {
		t.Error("an open poll is not counting")
	}
	// A revealed poll is out of the gap entirely.
	revealed := Gravity{Polls: []Poll{{ID: 235, Finalized: true, Result: &PollResult{}}}}
	if _, ok := revealed.CountingPoll(); ok {
		t.Error("a revealed poll is not counting")
	}
}

// TestVotable only an open, single-choice, Abstract gravity can be voted on.

// TestNextPollEarliest checks the scheduled poll shown is the one that opens
// first, whatever order the API lists them in.
func TestNextPollEarliest(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	g := Gravity{Polls: []Poll{
		{ID: 9, StartDate: future.Add(48 * time.Hour).UTC().Format(time.RFC3339)},
		{ID: 7, StartDate: future.UTC().Format(time.RFC3339)},
		{ID: 8, StartDate: future.Add(24 * time.Hour).UTC().Format(time.RFC3339)},
	}}
	if p, ok := g.NextPoll(); !ok || p.ID != 7 {
		t.Fatalf("NextPoll = %d/%v, want 7/true", p.ID, ok)
	}
	if _, ok := (Gravity{Polls: []Poll{{ID: 1, Finalized: true}}}).NextPoll(); ok {
		t.Error("a finished gravity has no next poll")
	}
}

func TestVotable(t *testing.T) {
	abscan := "https://abscan.org/address/0xF1A787da84af2A6e8227aD87112a21181B7b9b39"
	open := []Poll{{ID: 235, Finalized: false}}

	if g := (Gravity{PollType: PollSingle, Polls: open, ContractOutlink: abscan}); !g.Votable() {
		t.Fatal("an open single-poll Abstract gravity should be votable")
	}
	if g := (Gravity{PollType: PollSingle, Polls: []Poll{{Finalized: true}}, ContractOutlink: abscan}); g.Votable() {
		t.Fatal("a finished gravity must not be votable")
	}
	if g := (Gravity{PollType: PollCombination, Polls: open, ContractOutlink: abscan}); g.Votable() {
		t.Fatal("a legacy combination poll must not be votable")
	}
	poly := "https://polygonscan.com/address/0xc3E5ad11aE2F00c740E74B81f134426A3331D950"
	if g := (Gravity{PollType: PollSingle, Polls: open, ContractOutlink: poly}); g.Votable() {
		t.Fatal("a Polygon-era gravity must not be votable")
	}
}

// TestOpenPoll finds the round currently accepting votes.
func TestOpenPoll(t *testing.T) {
	g := Gravity{Polls: []Poll{{ID: 1, Finalized: true}, {ID: 2, Finalized: false}}}
	p, ok := g.OpenPoll()
	if !ok || p.ID != 2 {
		t.Fatalf("open poll = %d/%v, want 2/true", p.ID, ok)
	}
	if _, ok := (Gravity{Polls: []Poll{{ID: 1, Finalized: true}}}).OpenPoll(); ok {
		t.Fatal("a fully finalized gravity has no open poll")
	}
}
