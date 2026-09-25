package room

import (
	"strings"
	"testing"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"

	tea "charm.land/bubbletea/v2"
)

// TestPostTitleMediaCounts checks that a post's title counts images and videos
// separately and omits a marker when that kind is absent.
func TestPostTitleMediaCounts(t *testing.T) {
	mixed := postItem{post: cosmo.Post{CreatedAt: "2026-07-10T05:08:54.095Z", Media: []cosmo.Media{
		{URL: "a.jpg", Kind: "image"}, {URL: "b.jpg", Kind: "image"}, {URL: "c.mp4", Kind: "video"},
	}}}
	if got := mixed.Title(); !strings.Contains(got, "📷×2") || !strings.Contains(got, "🎥×1") {
		t.Fatalf("mixed title = %q, want both media counts", got)
	}

	imagesOnly := postItem{post: cosmo.Post{CreatedAt: "2026-07-10T05:08:54.095Z", Media: []cosmo.Media{
		{URL: "a.jpg", Kind: "image"},
	}}}
	if got := imagesOnly.Title(); !strings.Contains(got, "📷×1") || strings.Contains(got, "🎥") {
		t.Fatalf("image-only title = %q, want just the camera count", got)
	}

	textOnly := postItem{post: cosmo.Post{CreatedAt: "2026-07-10T05:08:54.095Z"}}
	if got := textOnly.Title(); strings.Contains(got, "📷") || strings.Contains(got, "🎥") {
		t.Fatalf("text-only title = %q, want no media markers", got)
	}
}

// TestGroupCacheRestore checks that returning to a group restores its posts
// from the session cache instead of refetching.
func TestGroupCacheRestore(t *testing.T) {
	m := New(nil, "tripleS", ".", false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	posts := []cosmo.Post{{ID: "1", Content: "hi", CreatedAt: "2026-07-08T07:00:00.000Z"}}
	updated, _ = m.Update(postsLoadedMsg{group: "tripleS", posts: posts})
	m = updated.(Model)

	updated, cmd := m.Update(uimsg.GroupChanged{Group: "artms"})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("an uncached group should fetch")
	}
	if len(m.all) != 0 {
		t.Fatal("posts should clear while the new group loads")
	}

	updated, cmd = m.Update(uimsg.GroupChanged{Group: "tripleS"})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("a cached group should restore without fetching")
	}
	if len(m.all) != 1 || m.all[0].Content != "hi" {
		t.Fatalf("cached posts not restored: %+v", m.all)
	}
}

// key feeds one keystroke through Update and returns the updated model.
func key(t *testing.T, m Model, s string) Model {
	t.Helper()
	var km tea.KeyPressMsg
	switch s {
	case "l", "h", "j":
		km = tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	default:
		t.Fatalf("unhandled key %q", s)
	}
	updated, _ := m.Update(km)
	return updated.(Model)
}

// TestCursorCachedPerMember checks that each member's post cursor is remembered
// across list switches, and that a manual refresh resets it.
func TestCursorCachedPerMember(t *testing.T) {
	m := New(nil, "tripleS", ".", false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	var posts []cosmo.Post
	for _, name := range []string{"SeoYeon", "HyeRin"} {
		for i := 0; i < 8; i++ {
			p := cosmo.Post{CreatedAt: "2026-07-08T07:00:00.000Z"}
			p.Author.Nickname = name
			posts = append(posts, p)
		}
	}
	updated, _ = m.Update(postsLoadedMsg{group: "tripleS", posts: posts})
	m = updated.(Model)

	// Enter SeoYeon (member index 1) and move down five posts.
	m.memberList.Select(1)
	m = key(t, m, "l")
	for i := 0; i < 5; i++ {
		m = key(t, m, "j")
	}
	if got := m.postList.Index(); got != 5 {
		t.Fatalf("SeoYeon cursor = %d, want 5", got)
	}

	// Back to members, then into HyeRin (index 2): a fresh list starts at top.
	m = key(t, m, "h")
	m.memberList.Select(2)
	m = key(t, m, "l")
	if got := m.postList.Index(); got != 0 {
		t.Fatalf("HyeRin cursor = %d, want 0 on first visit", got)
	}

	// Returning to SeoYeon restores her remembered position.
	m = key(t, m, "h")
	m.memberList.Select(1)
	m = key(t, m, "l")
	if got := m.postList.Index(); got != 5 {
		t.Fatalf("SeoYeon cursor = %d, want 5 restored", got)
	}

	// A refresh refetches the whole group, so it forgets every member's cursor,
	// not just the one on screen. Give HyeRin a remembered position first.
	m = key(t, m, "h")
	m.memberList.Select(2)
	m = key(t, m, "l")
	for i := 0; i < 3; i++ {
		m = key(t, m, "j")
	}
	if got := m.postList.Index(); got != 3 {
		t.Fatalf("HyeRin cursor = %d, want 3 before refresh", got)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = updated.(Model)
	updated, _ = m.Update(postsLoadedMsg{group: "tripleS", posts: posts})
	m = updated.(Model)
	if got := m.postList.Index(); got != 0 {
		t.Fatalf("HyeRin cursor = %d, want 0 after refresh", got)
	}

	// SeoYeon's remembered position is cleared by the same refresh.
	m = key(t, m, "h")
	m.memberList.Select(1)
	m = key(t, m, "l")
	if got := m.postList.Index(); got != 0 {
		t.Fatalf("SeoYeon cursor = %d, want 0 after group refresh", got)
	}
}

// TestPaneFollowsMemberCursor checks that moving the member cursor re-filters
// the post pane in place, without focus leaving the member list.
func TestPaneFollowsMemberCursor(t *testing.T) {
	m := New(nil, "tripleS", ".", false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	var posts []cosmo.Post
	for i := 0; i < 3; i++ {
		p := cosmo.Post{CreatedAt: "2026-07-08T07:00:00.000Z"}
		p.Author.Nickname = "SeoYeon"
		posts = append(posts, p)
	}
	updated, _ = m.Update(postsLoadedMsg{group: "tripleS", posts: posts})
	m = updated.(Model)
	if m.selected != "All" {
		t.Fatalf("selected = %q, want All initially", m.selected)
	}

	// Move down onto the first member: the pane follows, focus stays left.
	m = key(t, m, "j")
	if m.selected != "SeoYeon" {
		t.Fatalf("selected = %q, want SeoYeon after cursor move", m.selected)
	}
	if got := len(m.postList.Items()); got != 3 {
		t.Fatalf("post list = %d items, want 3 (SeoYeon's posts)", got)
	}
	if m.focus != focusMembers {
		t.Fatal("focus should stay on the member list")
	}
}

// TestStalePostsLoadCached checks that a load finishing after a group switch
// doesn't clobber the current view but still lands in the cache.
func TestStalePostsLoadCached(t *testing.T) {
	m := New(nil, "artms", ".", false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	updated, _ = m.Update(postsLoadedMsg{group: "tripleS", posts: []cosmo.Post{{ID: "1"}}})
	m = updated.(Model)
	if len(m.all) != 0 {
		t.Fatal("a stale group's posts must not show in the current group")
	}
	if len(m.cache["tripleS"]) != 1 {
		t.Fatal("the stale load should still be cached for its own group")
	}
}

// openDetailWith loads a single post, enters the posts pane, and opens its detail
// view. It returns the model focused on the detail viewport with comments still
// loading (the fetch command is discarded — tests deliver comments directly).
func openDetailWith(t *testing.T, post cosmo.Post) Model {
	t.Helper()
	m := New(nil, "tripleS", ".", false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(postsLoadedMsg{group: "tripleS", posts: []cosmo.Post{post}})
	m = updated.(Model)

	m = key(t, m, "l") // into the posts pane
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.focus != focusDetail {
		t.Fatalf("focus = %v, want focusDetail after Enter", m.focus)
	}
	return m
}

// TestPostDetailRender checks that Enter opens the detail viewport with the full
// (untruncated, multi-line) content and media summary, and that fetched comments
// render with the artist markers (top-level via isArtist, replies via membership).
func TestPostDetailRender(t *testing.T) {
	long := "line one of a very long body\nline two that the list would truncate away"
	post := cosmo.Post{
		ID:               "2194",
		Content:          long,
		CreatedAt:        "2026-07-13T05:41:30.170Z",
		Media:            []cosmo.Media{{URL: "a.jpg", Kind: "image"}},
		MediaAspectRatio: "3:4",
	}
	post.Author.Nickname = "JiYeon"
	m := openDetailWith(t, post)

	// While loading, the section shows a loading marker, not the content yet.
	if !strings.Contains(m.viewport.View(), "loading") {
		t.Fatalf("detail view should show comments loading:\n%s", m.viewport.View())
	}

	// Deliver the fetched thread: an artist top-level comment plus a fan comment
	// with an artist reply (reply author is a tripleS member).
	fan := cosmo.Comment{Content: "nice pic", Replies: []cosmo.CommentReply{{Content: "a reply"}}}
	fan.Author.Nickname = "someFan"
	fan.Replies[0].Author.Nickname = "SeoYeon"
	artist := cosmo.Comment{Content: "top comment", IsArtist: true}
	artist.Author.Nickname = "JiYeon"
	updated, _ := m.Update(commentsLoadedMsg{postID: "2194", artistOnly: false,
		comments: []cosmo.Comment{artist, fan}})
	m = updated.(Model)

	view := m.viewport.View()
	for _, want := range []string{"JiYeon", "line two that the list would truncate away",
		"top comment", "a reply", "SeoYeon"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail view missing %q:\n%s", want, view)
		}
	}
}

// TestArtistFilterToggle checks that 'a' flips the artist-only filter, refetches
// on a miss, and serves a cached thread (nil command) when toggling back.
func TestArtistFilterToggle(t *testing.T) {
	post := cosmo.Post{ID: "2194", Content: "hi", CreatedAt: "2026-07-13T05:41:30.170Z"}
	post.Author.Nickname = "JiYeon"
	m := openDetailWith(t, post)

	// Cache the unfiltered thread (as if the open fetch returned).
	all := cosmo.Comment{Content: "everyone sees this"}
	all.Author.Nickname = "fan"
	updated, _ := m.Update(commentsLoadedMsg{postID: "2194", artistOnly: false,
		comments: []cosmo.Comment{all}})
	m = updated.(Model)

	// 'a' turns the filter on; artist thread isn't cached, so a fetch is issued.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated.(Model)
	if !m.artistOnly {
		t.Fatal("'a' should enable the artist-only filter")
	}
	if cmd == nil {
		t.Fatal("an uncached filter should trigger a fetch")
	}
	art := cosmo.Comment{Content: "artist only", IsArtist: true}
	art.Author.Nickname = "JiYeon"
	updated, _ = m.Update(commentsLoadedMsg{postID: "2194", artistOnly: true,
		comments: []cosmo.Comment{art}})
	m = updated.(Model)
	if !strings.Contains(m.viewport.View(), "artist only") {
		t.Fatalf("artist-only view missing its comment:\n%s", m.viewport.View())
	}

	// 'a' again returns to the cached unfiltered thread with no fetch.
	updated, cmd = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = updated.(Model)
	if m.artistOnly {
		t.Fatal("'a' should disable the artist-only filter")
	}
	if cmd != nil {
		t.Fatal("a cached filter should restore without fetching")
	}
	if !strings.Contains(m.viewport.View(), "everyone sees this") {
		t.Fatalf("unfiltered view not restored from cache:\n%s", m.viewport.View())
	}
}

// TestDetailEscReturns checks that esc leaves the detail view for the post list.
func TestDetailEscReturns(t *testing.T) {
	m := New(nil, "tripleS", ".", false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	updated, _ = m.Update(postsLoadedMsg{group: "tripleS", posts: []cosmo.Post{
		{Content: "hi", CreatedAt: "2026-07-13T05:41:30.170Z"},
	}})
	m = updated.(Model)

	m = key(t, m, "l")
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if m.focus != focusDetail {
		t.Fatal("Enter should open the detail view")
	}

	m = key(t, m, "h")
	if m.focus != focusPosts {
		t.Fatalf("focus = %v, want focusPosts after h", m.focus)
	}
}

// TestTranslatePost checks that 't' requests a translation (showing a pending
// line), that the delivered translation renders beneath the original text, and
// that pressing 't' again is a no-op.
func TestTranslatePost(t *testing.T) {
	post := cosmo.Post{ID: "2194", Content: "후면 selfie", CreatedAt: "2026-07-13T05:41:30.170Z"}
	post.Author.Nickname = "JiYeon"
	m := openDetailWith(t, post)

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("'t' should trigger a translation fetch")
	}
	if !m.transPending["2194"] {
		t.Fatal("post should be marked translating")
	}
	if !strings.Contains(m.viewport.View(), "translating") {
		t.Fatalf("detail view should show a translating line:\n%s", m.viewport.View())
	}

	updated, _ = m.Update(postTranslatedMsg{postID: "2194",
		t: cosmo.Translation{TranslatedContent: "Rear-facing selfie"}})
	m = updated.(Model)
	view := m.viewport.View()
	if !strings.Contains(view, "후면 selfie") || !strings.Contains(view, "Rear-facing selfie") {
		t.Fatalf("translation should render beneath the original:\n%s", view)
	}

	// Already translated: pressing 't' again does nothing.
	_, cmd = m.Update(tea.KeyPressMsg{Code: 't', Text: "t"})
	if cmd != nil {
		t.Fatal("re-pressing 't' on a translated post should be a no-op")
	}
}

// TestCommentSortToggle checks that 's' reverses the comment order between
// newest-first (default) and oldest-first, without an extra fetch.
func TestCommentSortToggle(t *testing.T) {
	post := cosmo.Post{ID: "2194", Content: "hi", CreatedAt: "2026-07-13T05:41:30.170Z"}
	post.Author.Nickname = "JiYeon"
	m := openDetailWith(t, post)

	// Comments as the API returns them: newest first.
	mk := func(content string) cosmo.Comment {
		c := cosmo.Comment{Content: content}
		c.Author.Nickname = "fan"
		return c
	}
	updated, _ := m.Update(commentsLoadedMsg{postID: "2194", artistOnly: false,
		comments: []cosmo.Comment{mk("newest"), mk("middle"), mk("oldest")}})
	m = updated.(Model)

	before := m.viewport.View()
	if strings.Index(before, "newest") > strings.Index(before, "oldest") {
		t.Fatalf("default order should be newest-first:\n%s", before)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	m = updated.(Model)
	if !m.commentsOldest {
		t.Fatal("'s' should enable oldest-first sorting")
	}
	after := m.viewport.View()
	if strings.Index(after, "oldest") > strings.Index(after, "newest") {
		t.Fatalf("after 's' order should be oldest-first:\n%s", after)
	}

	// 's' again flips back to newest-first.
	updated, _ = m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	m = updated.(Model)
	if m.commentsOldest {
		t.Fatal("'s' should toggle back to newest-first")
	}
}

// TestPostFilterSpansTheDrawnRow filters the post list by a date and checks the
// delegate's match indices land on that date where Title actually draws it. The
// delegate highlights matches by styling Title's runes at the indices the filter
// found in FilterValue, so the underline only sits under the text the user typed
// while FilterValue leads with the title verbatim. The body still filters: those
// matches fall past the title and just go unhighlighted.
func TestPostFilterSpansTheDrawnRow(t *testing.T) {
	m := New(nil, "tripleS", ".", false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	posts := []cosmo.Post{{ID: "1", Content: "hello", CreatedAt: "2026-07-08T07:00:00.000Z"}}
	updated, _ = m.Update(postsLoadedMsg{group: "tripleS", posts: posts})
	m = updated.(Model)

	date := cosmo.LocalDate(posts[0].CreatedAt)
	m.postList.SetFilterText(date)
	if got := len(m.postList.VisibleItems()); got != 1 {
		t.Fatalf("filtering by date matched %d rows, want the 1 post: a date must be filterable", got)
	}
	title := []rune(m.postList.VisibleItems()[0].(postItem).Title())
	var under strings.Builder
	for _, i := range m.postList.MatchesForItem(0) {
		if i < 0 || i >= len(title) {
			t.Fatalf("match index %d is outside the drawn title %q", i, string(title))
		}
		under.WriteRune(title[i])
	}
	if under.String() != date {
		t.Errorf("the highlight sits under %q, want it under the typed %q", under.String(), date)
	}

	// The body stays filterable, unhighlighted: it is drawn on the row below.
	m.postList.SetFilterText("hello")
	if got := len(m.postList.VisibleItems()); got != 1 {
		t.Fatalf("filtering by body matched %d rows, want the 1 post", got)
	}
}

// TestPasteIntoListFilter checks pasted text reaches the filter box of the pane
// that is filtering, and not the other pane's.
func TestPasteIntoListFilter(t *testing.T) {
	m := New(nil, "tripleS", ".", false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(Model)
	if !m.AcceptsText() {
		t.Fatal("/ should open a filter box")
	}

	updated, _ = m.Update(tea.PasteMsg{Content: "seoyeon"})
	m = updated.(Model)
	if got := m.memberList.FilterInput.Value(); got != "seoyeon" {
		t.Fatalf("member filter = %q, want the pasted text", got)
	}
	if got := m.postList.FilterInput.Value(); got != "" {
		t.Fatalf("post filter = %q, want it untouched", got)
	}
}
