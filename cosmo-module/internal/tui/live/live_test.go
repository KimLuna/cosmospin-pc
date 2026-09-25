package live

import (
	"errors"
	"strings"
	"testing"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"

	tea "charm.land/bubbletea/v2"
)

// newSized builds a replay model with a window size applied.
func newSized(t *testing.T) Model {
	t.Helper()
	m := New(nil, "tripleS", ".", 0)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return updated.(Model)
}

// keyRune feeds one rune keystroke through Update.
func keyRune(t *testing.T, m Model, r rune) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	return updated.(Model), cmd
}

// memberAt returns the member behind the i-th row of the member list
// (row 0 is "All").
func memberAt(m Model, i int) int {
	return m.memberList.Items()[i].(memberItem).member.APIID
}

// someVods builds an n-item replay list streamed by member.
func someVods(n int, member string) []cosmo.Vod {
	vods := make([]cosmo.Vod, n)
	for i := range vods {
		vods[i] = cosmo.Vod{VideoID: "v", Title: "live", Member: member,
			Date: "2026-07-08", Playable: true}
	}
	return vods
}

// TestInitLoadsGroup checks that the page fetches the whole group's replays at
// startup, shows the All list once loaded, and that a group switch refetches
// while a switch back restores from cache.
func TestInitLoadsGroup(t *testing.T) {
	m := newSized(t)
	if m.Init() == nil {
		t.Fatal("Init should fetch the group's replays")
	}

	buckets := map[int][]cosmo.Vod{
		cosmo.AllMembersID: someVods(3, "SeoYeon"),
		memberAt(m, 1):     someVods(2, "SeoYeon"),
	}
	updated, _ := m.Update(vodsLoadedMsg{group: "tripleS", vods: buckets})
	m = updated.(Model)
	if m.loading || len(m.vodList.Items()) != 3 {
		t.Fatalf("loading=%v items=%d, want the 3-item All list", m.loading, len(m.vodList.Items()))
	}
	if m.vodList.Title != "Replays - All" {
		t.Fatalf("title = %q, want the All list", m.vodList.Title)
	}

	// A switch to an uncached group refetches; back to the cached one restores.
	updated, cmd := m.Update(uimsg.GroupChanged{Group: "artms"})
	m = updated.(Model)
	if cmd == nil || !m.loading {
		t.Fatal("a switch to an uncached group should fetch")
	}
	// A cached group restores its replays without refetching them (loading
	// stays clear), though live status is always refetched.
	updated, cmd = m.Update(uimsg.GroupChanged{Group: "tripleS"})
	m = updated.(Model)
	if m.loading {
		t.Fatal("a cached group should restore without refetching replays")
	}
	if cmd == nil {
		t.Fatal("a group switch should refetch live status")
	}
	if len(m.vodList.Items()) != 3 {
		t.Fatalf("cached All list not restored: %d items", len(m.vodList.Items()))
	}
}

// titleAt returns the rendered title of the i-th member row.
func titleAt(m Model, i int) string {
	return m.memberList.Items()[i].(memberItem).Title()
}

// TestLiveMemberMarked checks that a streaming member's row gains the marker,
// that quiet members and the All bucket don't, and that filtering still matches
// the bare name.
func TestLiveMemberMarked(t *testing.T) {
	m := newSized(t)
	live, quiet := memberAt(m, 1), memberAt(m, 2)

	updated, _ := m.Update(liveSessionsLoadedMsg{group: "tripleS", ids: map[int]bool{live: true}})
	m = updated.(Model)

	if !strings.Contains(titleAt(m, 1), "🔴") {
		t.Errorf("live member row = %q, want a marker", titleAt(m, 1))
	}
	if strings.Contains(titleAt(m, 2), "🔴") {
		t.Errorf("quiet member %d row = %q, want no marker", quiet, titleAt(m, 2))
	}
	if strings.Contains(titleAt(m, 0), "🔴") {
		t.Errorf("All row = %q, want no marker", titleAt(m, 0))
	}
	// The marker is display-only: filtering matches the bare name.
	item := m.memberList.Items()[1].(memberItem)
	if item.FilterValue() != item.member.Name {
		t.Errorf("FilterValue = %q, want the bare name %q", item.FilterValue(), item.member.Name)
	}
}

// TestLiveSessionsKeepCursor checks that a marker landing doesn't move the
// selection out from under the user.
func TestLiveSessionsKeepCursor(t *testing.T) {
	m := newSized(t)
	m.memberList.Select(3)

	updated, _ := m.Update(liveSessionsLoadedMsg{group: "tripleS",
		ids: map[int]bool{memberAt(m, 1): true}})
	m = updated.(Model)

	if m.memberList.Index() != 3 {
		t.Fatalf("cursor = %d, want it held at 3", m.memberList.Index())
	}
}

// TestLiveSessionsDuringFilter checks that live status landing while a member
// filter is applied doesn't blank the sidebar: rebuilding the rows drops the
// list's computed matches, and the swap has to re-run the filter in place
// (listfilter.Resync) — its FilterMatchesMsg can't be routed back once the
// filter is applied rather than being typed.
func TestLiveSessionsDuringFilter(t *testing.T) {
	m := newSized(t)
	m.memberList.SetFilterText("seoyeon")
	if n := len(m.memberList.VisibleItems()); n != 1 {
		t.Fatalf("visible items = %d, want the 1 match", n)
	}

	updated, _ := m.Update(liveSessionsLoadedMsg{group: "tripleS",
		ids: map[int]bool{memberAt(m, 1): true}})
	m = updated.(Model)

	vis := m.memberList.VisibleItems()
	if len(vis) != 1 {
		t.Fatalf("visible items after rebuild = %d, want the filter's 1 match kept", len(vis))
	}
	if got := vis[0].(memberItem).Title(); !strings.Contains(got, "🔴") {
		t.Fatalf("filtered row = %q, want the live marker", got)
	}
}

// TestStaleLiveSessionsDropped checks that live status landing for a group
// we've since left doesn't mark the new group's roster.
func TestStaleLiveSessionsDropped(t *testing.T) {
	m := newSized(t)
	updated, _ := m.Update(uimsg.GroupChanged{Group: "artms"})
	m = updated.(Model)

	updated, _ = m.Update(liveSessionsLoadedMsg{group: "tripleS",
		ids: map[int]bool{memberAt(m, 1): true}})
	m = updated.(Model)

	if strings.Contains(titleAt(m, 1), "🔴") {
		t.Fatalf("a stale group's live status marked row %q", titleAt(m, 1))
	}
}

// TestLiveMarkerClearedOnGroupSwitch checks live status doesn't survive a group
// switch: it's per-group and too volatile to cache.
func TestLiveMarkerClearedOnGroupSwitch(t *testing.T) {
	m := newSized(t)
	updated, _ := m.Update(liveSessionsLoadedMsg{group: "tripleS",
		ids: map[int]bool{memberAt(m, 1): true}})
	m = updated.(Model)

	updated, _ = m.Update(uimsg.GroupChanged{Group: "artms"})
	m = updated.(Model)
	for i := range m.memberList.Items() {
		if strings.Contains(titleAt(m, i), "🔴") {
			t.Fatalf("row %q still marked after a group switch", titleAt(m, i))
		}
	}
}

// TestAllListShowsMembers checks that the All list is first in the member list
// and prefixes each replay with the streaming member's name, while a member's
// own list doesn't.
func TestAllListShowsMembers(t *testing.T) {
	m := newSized(t)
	if got := m.memberList.Items()[0].(memberItem).member.Name; got != "All" {
		t.Fatalf("first member row = %q, want All", got)
	}

	buckets := map[int][]cosmo.Vod{
		cosmo.AllMembersID: someVods(1, "SeoYeon"),
		memberAt(m, 1):     someVods(1, "SeoYeon"),
	}
	updated, _ := m.Update(vodsLoadedMsg{group: "tripleS", vods: buckets})
	m = updated.(Model)
	it := m.vodList.Items()[0].(vodItem)
	if !strings.Contains(it.Title(), "SeoYeon: live") {
		t.Fatalf("All item title = %q, want the member prefix", it.Title())
	}
	if !strings.Contains(it.FilterValue(), "SeoYeon") {
		t.Fatalf("All item filter = %q, want the member name filterable", it.FilterValue())
	}

	m, _ = keyRune(t, m, 'j') // onto the first real member
	it = m.vodList.Items()[0].(vodItem)
	if strings.Contains(it.Title(), "SeoYeon:") {
		t.Fatalf("member item title = %q, want no member prefix", it.Title())
	}
}

// TestCursorMoveInstant checks that once the group is loaded, moving the
// member cursor fills the pane immediately with no fetch.
func TestCursorMoveInstant(t *testing.T) {
	m := newSized(t)
	m.all = map[int][]cosmo.Vod{memberAt(m, 1): someVods(2, "SeoYeon")}
	m.cache["tripleS"] = m.all

	m, _ = keyRune(t, m, 'j')
	if len(m.vodList.Items()) != 2 {
		t.Fatalf("vod list = %d items, want 2 filled instantly", len(m.vodList.Items()))
	}
	if m.loading {
		t.Fatal("no loading state on a client-side filter")
	}
	if m.focus != focusMembers {
		t.Fatal("focus should stay on the member list")
	}
}

// TestCursorMoveWhileLoading checks that moving the cursor mid-fetch keeps the
// pane empty without starting another fetch, and that the load fills the pane
// for the member the cursor rests on.
func TestCursorMoveWhileLoading(t *testing.T) {
	m := newSized(t)
	updated, _ := m.Update(uimsg.GroupChanged{Group: "artms"}) // fetch in flight
	m = updated.(Model)

	m, _ = keyRune(t, m, 'j')
	if len(m.vodList.Items()) != 0 {
		t.Fatal("the pane should stay empty while the group loads")
	}

	updated, _ = m.Update(vodsLoadedMsg{group: "artms",
		vods: map[int][]cosmo.Vod{m.current.APIID: someVods(3, "HeeJin")}})
	m = updated.(Model)
	if len(m.vodList.Items()) != 3 {
		t.Fatalf("vod list = %d items, want the moved-to member's 3", len(m.vodList.Items()))
	}
}

// TestStaleGroupLoadDropped checks that a load landing for a group we've since
// left is cached but doesn't touch the pane.
func TestStaleGroupLoadDropped(t *testing.T) {
	m := newSized(t)
	updated, _ := m.Update(uimsg.GroupChanged{Group: "artms"})
	m = updated.(Model)

	updated, _ = m.Update(vodsLoadedMsg{group: "tripleS",
		vods: map[int][]cosmo.Vod{cosmo.AllMembersID: someVods(1, "SeoYeon")}})
	m = updated.(Model)
	if len(m.vodList.Items()) != 0 {
		t.Fatal("a stale group's load must not fill the pane")
	}

	updated, _ = m.Update(uimsg.GroupChanged{Group: "tripleS"})
	m = updated.(Model)
	if m.loading {
		t.Fatal("the stale load should have been cached for its group")
	}
	if len(m.vodList.Items()) != 1 {
		t.Fatalf("cached vods not restored: %d items", len(m.vodList.Items()))
	}
}

// TestNoMembershipStatus checks that a gated stream resolves to a friendly
// membership notice in the status line, not a red error.
func TestNoMembershipStatus(t *testing.T) {
	m := newSized(t)
	updated, _ := m.Update(vodsLoadedMsg{group: "tripleS",
		vods: map[int][]cosmo.Vod{cosmo.AllMembersID: someVods(1, "SeoYeon")}})
	m = updated.(Model)

	updated, _ = m.Update(errMsg{err: cosmo.ErrNoMembership})
	m = updated.(Model)
	if m.err != nil {
		t.Fatalf("err should stay unset, got %v", m.err)
	}
	if !strings.Contains(m.status, "membership required") {
		t.Fatalf("status = %q, want the membership notice", m.status)
	}
	if strings.Contains(m.render(), "error:") {
		t.Fatal("the gate should not render as an error")
	}
}

// TestLockedVodMarked checks that a membership-gated clip renders a lock badge
// in its description line and a playable one doesn't. The badge lives in the
// description, not the title, so it can't be mistaken for the stream title.
func TestLockedVodMarked(t *testing.T) {
	locked := vodItem{vod: cosmo.Vod{Title: "live", Date: "2026-07-08"}}
	if !strings.Contains(locked.Description(), "🔒") {
		t.Fatalf("locked description = %q, want a lock marker", locked.Description())
	}
	if strings.Contains(locked.Title(), "🔒") {
		t.Fatalf("locked title = %q, want no badge in the title", locked.Title())
	}
	open := vodItem{vod: cosmo.Vod{Title: "live", Date: "2026-07-08", Playable: true}}
	if strings.Contains(open.Description(), "🔒") {
		t.Fatalf("playable description = %q, want no lock marker", open.Description())
	}
}

// TestDownloadedVodMarked checks that a replay with a local copy shows the disk
// badge in its description line and an un-downloaded one doesn't.
func TestDownloadedVodMarked(t *testing.T) {
	saved := vodItem{vod: cosmo.Vod{Title: "live", Date: "2026-07-08", Playable: true}, downloaded: true}
	if !strings.Contains(saved.Description(), "💾") {
		t.Fatalf("downloaded description = %q, want a disk marker", saved.Description())
	}
	if strings.Contains(saved.Title(), "💾") {
		t.Fatalf("downloaded title = %q, want no badge in the title", saved.Title())
	}
	fresh := vodItem{vod: cosmo.Vod{Title: "live", Date: "2026-07-08", Playable: true}}
	if strings.Contains(fresh.Description(), "💾") {
		t.Fatalf("un-downloaded description = %q, want no disk marker", fresh.Description())
	}
}

// TestTitleNormalizedWidth checks that a title carrying a width-mismatched emoji
// sequence (the heart-on-fire ZWJ join from Xinyu's real replay list) is stripped
// of the ZWJ/variation-selector runes before display. Drawn raw, lipgloss measures
// the whole cluster as width 2 while wcwidth terminals paint each codepoint as its
// own glyph, so the row over-runs its column and desyncs the list renderer (header
// scrolls off, content duplicates) at narrow widths — the reported bug.
func TestTitleNormalizedWidth(t *testing.T) {
	// "기다릴수없는 내일❤️‍🔥": ❤(U+2764) VS16(U+FE0F) ZWJ(U+200D) 🔥(U+1F525).
	const raw = "기다릴수없는 내일❤️‍\U0001f525"

	// The member-prefixed (All list) and bare forms both go through Normalize.
	all := vodItem{vod: cosmo.Vod{Title: raw, Member: "Xinyu", Date: "2026-07-08",
		Playable: true}, showMember: true}
	own := vodItem{vod: cosmo.Vod{Title: raw, Date: "2026-07-08", Playable: true}}

	for name, got := range map[string]string{
		"all-title":  all.Title(),
		"own-title":  own.Title(),
		"all-filter": all.FilterValue(),
		"own-filter": own.FilterValue(),
	} {
		if strings.ContainsRune(got, '‍') || strings.ContainsRune(got, '️') {
			t.Errorf("%s = %q, still carries the ZWJ/variation-selector runes that overflow the row", name, got)
		}
		// The base emoji survive; only the width-mismatched joiners are dropped.
		if !strings.ContainsRune(got, '❤') || !strings.ContainsRune(got, '\U0001f525') {
			t.Errorf("%s = %q, want the base ❤ and 🔥 kept", name, got)
		}
	}
}

// TestThumbnailKey checks that 't' opens the selected replay's thumbnail and
// is a no-op when the clip has none.
func TestThumbnailKey(t *testing.T) {
	m := newSized(t)
	vods := someVods(1, "SeoYeon")
	vods[0].ThumbnailURL = "https://static.cosmo.fans/t.jpg"
	updated, _ := m.Update(vodsLoadedMsg{group: "tripleS",
		vods: map[int][]cosmo.Vod{cosmo.AllMembersID: vods}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // focus the vod pane
	m = updated.(Model)

	if _, cmd := keyRune(t, m, 't'); cmd == nil {
		t.Fatal("t should open the thumbnail")
	}

	m.all[cosmo.AllMembersID][0].ThumbnailURL = ""
	m.setVods(m.all[cosmo.AllMembersID])
	if _, cmd := keyRune(t, m, 't'); cmd != nil {
		t.Fatal("t must be a no-op without a thumbnail")
	}
}

// TestCursorCachedPerMember checks that each member's replay cursor survives a
// list switch and that a manual refresh resets it.
func TestCursorCachedPerMember(t *testing.T) {
	m := New(nil, "tripleS", ".", 0)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	buckets := map[int][]cosmo.Vod{
		cosmo.AllMembersID: someVods(8, "SeoYeon"),
		memberAt(m, 1):     someVods(8, "SeoYeon"),
		memberAt(m, 2):     someVods(8, "HyeRin"),
	}
	updated, _ = m.Update(vodsLoadedMsg{group: "tripleS", vods: buckets})
	m = updated.(Model)

	open := func(m Model, memberIdx int) Model {
		m.memberList.Select(memberIdx)
		updated, _ := m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
		return updated.(Model)
	}
	down := func(m Model, n int) Model {
		for i := 0; i < n; i++ {
			updated, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
			m = updated.(Model)
		}
		return m
	}
	back := func(m Model) Model {
		updated, _ := m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
		return updated.(Model)
	}

	// Member 1: move down five and leave.
	m = down(open(m, 1), 5)
	if got := m.vodList.Index(); got != 5 {
		t.Fatalf("member1 cursor = %d, want 5", got)
	}
	m = back(m)

	// Member 2: a fresh list starts at the top.
	m = open(m, 2)
	if got := m.vodList.Index(); got != 0 {
		t.Fatalf("member2 cursor = %d, want 0 on first visit", got)
	}
	m = back(m)

	// Member 1 again: remembered position restored.
	m = open(m, 1)
	if got := m.vodList.Index(); got != 5 {
		t.Fatalf("member1 cursor = %d, want 5 restored", got)
	}

	// Manual refresh forgets it.
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = updated.(Model)
	updated, _ = m.Update(vodsLoadedMsg{group: "tripleS", vods: buckets})
	m = updated.(Model)
	if got := m.vodList.Index(); got != 0 {
		t.Fatalf("member1 cursor = %d, want 0 after refresh", got)
	}
}

// TestInFlightDownloadBlocksNavigation checks the back motion cannot bin a
// download that is still running: only 'c' cancels it.
func TestInFlightDownloadBlocksNavigation(t *testing.T) {
	for _, phase := range []dlPhase{phaseResolving, phaseDownloading, phaseMuxing, phaseSubtitles} {
		m := newSized(t)
		m.focus = focusDownload
		m.dl = dlState{phase: phase}

		for _, r := range []rune{'h', 'q', 'r', 'd'} {
			m, _ = keyRune(t, m, r)
			if m.focus != focusDownload {
				t.Fatalf("phase %v: %q left the download view", phase, r)
			}
		}

		m, _ = keyRune(t, m, 'c')
		if m.focus != focusVods {
			t.Fatalf("phase %v: focus = %v, want focusVods after c", phase, m.focus)
		}
		if m.dl.phase != phaseResolving || m.dlProgress != nil {
			t.Fatalf("phase %v: download state not cleared on cancel", phase)
		}
	}
}

// TestFinishedDownloadDismissed checks that once a download settles there is
// nothing left to lose, so the uniform back motion leaves the panel.
func TestFinishedDownloadDismissed(t *testing.T) {
	for _, phase := range []dlPhase{phaseDone, phaseError} {
		m := newSized(t)
		m.focus = focusDownload
		m.dl = dlState{phase: phase, savedPath: "/tmp/x.mp4", err: errors.New("boom")}

		m, _ = keyRune(t, m, 'h')
		if m.focus != focusVods {
			t.Fatalf("phase %v: focus = %v, want focusVods after h", phase, m.focus)
		}
	}
}

// TestDownloadHint checks the modal advertises whichever exit is live: 'c' while
// the download can still be cancelled, and the back motion once it has settled.
// The view blocks the motions mid-download, so it has to say when they work.
func TestDownloadHint(t *testing.T) {
	m := newSized(t)
	m.focus = focusDownload
	m.dl = dlState{phase: phaseDownloading, vod: cosmo.Vod{Title: "live"}}
	v := m.render()
	if !strings.Contains(v, "c: cancel") {
		t.Fatalf("a running download must advertise the cancel key, got:\n%s", v)
	}
	if strings.Contains(v, "h: back") {
		t.Fatalf("a running download must not advertise a blocked motion, got:\n%s", v)
	}

	for _, phase := range []dlPhase{phaseDone, phaseError} {
		m.dl.phase = phase
		m.dl.savedPath = "/tmp/x.mp4"
		m.dl.err = errors.New("boom")
		v = m.render()
		if !strings.Contains(v, "h: back") {
			t.Fatalf("phase %v must advertise the way out, got:\n%s", phase, v)
		}
		if strings.Contains(v, "cancel") {
			t.Fatalf("phase %v has nothing to cancel, got:\n%s", phase, v)
		}
	}
}

// TestReplayFilterSpansTheDrawnRow filters the replay list by a date and checks
// the delegate's match indices land on that date where Title actually draws it.
// The delegate highlights matches by styling Title's runes at the indices the
// filter found in FilterValue, so the two only line up — and the underline only
// sits under the text the user typed — while they are the same string.
func TestReplayFilterSpansTheDrawnRow(t *testing.T) {
	m := newSized(t)
	buckets := map[int][]cosmo.Vod{cosmo.AllMembersID: someVods(1, "SeoYeon")}
	updated, _ := m.Update(vodsLoadedMsg{group: "tripleS", vods: buckets})
	m = updated.(Model)

	const date = "2026-07-08" // someVods' date, drawn at the head of every row
	m.vodList.SetFilterText(date)

	if got := len(m.vodList.VisibleItems()); got != 1 {
		t.Fatalf("filtering by date matched %d rows, want the 1 replay: a date must be filterable", got)
	}
	matched := m.vodList.MatchesForItem(0)
	title := []rune(m.vodList.VisibleItems()[0].(vodItem).Title())
	var under strings.Builder
	for _, i := range matched {
		if i < 0 || i >= len(title) {
			t.Fatalf("match index %d is outside the drawn title %q", i, string(title))
		}
		under.WriteRune(title[i])
	}
	if under.String() != date {
		t.Errorf("the highlight sits under %q, want it under the typed %q", under.String(), date)
	}
}

// TestCaptionPath checks subtitles land on the video's own stem, with the
// language in the name so switching it in the app adds a file rather than
// overwriting the one already saved.
func TestCaptionPath(t *testing.T) {
	video := "/vods/cosmo-tripleS/ChaeWon/2026-09-16_ChaeWon_뿌뿌.mp4"
	if got, want := captionPath(video, "ko"), "/vods/cosmo-tripleS/ChaeWon/2026-09-16_ChaeWon_뿌뿌.ko.vtt"; got != want {
		t.Errorf("captionPath = %q, want %q", got, want)
	}
	if ko, en := captionPath(video, "ko"), captionPath(video, "en"); ko == en {
		t.Errorf("languages collide: both %q", ko)
	}
}

// TestSubtitleOutcomeUncaptioned checks an uncaptioned replay is reported
// without asking the API for a track that was never made — the nil client
// stands in for that: touching it would panic.
func TestSubtitleOutcomeUncaptioned(t *testing.T) {
	got := subtitleOutcome(t.Context(), nil, cosmo.Clip{HasCaption: false}, cosmo.Vod{VideoID: "1537"}, "/vods/x.mp4")
	if !strings.Contains(got, "none") {
		t.Errorf("outcome = %q, want it to report none", got)
	}
}

// TestSubtitleFetchHoldsTheExit checks the window between the video landing and
// its subtitles arriving is treated as in-flight. The video is saved by then, so
// it is tempting to call the download finished — but offering the exit there
// invites the user to leave during the one stretch that still has work to
// finish, and the subtitle line would never be read.
func TestSubtitleFetchHoldsTheExit(t *testing.T) {
	m := newSized(t)
	m.focus = focusDownload
	m.dl = dlState{phase: phaseSubtitles, savedPath: "/tmp/x.mp4", vod: cosmo.Vod{Title: "live"}}

	v := m.render()
	if strings.Contains(v, "h: back") {
		t.Fatalf("the exit must stay hidden while subtitles are in flight, got:\n%s", v)
	}
	if !strings.Contains(v, "fetching subtitles") {
		t.Fatalf("the phase must say what it is waiting on, got:\n%s", v)
	}

	// And the settled phase, which carries the outcome, does offer it.
	m.dl.phase, m.dl.subtitles = phaseDone, "ko"
	v = m.render()
	if !strings.Contains(v, "h: back") || !strings.Contains(v, "subtitles: ko") {
		t.Fatalf("a settled download must show the outcome and the way out, got:\n%s", v)
	}
}
