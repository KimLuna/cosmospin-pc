package gravity

import (
	"strings"
	"testing"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// sized builds a sized page (no client needed: tests feed messages directly and
// never run the returned network commands).
func sized(t *testing.T) Model {
	t.Helper()
	m := New(nil, "tripleS", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return updated.(Model)
}

func key(m Model, k string) Model {
	var msg tea.KeyPressMsg
	switch k {
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEsc}
	default:
		msg = tea.KeyPressMsg{Code: []rune(k)[0], Text: k}
	}
	updated, _ := m.Update(msg)
	return updated.(Model)
}

// TestEmptyCategoryKeepsHeader an empty category still shows which pane it is:
// the list's own title stays on screen with the notice underneath it, so an
// artist with nothing running does not get a blank right-hand pane.
func TestEmptyCategoryKeepsHeader(t *testing.T) {
	m := sized(t)
	updated, _ := m.Update(ongoingLoadedMsg{gravities: nil})
	m = updated.(Model)
	out := m.render()
	if !strings.Contains(out, "No gravities are ongoing right now.") {
		t.Fatalf("expected empty-ongoing notice, got:\n%s", out)
	}
	// The title appears twice: the sidebar row and the list's own header.
	if n := strings.Count(out, categoryOngoing.name()); n < 2 {
		t.Fatalf("Ongoing header missing from the list pane (%d occurrences):\n%s", n, out)
	}

	m = key(m, "j") // switch to Past
	updated, _ = m.Update(pastLoadedMsg{gravities: nil})
	m = updated.(Model)
	out = m.render()
	if !strings.Contains(out, "No past gravities.") {
		t.Fatalf("expected empty-past notice, got:\n%s", out)
	}
	if n := strings.Count(out, categoryPast.name()); n < 2 {
		t.Fatalf("Past header missing from the list pane (%d occurrences):\n%s", n, out)
	}
}

// TestCategorySwitchLoadsPast moving the category cursor to Past switches the
// active category and asks for a load (the Past list has not been fetched yet).
func TestCategorySwitchLoadsPast(t *testing.T) {
	m := sized(t)
	if m.category != categoryOngoing {
		t.Fatalf("category = %d, want ongoing", m.category)
	}
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = updated.(Model)
	if m.category != categoryPast {
		t.Fatalf("after j, category = %d, want past", m.category)
	}
	if cmd == nil {
		t.Fatal("expected a load command for the newly shown Past category")
	}
	if !m.loading {
		t.Fatal("expected loading state while Past loads")
	}
}

// TestPastListPopulates feeds a past-gravities load and checks a row renders
// with its winner. The poll carries a Result as well as Finalized because that
// is what a past gravity actually looks like - across the whole tripleS history
// the two always arrive together, and a poll finalized without one means the
// reveal is still pending (see TestCountedAwaitingReveal).
func TestPastListPopulates(t *testing.T) {
	m := sized(t)
	m = key(m, "j") // switch to Past
	updated, _ := m.Update(pastLoadedMsg{gravities: []cosmo.Gravity{
		{
			ID: 188, Type: cosmo.GravityEvent, Title: "Badge War 4",
			EndDate: "2026-06-12T04:00:00.000Z",
			Polls: []cosmo.Poll{{ID: 229, Finalized: true,
				Result: &cosmo.PollResult{TotalComoUsed: 755316}}},
			Result: &cosmo.GravityResult{ResultTitle: "World Wild Women"},
		},
	}})
	m = updated.(Model)
	out := m.render()
	if !strings.Contains(out, "Badge War 4") {
		t.Fatalf("past list missing title:\n%s", out)
	}
	if !strings.Contains(out, "World Wild Women") {
		t.Fatalf("past row missing winner:\n%s", out)
	}
}

// TestDetailRendersResults checks a finished gravity's detail shows its ranked
// results and COMO totals (with thousands separators), then the leaderboard.
func TestDetailRendersResults(t *testing.T) {
	m := sized(t)
	g := cosmo.Gravity{
		ID: 188, Type: cosmo.GravityEvent, Title: "Badge War 4",
		Description: "Decide the theme song.",
		StartDate:   "2026-06-11T01:00:00.000Z",
		EndDate:     "2026-06-12T04:00:00.000Z",
		Body:        []cosmo.BodyBlock{{Type: "heading", Text: "Event Gravity"}},
		Polls: []cosmo.Poll{{
			ID: 229, Finalized: true,
			Result: &cosmo.PollResult{
				TotalComoUsed: 141071,
				Results: []cosmo.VoteResult{
					{Rank: 1, ChoiceName: "World Wild Women", ComoUsed: 53169},
					{Rank: 2, ChoiceName: "Youth", ComoUsed: 52335},
				},
			},
		}},
		Leaderboard: []cosmo.LeaderboardEntry{{Rank: 1, Nickname: "tripleskre", ComoUsed: 6575}},
	}
	out := m.renderDetail(g, cosmo.GravityStatus{})
	for _, want := range []string{
		"Badge War 4", "Event Gravity", "Results", "1. World Wild Women", "53,169",
		"total: 141,071 COMO", "Leaderboard", "tripleskre", "6,575",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q:\n%s", want, out)
		}
	}
}

// TestLeaderboardOnlyWhenPresent an unrevealed gravity has no leaderboard, and
// the section must not appear as an empty heading.
func TestLeaderboardOnlyWhenPresent(t *testing.T) {
	m := sized(t)
	g := cosmo.Gravity{ID: 194, Title: "msnz Cover Stage moon",
		Polls: []cosmo.Poll{{ID: 235, Choices: []cosmo.Choice{{ID: "dejavu", Title: "Deja Vu"}}}}}
	if out := m.renderDetail(g, cosmo.GravityStatus{}); strings.Contains(out, "Leaderboard") {
		t.Fatalf("no leaderboard yet, so no section:\n%s", out)
	}
}

// TestLeaderboardMarksOwnRow checks the columns line up under nicknames of
// different widths and that the user's own row is the one marked. The entries
// are gravity 194's real top three; the "you" row is spliced in at a rank the
// user could plausibly hold, since their real spend put them at 3912.
func TestLeaderboardMarksOwnRow(t *testing.T) {
	entries := []cosmo.LeaderboardEntry{
		{Rank: 1, Nickname: "magpieS", ComoUsed: 9001},
		{Rank: 2, Nickname: "WarmMeUp", ComoUsed: 8000},
		{Rank: 3, Nickname: "Simioun", ComoUsed: 4000},
		{Rank: 10, Nickname: "chuu", ComoUsed: 1917},
	}
	status := cosmo.GravityStatus{Rank: 10, TotalComoUsed: 1917}
	out := leaderboardSection(entries, status, 60)
	lines := strings.Split(out, "\n")
	if len(lines) != 5 { // heading + four rows
		t.Fatalf("want a heading and four rows, got %d lines:\n%s", len(lines), out)
	}
	// The COMO column is right-aligned, so every row ends at the same column.
	width := lipgloss.Width(lines[1])
	for _, l := range lines[1:] {
		if w := lipgloss.Width(l); w != width {
			t.Errorf("row %q width %d, want %d - columns are ragged", l, w, width)
		}
	}
	// Rank 10 pushes the rank column a character wider; the single-digit ranks
	// pad to match rather than shifting their names left.
	if !strings.Contains(out, "   1. magpieS ") {
		t.Errorf("rank column not padded to the widest rank:\n%s", out)
	}
	// Every row carries styling (the COMO column), so "has escapes" proves
	// nothing. Rendering the same entries for a stranger must change the user's
	// row and leave the other three untouched.
	plain := strings.Split(leaderboardSection(entries, cosmo.GravityStatus{}, 60), "\n")
	if lines[4] == plain[4] {
		t.Errorf("the user's own row is not marked: %q", lines[4])
	}
	for i := range plain[:4] {
		if lines[i] != plain[i] {
			t.Errorf("row %d changed for a status that is not theirs:\n%q\n%q", i, lines[i], plain[i])
		}
	}
	// A tie on rank alone must not claim a stranger's row.
	tied := cosmo.GravityStatus{Rank: 10, TotalComoUsed: 42}
	if strings.Split(leaderboardSection(entries, tied, 60), "\n")[4] != plain[4] {
		t.Error("a rank match with a different spend must not be marked as the user's")
	}
}

// TestDetailRendersCandidates checks an ongoing gravity's detail lists candidates
// and reflects the user's own COMO spend from their status record.
func TestDetailRendersCandidates(t *testing.T) {
	m := sized(t)
	g := cosmo.Gravity{
		ID: 194, Type: cosmo.GravityEvent, Title: "msnz Cover Stage",
		StartDate: "2026-07-24T01:00:00.000Z",
		EndDate:   "2026-07-25T04:00:00.000Z",
		Polls: []cosmo.Poll{{
			ID: 235, Finalized: false,
			Choices: []cosmo.Choice{
				{ID: "dejavu", Title: "Deja Vu", Description: "TXT"},
				{ID: "psycho", Title: "Psycho", Description: "Red Velvet"},
			},
		}},
	}
	status := cosmo.GravityStatus{Votes: []cosmo.PollVoteStatus{{PollID: 235, ComoUsed: 1}}}
	out := m.renderDetail(g, status)
	for _, want := range []string{"voting open", "Candidates", "Deja Vu", "Psycho", "you spent 1 COMO here"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q:\n%s", want, out)
		}
	}
}

// TestCountedAwaitingReveal is the moon gravity in the gap between its tally
// and its announcement: poll 235 finalized around 03:00 on 2026-07-25 with no
// result attached and its reveal still an hour out, while Cosmo kept the
// gravity in the ongoing list. The page has to say the counting is done without
// claiming a winner, and must still show the candidates - /polls/235 keeps
// serving them, so an empty section here would be the client's own doing.
func TestCountedAwaitingReveal(t *testing.T) {
	m := sized(t)
	g := cosmo.Gravity{
		ID: 194, Type: cosmo.GravityEvent, Title: "msnz Cover Stage moon",
		StartDate: "2026-07-24T01:00:00.000Z",
		EndDate:   "2026-07-25T04:00:00.000Z",
		Polls: []cosmo.Poll{{
			ID: 235, Finalized: true, Result: nil,
			StartDate:  "2026-07-24T01:00:00.000Z",
			EndDate:    "2026-07-25T01:00:00.000Z",
			RevealDate: "2026-07-25T04:00:00.000Z",
			Choices: []cosmo.Choice{
				{ID: "dejavu", Title: "Deja Vu", Description: "TXT"},
				{ID: "psycho", Title: "Psycho", Description: "Red Velvet"},
			},
		}},
	}
	status := cosmo.GravityStatus{Rank: 3912, TotalComoUsed: 2, Votes: []cosmo.PollVoteStatus{{
		PollID: 235, ComoUsed: 2,
		Votes: []cosmo.Vote{{ChoiceID: "dejavu", ChoiceName: "Deja Vu", ComoUsed: 2, At: "2026-07-24T15:02:30.972Z"}},
	}}}
	out := m.renderDetail(g, status)
	for _, want := range []string{
		"votes counted", "Candidates (votes counted)", "Deja Vu", "Psycho", "your vote: Deja Vu",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q:\n%s", want, out)
		}
	}
	// Nothing has been announced, so nothing may read as a ranking or a result.
	if strings.Contains(out, "Results") || strings.Contains(out, "candidates unavailable") {
		t.Fatalf("a counted-but-unrevealed poll must not show results or an empty list:\n%s", out)
	}
	if g.Votable() {
		t.Fatal("a gravity past its voting window must not offer a ballot")
	}

	// And the list row it renders from, which is what the ongoing list shows.
	if d := (gravityItem{g}).Description(); !strings.HasPrefix(d, "votes counted · results ") {
		t.Fatalf("ongoing row = %q, want a votes-counted row with the reveal time", d)
	}
}

// TestZeroSpendMakesNoClaim a zero from /gravities/{id}/status must not be
// rendered as "you have not voted": Cosmo reports 0 for a poll voted in but not
// yet revealed, so the page stays silent rather than assert something false.
func TestZeroSpendMakesNoClaim(t *testing.T) {
	m := sized(t)
	g := cosmo.Gravity{
		ID: 194, Title: "msnz Cover Stage",
		Polls: []cosmo.Poll{{
			ID:      235,
			Choices: []cosmo.Choice{{ID: "dejavu", Title: "Deja Vu"}},
		}},
	}
	// The shape Cosmo actually returns for a voted-but-unrevealed poll.
	status := cosmo.GravityStatus{Votes: []cosmo.PollVoteStatus{{PollID: 235, ComoUsed: 0}}}
	out := m.renderDetail(g, status)
	if strings.Contains(strings.ToLower(out), "not voted") {
		t.Fatalf("must not claim the user has not voted:\n%s", out)
	}
	if strings.Contains(out, "you have spent") {
		t.Fatalf("must not report a spend of zero:\n%s", out)
	}
	if !strings.Contains(out, "Deja Vu") {
		t.Fatalf("candidates should still render:\n%s", out)
	}
}

// TestPastVoteHistory a finished gravity shows the user's own history: their
// overall rank, the candidate they backed marked in the results, and the ballot
// with its COMO cost. Mirrors the real /gravities/184/status payload.
func TestPastVoteHistory(t *testing.T) {
	m := sized(t)
	g := cosmo.Gravity{
		ID: 184, Title: "Reality Show Gravity",
		EndDate: "2026-04-24T04:00:00.000Z",
		Polls: []cosmo.Poll{{
			ID: 225, Finalized: true,
			Result: &cosmo.PollResult{
				TotalComoUsed: 496143,
				Results: []cosmo.VoteResult{
					{Rank: 1, ChoiceName: "Trust No One", ComoUsed: 385373},
					{Rank: 2, ChoiceName: "ALL YES, tripleS", ComoUsed: 110770},
				},
			},
		}},
	}
	status := cosmo.GravityStatus{
		Rank: 3965, TotalComoUsed: 10,
		Votes: []cosmo.PollVoteStatus{{
			PollID: 225, ComoUsed: 10,
			Votes: []cosmo.Vote{{
				ChoiceID: "trustnoone", ChoiceName: "Trust No One",
				ComoUsed: 10, At: "2026-04-23T14:45:38.127Z",
			}},
		}},
	}
	out := m.renderDetail(g, status)
	for _, want := range []string{"your rank: #3965", "10 COMO spent", "1. Trust No One", "your vote: Trust No One (10 COMO)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q:\n%s", want, out)
		}
	}
	// Ballots are listed on their own; candidates carry no per-row marker, since
	// repeat voting can spread a user's votes over several candidates.
	if strings.Contains(out, "←") {
		t.Fatalf("results should carry no vote marker:\n%s", out)
	}
}

// TestAllBallotsListed a user may vote repeatedly in one poll, including for
// different candidates; every ballot in the record must be shown, not just the
// first or an aggregate.
func TestAllBallotsListed(t *testing.T) {
	m := sized(t)
	g := cosmo.Gravity{
		ID: 184, Title: "Reality Show Gravity",
		Polls: []cosmo.Poll{{
			ID: 225, Finalized: true,
			Result: &cosmo.PollResult{
				TotalComoUsed: 496143,
				Results: []cosmo.VoteResult{
					{Rank: 1, ChoiceName: "Trust No One", ComoUsed: 385373},
					{Rank: 2, ChoiceName: "ALL YES, tripleS", ComoUsed: 110770},
				},
			},
		}},
	}
	status := cosmo.GravityStatus{
		Rank: 12, TotalComoUsed: 25,
		Votes: []cosmo.PollVoteStatus{{
			PollID: 225, ComoUsed: 25,
			Votes: []cosmo.Vote{
				{ChoiceName: "Trust No One", ComoUsed: 10, At: "2026-04-23T14:45:38.127Z"},
				{ChoiceName: "ALL YES, tripleS", ComoUsed: 5, At: "2026-04-23T15:01:00.000Z"},
				{ChoiceName: "Trust No One", ComoUsed: 10, At: "2026-04-23T16:20:00.000Z"},
			},
		}},
	}
	out := m.renderDetail(g, status)
	if got := strings.Count(out, "your vote:"); got != 3 {
		t.Fatalf("listed %d ballots, want 3:\n%s", got, out)
	}
	for _, want := range []string{"your vote: Trust No One (10 COMO)", "your vote: ALL YES, tripleS (5 COMO)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q:\n%s", want, out)
		}
	}
}

// TestStatusSkippedWhileUnrevealed a gravity with no finalized poll must not
// spend a request on /status: it can only come back empty (see loadDetail).
func TestStatusSkippedWhileUnrevealed(t *testing.T) {
	open := cosmo.Gravity{Polls: []cosmo.Poll{{ID: 235, Finalized: false}}}
	if anyFinalized(open) {
		t.Fatal("an open gravity should not trigger a status fetch")
	}
	done := cosmo.Gravity{Polls: []cosmo.Poll{{ID: 229, Finalized: true}}}
	if !anyFinalized(done) {
		t.Fatal("a finished gravity should trigger a status fetch")
	}
	// A mixed multi-poll event still fetches, for the round already revealed.
	mixed := cosmo.Gravity{Polls: []cosmo.Poll{{ID: 1, Finalized: true}, {ID: 2, Finalized: false}}}
	if !anyFinalized(mixed) {
		t.Fatal("a mixed gravity should still fetch its revealed history")
	}
}

// TestDetailDropsMediaBlocks image and video blocks carry only the app's own
// layout graphics, so neither a label nor its URL belongs in the detail view.
func TestDetailDropsMediaBlocks(t *testing.T) {
	m := sized(t)
	g := cosmo.Gravity{
		ID: 188, Title: "Badge War 4",
		ContractOutlink: "https://abscan.org/address/0xF1A7#readProxyContract",
		Body: []cosmo.BodyBlock{
			{Type: "heading", Text: "Event Gravity"},
			{Type: "image", ImageURL: "https://resources.cosmo.fans/x.webp"},
			{Type: "video", VideoURL: "https://customer-x.cloudflarestream.com/v.m3u8"},
			{Type: "spacing", Height: 24},
			{Type: "text", Text: "Pick a concept."},
		},
	}
	out := m.renderDetail(g, cosmo.GravityStatus{})
	for _, unwanted := range []string{"[image]", "[video]", "cosmo.fans/x.webp", "cloudflarestream", "on-chain", "abscan.org"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("detail should not contain %q:\n%s", unwanted, out)
		}
	}
	// The real text still renders.
	for _, want := range []string{"Event Gravity", "Pick a concept."} {
		if !strings.Contains(out, want) {
			t.Fatalf("detail missing %q:\n%s", want, out)
		}
	}
}

// TestOngoingShowsCloseTime an open vote shows the wall-clock close (from the
// poll's own end, not the gravity's later reveal time), in both the row and the
// detail meta.
func TestOngoingShowsCloseTime(t *testing.T) {
	m := sized(t)
	// The window is relative to now so the vote is genuinely open: the state is
	// judged against the wall clock, so fixed dates would age into the counting
	// state and stop testing what this test is about.
	iso := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }
	closes, reveal := iso(3*time.Hour), iso(6*time.Hour)
	g := cosmo.Gravity{
		ID: 194, Title: "msnz Cover Stage",
		StartDate: iso(-21 * time.Hour),
		EndDate:   reveal, // gravity end == reveal
		Polls: []cosmo.Poll{{
			ID: 235, Finalized: false,
			StartDate: iso(-21 * time.Hour),
			EndDate:   closes, // voting really closes here
			Choices:   []cosmo.Choice{{ID: "dejavu", Title: "Deja Vu"}},
		}},
	}
	wantTime := cosmo.LocalDateTime(closes)
	if got := (gravityItem{g: g}).Description(); !strings.Contains(got, wantTime) {
		t.Fatalf("row description = %q, want it to contain %q", got, wantTime)
	}
	out := m.renderDetail(g, cosmo.GravityStatus{})
	if !strings.Contains(out, wantTime) {
		t.Fatalf("detail meta missing close time %q:\n%s", wantTime, out)
	}
	// The gravity's own (later) reveal time must not be what we advertise.
	if r := cosmo.LocalDateTime(reveal); strings.Contains(out, r) {
		t.Fatalf("detail should show the voting close, not the reveal time %q:\n%s", r, out)
	}
}

// TestDetailCandidatesUnavailable a non-finalized poll whose candidate list did
// not load degrades to a notice rather than rendering nothing.
func TestDetailCandidatesUnavailable(t *testing.T) {
	m := sized(t)
	g := cosmo.Gravity{
		ID: 10, Type: cosmo.GravityGrand, PollType: cosmo.PollCombination,
		Title: "Two Big WAVes",
		Polls: []cosmo.Poll{{ID: 1, Finalized: false}}, // combination poll, no choices mapped
	}
	out := m.renderDetail(g, cosmo.GravityStatus{})
	if !strings.Contains(out, "candidates unavailable") {
		t.Fatalf("expected unavailable notice:\n%s", out)
	}
}

// TestMergeOngoing checks the Ongoing category lists the live vote first and
// then the scheduled ones in the order they open, not the newest-first order
// the API returns them in.
func TestMergeOngoing(t *testing.T) {
	lists := cosmo.GravityLists{
		Ongoing: []cosmo.Gravity{{ID: 194, StartDate: "2026-07-24T01:00:00.000Z"}},
		Upcoming: []cosmo.Gravity{
			{ID: 198, StartDate: "2026-07-27T01:00:00.000Z"},
			{ID: 197, StartDate: "2026-07-26T01:00:00.000Z"},
			{ID: 196, StartDate: "2026-07-25T01:00:00.000Z"},
		},
	}
	got := mergeOngoing(lists)
	want := []int64{194, 196, 197, 198}
	if len(got) != len(want) {
		t.Fatalf("merged len = %d, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("merged[%d].ID = %d, want %d (got %v)", i, got[i].ID, id, ids(got))
		}
	}
	// The caller's slices must not be reordered under it.
	if lists.Upcoming[0].ID != 198 {
		t.Fatalf("mergeOngoing sorted the caller's slice in place")
	}
}

func ids(gs []cosmo.Gravity) []int64 {
	out := make([]int64, len(gs))
	for i, g := range gs {
		out[i] = g.ID
	}
	return out
}

// TestUpcomingListed checks a scheduled gravity reaches the Ongoing pane - the
// case that was invisible while the page fetched category=ongoing, whose
// upcoming[] is always empty.
func TestUpcomingListed(t *testing.T) {
	m := sized(t)
	updated, _ := m.Update(ongoingLoadedMsg{gravities: mergeOngoing(cosmo.GravityLists{
		Ongoing:  []cosmo.Gravity{{ID: 194, Title: "moon", StartDate: "2026-07-24T01:00:00.000Z"}},
		Upcoming: []cosmo.Gravity{{ID: 196, Title: "sun", StartDate: "2026-07-25T01:00:00.000Z"}},
	})})
	m = updated.(Model)
	out := m.render()
	for _, want := range []string{"moon", "sun"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ongoing pane missing %q:\n%s", want, out)
		}
	}
	// The entry pane's count (shown once it has focus) covers both rows.
	if m.status != "2 gravity(s)" {
		t.Fatalf("status = %q, want %q", m.status, "2 gravity(s)")
	}
}

// TestUpcomingLabelledUpcoming checks a scheduled gravity is not dressed up as
// an open vote: the row and the detail header say when voting opens, and the
// detail offers no vote key.
func TestUpcomingLabelledUpcoming(t *testing.T) {
	opens := time.Now().Add(48 * time.Hour)
	g := cosmo.Gravity{
		ID: 198, Type: cosmo.GravityEvent, PollType: cosmo.PollSingle,
		Artist: "tripleS", Title: "msnz Cover Stage zenith",
		StartDate:       opens.UTC().Format(time.RFC3339),
		EndDate:         opens.Add(27 * time.Hour).UTC().Format(time.RFC3339),
		ContractOutlink: "https://abscan.org/address/0xF1A7#readProxyContract",
		Polls: []cosmo.Poll{{
			ID:        239,
			StartDate: opens.UTC().Format(time.RFC3339),
			EndDate:   opens.Add(24 * time.Hour).UTC().Format(time.RFC3339),
		}},
	}

	want := "opens " + cosmo.LocalDateTime(g.Polls[0].StartDate)
	if desc := (gravityItem{g: g}).Description(); !strings.HasPrefix(desc, "upcoming · "+want) {
		t.Errorf("row description = %q, want it to lead with %q", desc, "upcoming · "+want)
	}
	if meta := gravityMeta(g); !strings.Contains(meta, "upcoming ·") || !strings.Contains(meta, "voting "+want) {
		t.Errorf("detail meta = %q, want an upcoming label and the open time", meta)
	}

	// The vote key must be withheld even with a wallet present: canVote leans on
	// Votable, which is now false until the poll opens.
	m := sized(t)
	m.wallet = &wallet.Wallet{}
	m.setFocus(focusDetail)
	updated, _ := m.Update(detailMsg{gravity: g})
	m = updated.(Model)
	if m.canVote() {
		t.Error("canVote = true for a gravity whose poll has not opened")
	}
	if strings.Contains(m.detailView(), "v: vote") {
		t.Errorf("detail offers the vote key before voting opens:\n%s", m.detailView())
	}
}

// TestCountingLabelled checks the gap between a poll closing and its result
// being revealed - hours long, and the poll stays unfinalized throughout - is
// shown as its own state and offers no vote key. Before this the page called it
// "voting open" and let the flow start on a poll that would refuse the ballot.
func TestCountingLabelled(t *testing.T) {
	iso := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }
	closed, reveal := iso(-2*time.Hour), iso(time.Hour)
	g := cosmo.Gravity{
		ID: 194, Type: cosmo.GravityEvent, PollType: cosmo.PollSingle,
		Artist: "tripleS", Title: "msnz Cover Stage moon",
		StartDate:       iso(-26 * time.Hour),
		EndDate:         reveal,
		ContractOutlink: "https://abscan.org/address/0xF1A7#readProxyContract",
		Polls: []cosmo.Poll{{
			ID: 235, StartDate: iso(-26 * time.Hour), EndDate: closed, RevealDate: reveal,
			Choices: []cosmo.Choice{{ID: "dejavu", Title: "Deja Vu"}},
		}},
	}

	want := cosmo.LocalDateTime(reveal)
	if desc := (gravityItem{g: g}).Description(); !strings.HasPrefix(desc, "counting votes · results "+want) {
		t.Errorf("row description = %q, want it to lead with %q", desc, "counting votes · results "+want)
	}
	if meta := gravityMeta(g); !strings.Contains(meta, "counting votes ·") || !strings.Contains(meta, "results "+want) {
		t.Errorf("detail meta = %q, want a counting label and the reveal time", meta)
	}

	m := sized(t)
	m.wallet = &wallet.Wallet{}
	m.setFocus(focusDetail)
	updated, _ := m.Update(detailMsg{gravity: g})
	m = updated.(Model)
	if m.canVote() {
		t.Error("canVote = true while the votes are being counted")
	}
	view := m.detailView()
	if strings.Contains(view, "v: vote") {
		t.Errorf("detail offers the vote key after voting closed:\n%s", view)
	}
	// The candidates still show (they are all there is until the reveal), but
	// not as something to vote on.
	if !strings.Contains(view, "Candidates (voting closed)") || !strings.Contains(view, "Deja Vu") {
		t.Errorf("detail should list the candidates as closed:\n%s", view)
	}

	// And the flow itself refuses, in case the window closes under an open detail.
	started, _ := m.startVote()
	sm := started.(Model)
	if sm.vote.phase != voteDone || sm.vote.err == nil {
		t.Fatalf("startVote phase/err = %v/%v, want done with an error", sm.vote.phase, sm.vote.err)
	}
	if !strings.Contains(sm.vote.err.Error(), "voting is not open") {
		t.Errorf("startVote error = %q, want it to say voting is not open", sm.vote.err)
	}
}

// TestApolloURL pins the apollo.cafe link scheme: the artist slug and the same
// numeric gravity id Cosmo uses. The page's group stands in when a gravity
// record carries no artist of its own.
func TestApolloURL(t *testing.T) {
	g := cosmo.Gravity{ID: 194, Artist: "tripleS"}
	if got, want := apolloURL(g, "artms"), "https://apollo.cafe/gravity/tripleS/194"; got != want {
		t.Errorf("apolloURL = %q, want %q", got, want)
	}
	if got, want := apolloURL(cosmo.Gravity{ID: 190}, "idntt"), "https://apollo.cafe/gravity/idntt/190"; got != want {
		t.Errorf("apolloURL fallback = %q, want %q", got, want)
	}
}

// TestDetailHintsApollo checks the detail view advertises the key, and only
// once a gravity is actually open (nothing to link to while it loads).
func TestDetailHintsApollo(t *testing.T) {
	m := sized(t)
	m.setFocus(focusDetail)
	if strings.Contains(m.detailView(), "apollo.cafe") {
		t.Fatalf("hint shown before a gravity loaded:\n%s", m.detailView())
	}
	updated, _ := m.Update(detailMsg{gravity: cosmo.Gravity{ID: 194, Artist: "tripleS", Title: "msnz Cover Stage"}})
	m = updated.(Model)
	if !strings.Contains(m.detailView(), "a: apollo.cafe") {
		t.Fatalf("detail hint missing the apollo key:\n%s", m.detailView())
	}
}

func TestFmtComo(t *testing.T) {
	cases := map[int64]string{0: "0", 5: "5", 999: "999", 1000: "1,000", 53169: "53,169", 141071: "141,071", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := fmtComo(in); got != want {
			t.Errorf("fmtComo(%d) = %q, want %q", in, got, want)
		}
	}
}
