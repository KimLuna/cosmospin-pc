package talk

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/config"
	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/uimsg"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestRenderNoChannel builds the page, sizes it, and renders with no channel
// open to confirm the two-pane layout doesn't panic (no client calls involved).
func TestRenderNoChannel(t *testing.T) {
	m := New(nil, "tripleS", ".", true, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	if !m.ready {
		t.Fatal("expected page ready after sizing")
	}
	view := m.render()
	if strings.TrimSpace(view) == "" {
		t.Fatal("expected non-empty view")
	}
	if m.AcceptsText() {
		t.Fatal("should not capture text before a channel is opened")
	}
}

// sizedModel builds a sized page preloaded with n single-line artist messages.
func sizedModel(t *testing.T, n int) Model {
	t.Helper()
	m := New(nil, "tripleS", ".", true, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	var msgs []cosmo.Message
	for i := 0; i < n; i++ {
		msgs = append(msgs, cosmo.Message{
			ID:         fmt.Sprintf("msg-%02d", i),
			SenderType: "artistMember",
			Content:    fmt.Sprintf("hello %d", i),
			CreatedAt:  fmt.Sprintf("2026-07-08T07:%02d:00.000Z", i),
		})
	}
	m.appendMessages(msgs, true)
	return m
}

// TestDaySeparators checks that a date line renders above the first message of
// each local day, and only there.
func TestDaySeparators(t *testing.T) {
	m := New(nil, "tripleS", ".", true, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	// The first two messages share an instant (same local date everywhere); the
	// third is 48h later, so it lands on a new local date in every timezone.
	msgs := []cosmo.Message{
		{ID: "a", SenderType: "artistMember", Content: "one", CreatedAt: "2026-07-06T12:00:00.000Z"},
		{ID: "b", SenderType: "artistMember", Content: "two", CreatedAt: "2026-07-06T12:00:00.000Z"},
		{ID: "c", SenderType: "artistMember", Content: "three", CreatedAt: "2026-07-08T12:00:00.000Z"},
	}
	m.appendMessages(msgs, true)
	view := m.viewport.View()
	if got := strings.Count(view, "── 20"); got != 2 {
		t.Fatalf("separator count = %d, want 2\n%s", got, view)
	}
	for _, iso := range []string{msgs[0].CreatedAt, msgs[2].CreatedAt} {
		if !strings.Contains(view, "── "+cosmo.LocalDate(iso)+" ──") {
			t.Fatalf("missing separator for %s\n%s", iso, view)
		}
	}
}

// TestVisibleIndices scrolls the viewport and checks that only the messages
// intersecting it are reported, at both ends of the history.
func TestVisibleIndices(t *testing.T) {
	m := sizedModel(t, 30)
	// Each message renders 2 lines + a blank separator = 3 lines. The viewport
	// is 28 lines tall, so about ten messages fit.
	m.viewport.GotoTop()
	vis := m.visibleIndices()
	if len(vis) == 0 || vis[0] != 0 {
		t.Fatalf("expected the first message visible at the top, got %v", vis)
	}
	if len(vis) >= 30 {
		t.Fatalf("expected a screenful, not all 30 messages: %v", vis)
	}
	m.viewport.GotoBottom()
	vis = m.visibleIndices()
	if len(vis) == 0 || vis[len(vis)-1] != 29 {
		t.Fatalf("expected the last message visible at the bottom, got %v", vis)
	}
	if vis[0] == 0 {
		t.Fatalf("expected the first message off-screen at the bottom, got %v", vis)
	}
}

// TestTranslateSelectedSilentSkips checks that t asks the API for nothing, and
// says nothing, on the messages there is no translation to fetch for: your own
// (already in your language) and attachments with no text, which the API answers
// with an empty translation. A sticker sent with text still translates.
func TestTranslateSelectedSilentSkips(t *testing.T) {
	m := sizedModel(t, 1)
	m.memberID = 7
	m.messages = []cosmo.Message{
		{ID: "own", SenderType: "user", Content: "hi"},
		{ID: "photo", SenderType: "artistMember", Type: "image", MediaURL: "https://example.com/p.jpg"},
		{ID: "sticker", SenderType: "artistMember", Type: "sticker", StickerURL: "https://example.com/s.png"},
		{ID: "sticker-text", SenderType: "artistMember", Type: "sticker_text", StickerURL: "https://example.com/s.png", Content: "안녕"},
	}
	for i, want := range []bool{false, false, false, true} {
		id := m.messages[i].ID
		m.selected = i
		updated, cmd := m.translateSelected()
		got := updated.(Model)
		if (cmd != nil) != want {
			t.Fatalf("%s: requested = %v, want %v", id, cmd != nil, want)
		}
		if _, pending := got.transPending[id]; pending != want {
			t.Fatalf("%s: pending = %v, want %v", id, pending, want)
		}
		if got.status != "" {
			t.Fatalf("%s: status = %q, want silence", id, got.status)
		}
	}
}

// TestTranslateVisibleQueues checks that T queues only untranslated on-screen
// artist messages, keeps one request in flight, and dedupes repeat presses.
func TestTranslateVisibleQueues(t *testing.T) {
	m := sizedModel(t, 30)
	m.viewport.GotoBottom()
	vis := m.visibleIndices()
	// Pretend the newest message is already translated: it must be skipped.
	m.translations[m.messages[29].ID] = cosmo.Translation{TranslatedContent: "done"}

	updated, cmd := m.translateVisible()
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected a command for the first in-flight request")
	}
	want := len(vis) - 1 // all visible minus the pre-translated one
	if got := len(m.transPending); got != want {
		t.Fatalf("pending = %d, want %d", got, want)
	}
	if got := len(m.transQueue); got != want-1 {
		t.Fatalf("queue = %d, want %d (one popped as in flight)", got, want-1)
	}
	// A second T while the batch runs must not queue duplicates or fire a
	// second concurrent request.
	updated, cmd = m.translateVisible()
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("expected no new command while a request is in flight")
	}
	if got := len(m.transPending); got != want {
		t.Fatalf("pending after repeat T = %d, want %d", got, want)
	}
}

// TestTranslateVisibleAllTranslatedIsSilent checks that T over an
// already-translated screen says nothing. There is no request to answer it, so
// any notice set here would sit on the status line until the next reload — and a
// t that finds its message already translated is silent for the same reason.
func TestTranslateVisibleAllTranslatedIsSilent(t *testing.T) {
	m := sizedModel(t, 5)
	m.viewport.GotoBottom()
	for _, msg := range m.messages {
		m.translations[msg.ID] = cosmo.Translation{TranslatedContent: "done"}
	}
	updated, cmd := m.translateVisible()
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("expected no request with nothing left to translate")
	}
	if m.status != "" {
		t.Fatalf("status = %q, want empty", m.status)
	}
	if got := m.render(); strings.Contains(got, "translat…") || strings.Contains(got, "nothing on screen") {
		t.Fatalf("unexpected notice rendered: %q", got)
	}
}

// TestTranslatedMsgDrainsQueue checks that each translation result chains the
// next queued request until the queue is empty.
func TestTranslatedMsgDrainsQueue(t *testing.T) {
	m := sizedModel(t, 3)
	m.transPending = map[string]transPend{"msg-00": {origin: transBatch}, "msg-01": {origin: transBatch}}
	m.transQueue = []transReq{{id: "msg-01"}} // msg-00 is in flight

	updated, cmd := m.Update(translatedMsg{id: "msg-00", t: cosmo.Translation{TranslatedContent: "hi"}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected the result to chain the next queued request")
	}
	if _, pending := m.transPending["msg-00"]; len(m.transQueue) != 0 || pending {
		t.Fatalf("queue/pending not drained: queue=%v pending=%v", m.transQueue, m.transPending)
	}
	updated, cmd = m.Update(translatedMsg{id: "msg-01", t: cosmo.Translation{TranslatedContent: "hi"}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("expected no command once the queue is empty")
	}
	if len(m.transPending) != 0 {
		t.Fatalf("pending should be empty, got %v", m.transPending)
	}
}

// TestNoTranslationNoticeExpires covers the one notice nothing on the page
// answers: a translation the user asked for that came back empty. It has to
// retire itself, and its tick must not cut short whatever replaced it since.
func TestNoTranslationNoticeExpires(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 7
	m.transPending = map[string]transPend{"msg-02": {memberID: 7, origin: transSingle}}

	updated, cmd := m.Update(translatedMsg{memberID: 7, id: "msg-02"})
	m = updated.(Model)
	if m.status != "no translation available" {
		t.Fatalf("status = %q, want the empty-translation fallback", m.status)
	}
	if cmd == nil {
		t.Fatal("expected a tick to retire the notice")
	}

	m.status = "beginning of history"
	updated, _ = m.Update(statusExpiredMsg{text: "no translation available"})
	m = updated.(Model)
	if m.status != "beginning of history" {
		t.Fatalf("a stale tick cleared a newer notice: status = %q", m.status)
	}

	m.status = "no translation available"
	updated, _ = m.Update(statusExpiredMsg{text: "no translation available"})
	m = updated.(Model)
	if m.status != "" {
		t.Fatalf("status = %q, want the notice cleared", m.status)
	}
}

// TestTransientNoticesExpire checks the notices nothing on the page answers: a
// download result and the end of history both have to retire themselves, or
// they sit on the status line until the next reload.
func TestTransientNoticesExpire(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 7
	// With the welcome message already accounted for, an empty older page has
	// nothing left to add and falls back to announcing the end.
	m.welcomeDone = true

	for _, tc := range []struct {
		name string
		msg  tea.Msg
		want string
	}{
		{"saved", downloadDoneMsg{path: "/tmp/photo.jpg"}, "saved /tmp/photo.jpg"},
		{"already saved", downloadDoneMsg{path: "/tmp/photo.jpg", skipped: true}, "already saved: /tmp/photo.jpg"},
		{"end of history", olderLoadedMsg{memberID: 7}, "beginning of history"},
		{"no welcome message", welcomeLoadedMsg{memberID: 7, absent: true}, "beginning of history"},
	} {
		updated, cmd := m.Update(tc.msg)
		got := updated.(Model)
		if got.status != tc.want {
			t.Fatalf("%s: status = %q, want %q", tc.name, got.status, tc.want)
		}
		if cmd == nil {
			t.Fatalf("%s: expected a tick to retire the notice", tc.name)
		}
		updated, _ = got.Update(statusExpiredMsg{text: tc.want})
		if got = updated.(Model); got.status != "" {
			t.Fatalf("%s: status = %q after its tick, want cleared", tc.name, got.status)
		}
	}
}

// TestSendLeavesInsertMode checks that the enter which actually dispatches a
// reply hands the keys back, the same exit esc gives. The enters that send
// nothing — an empty box, or a channel with nothing to reply to — keep composing.
func TestSendLeavesInsertMode(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 7
	m.setMode(modeInsert)
	m.reply.Focus()

	updated, cmd := m.send()
	m = updated.(Model)
	if cmd != nil || m.mode != modeInsert {
		t.Fatalf("enter on an empty box: dispatched = %v, mode = %v, want neither", cmd != nil, m.mode)
	}

	m.reply.SetValue("안녕하세요")
	updated, cmd = m.send()
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected the reply to be dispatched")
	}
	if m.mode != modeNormal {
		t.Fatalf("mode = %v, want normal mode once the reply is on its way", m.mode)
	}
	if m.reply.Value() != "" {
		t.Fatalf("reply box = %q, want emptied", m.reply.Value())
	}
	if m.reply.Focused() {
		t.Fatal("the reply box should be blurred once the keys are handed back")
	}
	if got := m.render(); !strings.Contains(got, "i: reply") {
		t.Fatalf("expected the normal-mode hints back, got %q", got)
	}
}

// TestSendWithNothingToReplyTo covers a channel the artist has never posted in
// (a freshly subscribed member): every send is a reply to a message, so there is
// nothing to attach one to. The notice has to retire itself, and the typed text
// has to survive it — the artist's first message makes the same reply sendable.
func TestSendWithNothingToReplyTo(t *testing.T) {
	m := sizedModel(t, 1)
	m.memberID = 7
	m.messages = nil
	m.setMode(modeInsert) // enter only reaches send from the reply box
	m.reply.SetValue("안녕하세요")

	updated, cmd := m.send()
	m = updated.(Model)
	if m.status != "nothing to reply to yet" {
		t.Fatalf("status = %q, want the nothing-to-reply-to notice", m.status)
	}
	// The insert-mode status line is a fixed key hint, so a notice that answers
	// enter has to push through it or the send looks like it did nothing at all.
	if got := m.render(); !strings.Contains(got, "nothing to reply to yet") {
		t.Fatalf("notice not rendered while composing: %q", got)
	}
	if m.mode != modeInsert {
		t.Fatal("a send that never left should keep the reply box")
	}
	if cmd == nil {
		t.Fatal("expected a tick to retire the notice")
	}
	if m.reply.Value() != "안녕하세요" {
		t.Fatalf("reply box = %q, want the typed text kept", m.reply.Value())
	}

	updated, _ = m.Update(statusExpiredMsg{text: "nothing to reply to yet"})
	if m = updated.(Model); m.status != "" {
		t.Fatalf("status = %q after its tick, want cleared", m.status)
	}
}

// TestOlderLoadFailureClearsProgress checks that a failed history page takes its
// own "loading older messages…" line down with it. The error hides the line
// while it shows, so leaving it set would resurrect it on the next keypress
// (which dismisses the error), with no load left to answer it.
func TestOlderLoadFailureClearsProgress(t *testing.T) {
	m := sizedModel(t, 5)
	m.memberID = 7
	updated, _ := m.loadOlder()
	m = updated.(Model)
	if m.status != "loading older messages…" || !m.loadingOlder {
		t.Fatalf("precondition: status = %q, loadingOlder = %v", m.status, m.loadingOlder)
	}

	updated, _ = m.Update(olderLoadedMsg{memberID: 7, err: errors.New("boom")})
	m = updated.(Model)
	if m.err == nil {
		t.Fatal("the failure should surface as an error")
	}
	if m.status != "" {
		t.Fatalf("status = %q, want the progress line cleared", m.status)
	}
}

// TestTranslateLastMessageStaysPinned guards the viewport fix: translating the
// selected (last) message grows its block by a translation line, and the translatedMsg
// handler must re-scroll so the pane stays pinned to the bottom with the new
// line on screen, rather than drifting up until the cursor moves.
func TestTranslateLastMessageStaysPinned(t *testing.T) {
	m := sizedModel(t, 30)
	if m.selected != 29 {
		t.Fatalf("precondition: selected = %d, want 29 (last message)", m.selected)
	}
	if !m.viewport.AtBottom() {
		t.Fatal("precondition: the pane should start pinned to the bottom")
	}

	const marker = "TRANSLATED-LAST"
	updated, _ := m.Update(translatedMsg{
		id: m.messages[29].ID,
		t:  cosmo.Translation{TranslatedContent: marker},
	})
	m = updated.(Model)

	if m.selected != 29 {
		t.Fatalf("selection moved to %d during translate", m.selected)
	}
	if !m.viewport.AtBottom() {
		t.Fatal("viewport drifted off the bottom after translating the last message")
	}
	if !strings.Contains(m.viewport.View(), marker) {
		t.Fatal("the translation should be on screen immediately after translating")
	}
}

// olderPage builds n messages timestamped before everything sizedModel makes.
func olderPage(n int) []cosmo.Message {
	var msgs []cosmo.Message
	for i := 0; i < n; i++ {
		msgs = append(msgs, cosmo.Message{
			ID:         fmt.Sprintf("old-%02d", i),
			SenderType: "artistMember",
			Content:    fmt.Sprintf("earlier %d", i),
			CreatedAt:  fmt.Sprintf("2026-07-08T06:%02d:00.000Z", i),
		})
	}
	return msgs
}

// TestPrependOlderAnchors checks that an older page lands above the existing
// history, keeps the selection on the same message, and shifts the viewport
// by exactly the added lines so the screen doesn't jump.
func TestPrependOlderAnchors(t *testing.T) {
	m := sizedModel(t, 20)
	m.selected = 5
	m.rerender()
	m.ensureVisible()
	selID := m.messages[m.selected].ID
	offBefore := m.viewport.YOffset()
	linesBefore := m.totalLines

	page := olderPage(5)
	page = append(page, m.messages[0]) // one duplicate: must be dropped
	if got := m.prependOlder(page); got != 5 {
		t.Fatalf("added = %d, want 5", got)
	}
	if m.messages[0].ID != "old-00" || m.messages[5].ID != "msg-00" {
		t.Fatalf("unexpected order after prepend: %s, %s", m.messages[0].ID, m.messages[5].ID)
	}
	if m.messages[m.selected].ID != selID {
		t.Fatalf("selection moved from %s to %s", selID, m.messages[m.selected].ID)
	}
	wantOff := offBefore + (m.totalLines - linesBefore)
	if m.viewport.YOffset() != wantOff {
		t.Fatalf("YOffset = %d, want %d", m.viewport.YOffset(), wantOff)
	}
}

// TestLoadOlderGuards checks that k at the top fires nothing while a page is
// in flight or once history is exhausted.
func TestLoadOlderGuards(t *testing.T) {
	m := sizedModel(t, 5)
	m.memberID = 7 // an open channel, so only the flags can block the fetch
	m.historyEnd = true
	m.welcomeDone = true // else k at the top goes after the greeting instead
	updated, cmd := m.loadOlder()
	m = updated.(Model)
	// loadingOlder is what a real fetch sets; the command here is the notice's
	// expiry tick, not a page request.
	if m.loadingOlder {
		t.Fatal("expected no fetch once history is exhausted")
	}
	if m.status != "beginning of history" || cmd == nil {
		t.Fatalf("status = %q (tick: %v), want an expiring end-of-history notice", m.status, cmd != nil)
	}
	m.historyEnd = false
	m.loadingOlder = true
	if _, cmd = m.loadOlder(); cmd != nil {
		t.Fatal("expected no fetch while one is already in flight")
	}
}

// TestOlderLoadedEmptyMarksEnd checks that an empty older page flips
// historyEnd so further k presses stop hitting the API.
func TestOlderLoadedEmptyMarksEnd(t *testing.T) {
	m := sizedModel(t, 5)
	m.loadingOlder = true
	updated, _ := m.Update(olderLoadedMsg{memberID: m.memberID, messages: nil})
	m = updated.(Model)
	if m.loadingOlder {
		t.Fatal("loadingOlder should clear when the page lands")
	}
	if !m.historyEnd {
		t.Fatal("an empty page should mark the end of history")
	}
	if m.status != "beginning of history" {
		t.Fatalf("status = %q, want the end-of-history notice", m.status)
	}
}

// TestStashChannel checks that leaving a channel snapshots its state under
// its member id.
func TestStashChannel(t *testing.T) {
	// Enough messages to overflow the viewport: SetYOffset clamps to the
	// content, so a channel can only be scrolled up once there is scrollback.
	m := sizedModel(t, 40)
	m.memberID = 7
	m.selected = 1
	m.historyEnd = true
	m.viewport.SetYOffset(4) // pretend the channel was scrolled up a bit
	if got := m.viewport.YOffset(); got != 4 {
		t.Fatalf("viewport did not scroll: YOffset = %d, want 4", got)
	}
	m.translations["msg-00"] = cosmo.Translation{TranslatedContent: "hi"}
	m.stashChannel()
	st, ok := m.cache[7]
	if !ok {
		t.Fatal("expected channel 7 stashed")
	}
	if len(st.messages) != 40 || st.selected != 1 || !st.historyEnd {
		t.Fatalf("stashed state wrong: %+v", st)
	}
	if st.yOffset != 4 {
		t.Fatalf("stashed yOffset = %d, want 4", st.yOffset)
	}
	if st.translations["msg-00"].TranslatedContent != "hi" {
		t.Fatal("translations not stashed")
	}
}

// TestSentMsgStaysInItsChannel checks that a reply whose response arrives after
// the user has switched channels is dropped rather than appended to (and cached
// under) whichever channel is open by then.
func TestSentMsgStaysInItsChannel(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 7
	m.channels = []cosmo.Channel{
		{MemberID: 7, MemberName: "Nien", IsConnected: true},
		{MemberID: 9, MemberName: "Kotone", IsConnected: true},
	}
	before := len(m.messages)

	// The reply was composed in channel 7; the user is now in channel 9.
	m.memberID = 9
	reply := cosmo.Message{ID: "reply-1", SenderType: "user", Content: "hi",
		CreatedAt: "2026-07-08T09:00:00.000Z"}
	updated, _ := m.Update(sentMsg{memberID: 7, msg: reply})
	m = updated.(Model)

	if len(m.messages) != before {
		t.Fatalf("reply for channel 7 landed in channel 9: %d messages, want %d",
			len(m.messages), before)
	}
	if m.seen["reply-1"] {
		t.Fatal("reply for channel 7 marked seen in channel 9")
	}
	for _, ch := range m.channels {
		if ch.MemberID == 9 && ch.LastMessage == "hi" {
			t.Fatal("reply for channel 7 became channel 9's sidebar preview")
		}
	}

	// Back in its own channel the same message is taken.
	m.memberID = 7
	updated, _ = m.Update(sentMsg{memberID: 7, msg: reply})
	m = updated.(Model)
	if !m.seen["reply-1"] {
		t.Fatal("reply not appended to its own channel")
	}
}

// TestAutoTranslateToggle checks that a flips auto-translate on and off, and
// that the [on]/[off] indicator in the hint line tracks it.
func TestAutoTranslateToggle(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 7
	m.mode = modeNormal

	keyA := tea.KeyPressMsg{Code: 'a', Text: "a"}
	updated, _ := m.Update(keyA)
	m = updated.(Model)
	if !m.autoTranslate {
		t.Fatal("a should turn auto-translate on")
	}
	if !strings.Contains(m.render(), "a: auto [on]") {
		t.Fatal("expected the [on] indicator while auto-translate is on")
	}

	updated, _ = m.Update(keyA)
	m = updated.(Model)
	if m.autoTranslate {
		t.Fatal("a again should turn auto-translate off")
	}
	if !strings.Contains(m.render(), "a: auto [off]") {
		t.Fatal("expected the [off] indicator while auto-translate is off")
	}
}

// TestQueueAutoTranslate checks that only fresh artist messages with content
// are queued, and only while the toggle is on.
func TestQueueAutoTranslate(t *testing.T) {
	m := sizedModel(t, 3)
	artist := cosmo.Message{ID: "new-artist", SenderType: "artistMember", Content: "안녕"}

	if cmd := m.queueAutoTranslate(artist); cmd != nil || len(m.transPending) != 0 {
		t.Fatal("nothing should queue while auto-translate is off")
	}

	m.autoTranslate = true
	if cmd := m.queueAutoTranslate(cosmo.Message{ID: "own", SenderType: "user", Content: "hi"}); cmd != nil {
		t.Fatal("user messages should not queue")
	}
	if cmd := m.queueAutoTranslate(cosmo.Message{ID: "img", SenderType: "artistMember"}); cmd != nil {
		t.Fatal("messages without text content should not queue")
	}
	m.translations["done"] = cosmo.Translation{TranslatedContent: "hi"}
	if cmd := m.queueAutoTranslate(cosmo.Message{ID: "done", SenderType: "artistMember", Content: "x"}); cmd != nil {
		t.Fatal("already-translated messages should not queue")
	}
	if len(m.transPending) != 0 {
		t.Fatalf("pending should still be empty, got %v", m.transPending)
	}

	if cmd := m.queueAutoTranslate(artist); cmd == nil {
		t.Fatal("a fresh artist message should fire a request")
	}
	if _, pending := m.transPending["new-artist"]; !pending {
		t.Fatal("the queued message should be marked pending")
	}
	if cmd := m.queueAutoTranslate(artist); cmd != nil {
		t.Fatal("a repeat of a pending message should not fire again")
	}
}

// TestAutoTranslateIsSilent checks that auto-translated arrivals stay off the
// status line: they fire on every incoming artist message, so a progress notice
// per arrival would leave the line churning. A user-asked-for batch running
// alongside them keeps its own count, unpadded by their work.
func TestAutoTranslateIsSilent(t *testing.T) {
	m := sizedModel(t, 5)
	m.memberID = 7
	m.autoTranslate = true

	first := cosmo.Message{ID: "arrival-1", SenderType: "artistMember", Content: "안녕"}
	if cmd := m.queueAutoTranslate(first); cmd == nil {
		t.Fatal("a fresh artist message should fire a request")
	}
	if got := m.render(); strings.Contains(got, "translating") {
		t.Fatalf("auto-translate should show no progress, got %q", got)
	}

	// Nor should it report an empty-handed result, the one notice a t would get.
	updated, _ := m.Update(translatedMsg{memberID: 7, id: "arrival-1"})
	m = updated.(Model)
	if m.status != "" {
		t.Fatalf("status = %q, want empty for an auto-translated result", m.status)
	}

	// A T batch alongside an auto arrival counts only its own messages.
	if cmd := m.queueAutoTranslate(cosmo.Message{ID: "arrival-2", SenderType: "artistMember", Content: "안녕"}); cmd == nil {
		t.Fatal("the second arrival should fire a request")
	}
	m.viewport.GotoBottom()
	want := fmt.Sprintf("translating… (%d left)", len(m.visibleIndices()))
	updated, _ = m.translateVisible()
	m = updated.(Model)
	if got := m.render(); !strings.Contains(got, want) {
		t.Fatalf("render = %q, want %q (the auto arrival must not pad the count)", got, want)
	}
}

// TestQueueAutoTranslateChainsInFlight checks that a new arrival queues behind
// a running request instead of firing a concurrent one.
func TestQueueAutoTranslateChainsInFlight(t *testing.T) {
	m := sizedModel(t, 3)
	m.autoTranslate = true
	m.transPending = map[string]transPend{"msg-00": {}} // in flight, queue empty

	cmd := m.queueAutoTranslate(cosmo.Message{ID: "new-artist", SenderType: "artistMember", Content: "안녕"})
	if cmd != nil {
		t.Fatal("expected no new command while a request is in flight")
	}
	if len(m.transQueue) != 1 || m.transQueue[0].id != "new-artist" {
		t.Fatalf("queue = %v, want [new-artist]", m.transQueue)
	}
}

// bgState returns an empty cached channelState (maps initialized) for a
// backgrounded channel in tests.
func bgState() *channelState {
	return &channelState{
		seen:         map[string]bool{},
		translations: map[string]cosmo.Translation{},
	}
}

// TestApplyBackgroundSyncQueuesTranslations checks that a backgrounded channel's
// new messages merge into its cache and only its artist messages get queued for
// translation, tagged to that member.
func TestApplyBackgroundSyncQueuesTranslations(t *testing.T) {
	m := sizedModel(t, 1) // open channel is member 0
	m.autoTranslate = true
	m.cache[42] = bgState()
	arrivals := []cosmo.Message{
		{ID: "bg-art", SenderType: "artistMember", Content: "안녕", CreatedAt: "2026-07-08T08:00:00.000Z"},
		{ID: "bg-usr", SenderType: "user", Content: "hi", CreatedAt: "2026-07-08T08:01:00.000Z"},
	}
	cmd := m.applyBackgroundSync(bgSyncMsg{memberID: 42, messages: arrivals})
	if cmd == nil {
		t.Fatal("expected a translation command for the new artist message")
	}
	st := m.cache[42]
	if len(st.messages) != 2 {
		t.Fatalf("cached messages = %d, want 2 (both merged)", len(st.messages))
	}
	// The single artist message is in flight (queue drains one immediately when
	// nothing else is running), tagged to the background member.
	if _, pending := m.transPending["bg-art"]; !pending {
		t.Fatal("artist message should be pending")
	}
	if _, pending := m.transPending["bg-usr"]; pending {
		t.Fatal("user message must not be queued for translation")
	}
}

// TestApplyBackgroundSyncIgnoresOpenChannel checks that a sync landing for the
// now-open channel is dropped (its live stream owns it), avoiding a stashed-slice
// divergence from the open message list.
func TestApplyBackgroundSyncIgnoresOpenChannel(t *testing.T) {
	m := sizedModel(t, 1)
	m.memberID = 42
	m.cache[42] = bgState()
	cmd := m.applyBackgroundSync(bgSyncMsg{
		memberID: 42,
		messages: []cosmo.Message{{ID: "x", SenderType: "artistMember", Content: "hi"}},
	})
	if cmd != nil || len(m.cache[42].messages) != 0 {
		t.Fatal("sync for the open channel should be ignored")
	}
}

// TestTranslatedMsgRoutesToBackgroundCache checks that a translation for a
// non-open channel stashes into that channel's cache, not the open channel's map.
func TestTranslatedMsgRoutesToBackgroundCache(t *testing.T) {
	m := sizedModel(t, 1) // open channel is member 0
	m.cache[42] = bgState()
	m.transPending = map[string]transPend{"bg-art": {memberID: 9}}
	updated, _ := m.Update(translatedMsg{memberID: 42, id: "bg-art", t: cosmo.Translation{TranslatedContent: "hello"}})
	m = updated.(Model)
	if _, ok := m.translations["bg-art"]; ok {
		t.Fatal("background translation must not land in the open channel's map")
	}
	if got := m.cache[42].translations["bg-art"].TranslatedContent; got != "hello" {
		t.Fatalf("cache translation = %q, want hello", got)
	}
	if _, pending := m.transPending["bg-art"]; pending {
		t.Fatal("pending should be cleared")
	}
}

// tokenless builds a client that openChannel can be handed: EnsureToken fails
// on empty credentials, so the streams it starts never reach the network.
func tokenless() *cosmo.Client { return cosmo.New(config.Credentials{}, nil) }

// TestBatchSurvivesChannelSwitch covers a T followed by an immediate channel
// switch. The batch's entries name their own channel, so it must keep draining
// into that channel's cache rather than being dropped; its progress line must
// not follow the user to the new channel, and must come back on return.
func TestBatchSurvivesChannelSwitch(t *testing.T) {
	m := sizedModel(t, 5)
	m.client = tokenless()
	m.memberID = 7
	m.viewport.GotoBottom()

	updated, _ := m.translateVisible()
	m = updated.(Model)
	if got := m.render(); !strings.Contains(got, "translating… (5 left)") {
		t.Fatalf("expected batch progress after T, got %q", got)
	}
	queued := len(m.transQueue)

	updated, _ = m.openChannel(9, "Kotone", "Kotone")
	m = updated.(Model)
	defer m.msgStream.Close()
	if got := m.render(); strings.Contains(got, "translating") {
		t.Fatalf("the batch's progress line followed the switch: %q", got)
	}
	if len(m.transQueue) != queued {
		t.Fatalf("queue = %d, want %d: the batch must keep draining", len(m.transQueue), queued)
	}

	// A result for the backgrounded batch stashes and chains the next request,
	// all without touching the open channel's status line.
	updated, cmd := m.Update(translatedMsg{memberID: 7, id: "msg-04", t: cosmo.Translation{TranslatedContent: "hi"}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected the backgrounded batch to chain its next request")
	}
	if got := m.cache[7].translations["msg-04"].TranslatedContent; got != "hi" {
		t.Fatalf("cached translation = %q, want hi", got)
	}
	if got := m.render(); strings.Contains(got, "translating") {
		t.Fatalf("background progress leaked into the open channel: %q", got)
	}

	updated, _ = m.openChannel(7, "Nien", "Nien")
	m = updated.(Model)
	defer m.msgStream.Close()
	if got := m.render(); !strings.Contains(got, "translating… (") {
		t.Fatalf("expected the remaining count on return, got %q", got)
	}
}

// TestSelectedTranslateSurvivesChannelSwitch is the single-message analogue: a
// slow t whose channel is left before it answers must not strand "translating…",
// and its failure belongs to the channel that asked for it, not the open one.
func TestSelectedTranslateSurvivesChannelSwitch(t *testing.T) {
	m := sizedModel(t, 5)
	m.client = tokenless()
	m.memberID = 7

	updated, _ := m.translateSelected()
	m = updated.(Model)
	if got := m.render(); !strings.Contains(got, "translating…") {
		t.Fatalf("expected progress after t, got %q", got)
	}
	if strings.Contains(m.render(), "left)") {
		t.Fatal("a lone t should not count down like a batch")
	}

	updated, _ = m.openChannel(9, "Kotone", "Kotone")
	m = updated.(Model)
	defer m.msgStream.Close()
	if got := m.render(); strings.Contains(got, "translating") {
		t.Fatalf("single-message progress followed the switch: %q", got)
	}

	updated, _ = m.Update(transFailedMsg{memberID: 7, id: "msg-04", err: errors.New("boom")})
	m = updated.(Model)
	if m.err != nil {
		t.Fatalf("a failure from the left channel surfaced here: %v", m.err)
	}
	if _, pending := m.transPending["msg-04"]; pending {
		t.Fatal("a failed request must drop its pending entry, or it pins the progress line")
	}
}

// TestChannelPreview covers the sidebar's one-line summary: attachment
// messages carry no content, so they'd otherwise leave the row blank.
func TestChannelPreview(t *testing.T) {
	tests := []struct {
		name          string
		last, msgType string
		want          string
	}{
		{"text", "안녕", "text", "안녕"},
		{"sticker", "", "sticker", "(sticker)"},
		{"sticker with text", "핑크 닿자", "sticker_text", "핑크 닿자"},
		{"photo", "", "image", "(photo)"},
		{"video", "", "video", "(video)"},
		{"audio", "", "audio", "(audio)"},
		// A channel the artist has never posted in has no last message at all,
		// and must stay blank rather than claim an attachment.
		{"no messages", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := channelPreview(tt.last, tt.msgType); got != tt.want {
				t.Errorf("channelPreview(%q, %q) = %q, want %q", tt.last, tt.msgType, got, tt.want)
			}
		})
	}
}

func TestFmtDuration(t *testing.T) {
	tests := []struct {
		name string
		ms   int
		want string
	}{
		// Kotone's first voice message, the sample this was built against.
		{"voice message", 2115, "0:02"},
		// A clip that exists must never render as 0:00.
		{"sub-second", 400, "0:01"},
		{"minutes", 65000, "1:05"},
		{"past an hour", 3725000, "1:02:05"},
		// Stills, and any feed that omits the metadata, suppress the suffix.
		{"absent", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmtDuration(tt.ms); got != tt.want {
				t.Errorf("fmtDuration(%d) = %q, want %q", tt.ms, got, tt.want)
			}
		})
	}
}

// A sticker_text message carries both a sticker and text — the only kind that
// does — so its filename must sit below the translation rather than between the
// text and the translation that belongs to it.
func TestStickerTextAttachmentSitsBelowTranslation(t *testing.T) {
	m := New(nil, "tripleS", t.TempDir(), true, false)
	m.memberOrig = "JooBin"
	msg := cosmo.Message{
		ID: "26896", Type: "sticker_text", SenderType: "artistMember",
		SenderName: "JooBin", Content: "나도 지금",
		StickerURL: "https://resources.cosmo.fans/images/stickers/9a2151f2.png",
		CreatedAt:  "2026-07-27T08:33:21.470Z",
	}
	m.messages = []cosmo.Message{msg}
	m.translations = map[string]cosmo.Translation{
		"26896": {MessageID: "26896", TranslatedContent: "Me too, right now"},
	}

	lines := strings.Split(m.renderMessage(msg), "\n")
	idx := func(want string) int {
		for i, ln := range lines {
			if strings.Contains(ln, want) {
				return i
			}
		}
		t.Fatalf("no line containing %q in:\n%s", want, strings.Join(lines, "\n"))
		return -1
	}
	text, translation, sticker := idx("나도 지금"), idx("Me too, right now"), idx("9a2151f2.png")
	if !(text < translation && translation < sticker) {
		t.Errorf("want text < translation < sticker, got %d < %d < %d:\n%s",
			text, translation, sticker, strings.Join(lines, "\n"))
	}
}

// A fan can reply with a sticker and no text, which quotes as a bare arrow
// unless the sticker is named. When the quote does have text, that text wins —
// the sticker is never announced alongside it.
func TestStickerOnlyReplyQuote(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		hasSticker bool
		want       string
	}{
		{"sticker only", "", true, "↳ (sticker)"},
		// Real payloads carry trailing spaces, so blank is not just "".
		{"whitespace only", "  ", true, "↳ (sticker)"},
		{"sticker with text", "난 지금 ", true, "↳ 난 지금"},
		{"text only", "안녕", false, "↳ 안녕"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated, _ := New(nil, "tripleS", t.TempDir(), true, false).
				Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m := updated.(Model)
			m.memberOrig = "Kotone"
			msg := cosmo.Message{
				ID: "26791", Type: "text", SenderType: "artistMember",
				SenderName: "Kotone", Content: "너무 귀여워…",
				CreatedAt: "2026-07-27T09:14:00.000Z",
				Reply:     &cosmo.Reply{ID: "793478", Content: tt.content, HasSticker: tt.hasSticker},
			}
			m.messages = []cosmo.Message{msg}

			quote := strings.Split(m.renderMessage(msg), "\n")[1]
			if got := strings.TrimRight(ansi.Strip(quote), " "); got != tt.want {
				t.Errorf("quote = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUpdateChannelMetaKeepsType checks a newly-arrived attachment repoints the
// sidebar preview instead of leaving the previous message's text stale.
func TestUpdateChannelMetaKeepsType(t *testing.T) {
	m := sizedModel(t, 1)
	m.channels = []cosmo.Channel{
		{MemberID: 25, LastMessage: "안녕", LastMessageType: "text", UnreadCount: 3},
	}
	m.updateChannelMeta(25, cosmo.Message{Type: "sticker", StickerURL: "https://x/1.gif"}, true)

	ch := m.channels[0]
	if ch.UnreadCount != 0 {
		t.Errorf("UnreadCount = %d, want 0", ch.UnreadCount)
	}
	if got := channelPreview(ch.LastMessage, ch.LastMessageType); got != "(sticker)" {
		t.Errorf("preview = %q, want %q", got, "(sticker)")
	}
}

// TestBackgroundSyncCmdsSelectivity checks the diff only syncs cached, non-open
// channels whose last message advanced.
func TestBackgroundSyncCmdsSelectivity(t *testing.T) {
	m := sizedModel(t, 1)
	m.autoTranslate = true
	m.memberID = 7 // channel 7 is open
	old := "2026-07-08T07:00:00.000Z"
	m.channels = []cosmo.Channel{
		{MemberID: 7, IsConnected: true, LastMessageAt: old},
		{MemberID: 42, IsConnected: true, LastMessageAt: old}, // cached, will advance
		{MemberID: 99, IsConnected: true, LastMessageAt: old}, // advances but never opened
	}
	m.cache[7] = bgState()  // open channel is also cached (stashed) but must be skipped
	m.cache[42] = bgState() // eligible

	adv := "2026-07-08T09:00:00.000Z"
	// Only member 42 (cached, non-open, advanced) qualifies -> non-nil batch.
	if cmd := m.backgroundSyncCmds([]cosmo.Channel{
		{MemberID: 7, IsConnected: true, LastMessageAt: adv},
		{MemberID: 42, IsConnected: true, LastMessageAt: adv},
		{MemberID: 99, IsConnected: true, LastMessageAt: adv},
	}); cmd == nil {
		t.Fatal("expected a sync command for the advanced cached channel")
	}

	// No advance anywhere -> nothing to sync.
	if cmd := m.backgroundSyncCmds([]cosmo.Channel{
		{MemberID: 42, IsConnected: true, LastMessageAt: old},
	}); cmd != nil {
		t.Fatal("unchanged channels should not sync")
	}

	// Auto-translate off -> never syncs.
	m.autoTranslate = false
	if cmd := m.backgroundSyncCmds([]cosmo.Channel{
		{MemberID: 42, IsConnected: true, LastMessageAt: adv},
	}); cmd != nil {
		t.Fatal("background sync must not run while auto-translate is off")
	}
}

// TestApplyChannelsFiltersDisconnected checks that only membership-held
// channels reach the sidebar.
func TestApplyChannelsFiltersDisconnected(t *testing.T) {
	m := sizedModel(t, 0)
	m.applyChannels([]cosmo.Channel{
		{MemberID: 1, MemberName: "SeoYeon", IsConnected: true},
		{MemberID: 2, MemberName: "HyeRin", IsConnected: false},
		{MemberID: 3, MemberName: "JiWoo", IsConnected: true},
	}, nil)
	if len(m.channels) != 2 {
		t.Fatalf("channels = %d, want 2 connected", len(m.channels))
	}
	if len(m.sidebar.Items()) != 2 {
		t.Fatalf("sidebar items = %d, want 2", len(m.sidebar.Items()))
	}
	if m.gate != gateNone {
		t.Fatalf("gate = %d, want gateNone", m.gate)
	}
}

// TestSidebarReorderLocksOpenChannel checks the cursor across a sidebar
// reorder (new activity resorts by recency): with the chat pane focused it
// re-locks onto the open channel's row, and with the sidebar focused it keeps
// its index so it doesn't jump mid-navigation.
func TestSidebarReorderLocksOpenChannel(t *testing.T) {
	chans := func(ts ...string) []cosmo.Channel {
		return []cosmo.Channel{
			{MemberID: 1, MemberName: "SeoYeon", IsConnected: true, LastMessageAt: ts[0]},
			{MemberID: 2, MemberName: "HyeRin", IsConnected: true, LastMessageAt: ts[1]},
			{MemberID: 3, MemberName: "JiWoo", IsConnected: true, LastMessageAt: ts[2]},
		}
	}
	t1, t2, t3 := "2026-07-08T07:01:00.000Z", "2026-07-08T07:02:00.000Z", "2026-07-08T07:03:00.000Z"

	m := sizedModel(t, 0)
	m.applyChannels(chans(t3, t2, t1), nil) // order: SeoYeon, HyeRin, JiWoo
	m.memberID = 2                          // HyeRin's chat is open…
	m.setMode(modeNormal)                   // …and the chat pane has focus
	m.sidebar.Select(1)                     // cursor left on HyeRin's row

	// SeoYeon's activity keeps her on top; HyeRin stays at index 1: locking is a
	// no-op when the open channel doesn't move.
	m.applyChannels(chans(t3+"9", t2, t1), nil)
	if got := m.sidebar.SelectedItem().(channelItem).ch.MemberID; got != 2 {
		t.Fatalf("selected member = %d, want 2 (unmoved open channel)", got)
	}

	// JiWoo's new message resorts her above HyeRin; the cursor must follow the
	// open channel to its new row, not stay at the old index.
	m.applyChannels(chans(t3, t2, t3+"9"), nil) // order: JiWoo, SeoYeon, HyeRin
	if got := m.sidebar.SelectedItem().(channelItem).ch.MemberID; got != 2 {
		t.Fatalf("selected member = %d, want 2 (open channel after reorder)", got)
	}
	if got := m.sidebar.Index(); got != 2 {
		t.Fatalf("cursor index = %d, want 2 (HyeRin's new row)", got)
	}

	// With the sidebar itself focused, a reorder keeps the cursor by index so it
	// doesn't jump around mid-navigation.
	m.setMode(modeSidebar)
	m.sidebar.Select(1)                          // browsing: cursor on SeoYeon's row
	m.applyChannels(chans(t3+"99", t2, t1), nil) // reorders to SeoYeon, HyeRin, JiWoo
	if got := m.sidebar.Index(); got != 1 {
		t.Fatalf("cursor index = %d, want 1 (index kept while sidebar focused)", got)
	}
}

// TestNicknameOption checks the nicknames name selection: on shows the
// artist-set nickname (the API's artistMemberName/senderName), off swaps in
// the real name (originalName) in both the sidebar and message headers.
func TestNicknameOption(t *testing.T) {
	ch := cosmo.Channel{MemberID: 1, MemberName: "윤서연", OriginalName: "SeoYeon", IsConnected: true}
	msg := cosmo.Message{
		ID: "m1", SenderType: "artistMember", SenderID: 1, SenderName: "윤서연",
		Content: "hi", CreatedAt: "2026-07-14T11:00:00.000Z",
	}

	m := sizedModel(t, 0) // nickname on
	m.applyChannels([]cosmo.Channel{ch}, nil)
	if got := m.sidebar.Items()[0].(channelItem).name; got != "윤서연" {
		t.Fatalf("sidebar name = %q, want the nickname", got)
	}
	m.memberID, m.memberName = 1, m.channelName(ch)
	if r := m.renderMessage(msg); !strings.Contains(r, "윤서연") {
		t.Fatalf("rendered %q, want the nickname", r)
	}

	m = sizedModel(t, 0)
	m.nickname = false
	m.applyChannels([]cosmo.Channel{ch}, nil)
	if got := m.sidebar.Items()[0].(channelItem).name; got != "SeoYeon" {
		t.Fatalf("sidebar name = %q, want the real name", got)
	}
	m.memberID, m.memberName = 1, m.channelName(ch)
	if r := m.renderMessage(msg); !strings.Contains(r, "SeoYeon") || strings.Contains(r, "윤서연") {
		t.Fatalf("rendered %q, want the real name only", r)
	}

	// Channels without originalName (older API shape) keep the member name.
	if got := m.channelName(cosmo.Channel{MemberName: "Xinyu"}); got != "Xinyu" {
		t.Fatalf("channelName = %q, want fallback to member name", got)
	}

	// origName is the real name whatever the option says, so download paths
	// stay stable across nickname changes.
	if got := origName(ch); got != "SeoYeon" {
		t.Fatalf("origName = %q, want the real name", got)
	}
	if got := origName(cosmo.Channel{MemberName: "Xinyu"}); got != "Xinyu" {
		t.Fatalf("origName = %q, want fallback to member name", got)
	}
}

// TestChannelNameNormalizesWidth checks that the sidebar display name runs
// through textfmt.Normalize, so a nickname carrying a VS16 emoji selector (which
// lipgloss measures a column wider than a terminal drawing the base glyph) is
// stripped to its base form. Left unnormalized, the padded sidebar column
// renders a column short and slides the divider border off the row.
func TestChannelNameNormalizesWidth(t *testing.T) {
	const nick = "김유연♥️" // BLACK HEART SUIT + VARIATION SELECTOR-16
	const want = "김유연♥"  // VS16 stripped
	ch := cosmo.Channel{MemberID: 1, MemberName: nick, OriginalName: nick, IsConnected: true}

	m := sizedModel(t, 0)
	m.applyChannels([]cosmo.Channel{ch}, nil)
	got := m.sidebar.Items()[0].(channelItem).name
	if got != want {
		t.Fatalf("sidebar name = %q, want %q (VS16 stripped)", got, want)
	}
	if w := lipgloss.Width(got); w != lipgloss.Width(want) {
		t.Fatalf("sidebar name width = %d, want %d", w, lipgloss.Width(want))
	}
	if got := origName(ch); got != want {
		t.Fatalf("origName = %q, want %q (VS16 stripped)", got, want)
	}
}

// TestMultilineReplyKeepsNewlines checks that a quoted reply renders one quoted
// line per source line (ASCII art relies on this), dropping trailing newlines.
func TestMultilineReplyKeepsNewlines(t *testing.T) {
	m := sizedModel(t, 0)
	msg := cosmo.Message{
		ID: "m1", SenderType: "artistMember", SenderName: "Nien",
		Content:   "알겠어",
		CreatedAt: "2026-07-14T14:49:41.684Z",
		Reply:     &cosmo.Reply{ID: "r1", Content: "first\n  second\nthird\n"},
	}
	r := m.renderMessage(msg)
	lines := strings.Split(r, "\n")
	var quoted []string
	for _, ln := range lines {
		if strings.Contains(ln, "↳") || (len(quoted) > 0 && !strings.Contains(ln, "알겠어")) {
			quoted = append(quoted, ln)
		}
	}
	if len(quoted) != 3 {
		t.Fatalf("quoted lines = %d, want 3:\n%s", len(quoted), r)
	}
	if !strings.Contains(quoted[0], "↳ first") {
		t.Fatalf("first quoted line %q, want ↳ prefix", quoted[0])
	}
	if !strings.Contains(quoted[1], "  second") || strings.Contains(quoted[1], "↳") {
		t.Fatalf("continuation %q, want indent without ↳", quoted[1])
	}
}

// TestApplyChannelsAllDisconnectedGate checks that a successful load with no
// connected channel gates the page behind the subscription notice.
func TestApplyChannelsAllDisconnectedGate(t *testing.T) {
	m := sizedModel(t, 0)
	m.applyChannels([]cosmo.Channel{
		{MemberID: 1, MemberName: "SeoYeon", IsConnected: false},
		{MemberID: 2, MemberName: "HyeRin", IsConnected: false},
	}, nil)
	if len(m.channels) != 0 {
		t.Fatalf("channels = %d, want 0", len(m.channels))
	}
	if m.gate != gateNoSubscription {
		t.Fatalf("gate = %d, want gateNoSubscription", m.gate)
	}
	view := m.render()
	if !strings.Contains(view, "subscription") {
		t.Fatal("expected the subscription notice in the view")
	}
	if strings.Contains(view, "error:") {
		t.Fatal("the gate should not render as an error")
	}
}

// TestChannelsLoadedInvalidArtistNotice checks that the invalid-artist error
// code gates the page with a friendly notice instead of the raw red error, and
// leaves the channel-list SSE stream unopened.
func TestChannelsLoadedInvalidArtistNotice(t *testing.T) {
	m := sizedModel(t, 0)
	err := &cosmo.APIError{Status: 400, Code: cosmo.CodeInvalidArtist, Message: "invalid artist"}
	updated, cmd := m.Update(channelsLoadedMsg{err: err})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("no SSE stream should open when the load fails")
	}
	if m.gate != gateNoTalk {
		t.Fatalf("gate = %d, want gateNoTalk", m.gate)
	}
	if m.err != nil {
		t.Fatalf("err should stay unset, got %v", m.err)
	}
	view := m.render()
	if !strings.Contains(view, "isn't available") {
		t.Fatal("expected the availability notice in the view")
	}
	if strings.Contains(view, "error:") {
		t.Fatal("the gate should not render as an error")
	}
}

// TestChannelsLoadedOtherErrorSticky checks that other load failures keep the
// existing sticky status-line error and do not gate the page.
func TestChannelsLoadedOtherErrorShownThenCleared(t *testing.T) {
	m := sizedModel(t, 0)
	updated, cmd := m.Update(channelsLoadedMsg{err: errors.New("boom")})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("no SSE stream should open when the load fails")
	}
	if m.err == nil {
		t.Fatal("expected the error stored")
	}
	if m.gate != gateNone {
		t.Fatalf("gate = %d, want gateNone", m.gate)
	}
	if !strings.Contains(m.render(), "error:") {
		t.Fatal("expected the error in the status line")
	}
	// Errors are transient: a later successful list load (e.g. an SSE-triggered
	// refresh) clears the notice instead of leaving it sticky.
	m.applyChannels(nil, nil)
	if m.err != nil {
		t.Fatalf("expected the error cleared on a successful load, got %v", m.err)
	}
}

// TestReloadRecoversFailedChannelLoad checks that r refetches the channel list.
// The list stream only opens on a load that succeeds, so a page whose first load
// failed has nothing left that would refetch on its own; without r it would stay
// empty for the rest of the session.
func TestReloadRecoversFailedChannelLoad(t *testing.T) {
	m := sizedModel(t, 0)
	updated, _ := m.Update(channelsLoadedMsg{err: errors.New("boom")})
	m = updated.(Model)
	if m.listStream != nil {
		t.Fatal("no list stream should be open after a failed load")
	}

	keyR := tea.KeyPressMsg{Code: 'r', Text: "r"}
	updated, cmd := m.Update(keyR)
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("r should issue a reload after a failed load")
	}
	// A reload that changes nothing changes nothing on screen, so the status
	// line has to say one is running or the key looks inert.
	if !strings.Contains(m.render(), "reloading…") {
		t.Fatalf("expected the reload to announce itself:\n%s", m.render())
	}
	// A non-nil stream so the successful load doesn't try to open one (these
	// tests carry no client).
	m.listStream = &cosmo.Stream{}
	updated, _ = m.Update(channelsLoadedMsg{channels: []cosmo.Channel{
		{MemberID: 7, MemberName: "Nien", IsConnected: true},
	}})
	m = updated.(Model)
	if m.reloading {
		t.Fatal("the reload notice should clear when the list lands")
	}
	// The hint returns, so the retry stays discoverable.
	if !strings.Contains(m.render(), "r: reload") {
		t.Fatalf("expected the reload hint in the status line:\n%s", m.render())
	}

	// While the sidebar filter is capturing text, r is a character, not a reload.
	m.setMode(modeSidebar)
	updated, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(Model)
	if !m.AcceptsText() {
		t.Fatal("expected the sidebar filter to capture text")
	}
	updated, _ = m.Update(keyR)
	m = updated.(Model)
	if m.sidebar.FilterValue() != "r" {
		t.Fatalf("r should have typed into the filter, got %q", m.sidebar.FilterValue())
	}
}

// TestErrorRendersOneLineAndKeyDismisses feeds a multi-line error (a gateway
// 504 body is HTML several lines high) and checks the frame doesn't grow — a
// taller frame scrolls the whole interface off-screen — and that any keypress
// dismisses the notice.
func TestErrorRendersOneLineAndKeyDismisses(t *testing.T) {
	m := sizedModel(t, 3)
	before := strings.Count(m.render(), "\n")
	updated, _ := m.Update(errMsg{errors.New("line1\nline2\nline3")})
	m = updated.(Model)
	if got := strings.Count(m.render(), "\n"); got != before {
		t.Fatalf("view height changed with a multi-line error: %d lines, want %d", got+1, before+1)
	}
	if !strings.Contains(m.render(), "error:") {
		t.Fatal("expected the error in the status line")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m = updated.(Model)
	if m.err != nil {
		t.Fatalf("expected a keypress to dismiss the error, got %v", m.err)
	}
}

// TestStatusLineKeepsRowSlack checks that a long status (the batch-translate
// counter, a verbose error) never widens a frame row to the full terminal
// width: every row keeps one column of slack, which absorbs characters the
// terminal renders a column wider than lipgloss measures (see textfmt) instead
// of wrapping and desyncing the renderer.
func TestStatusLineKeepsRowSlack(t *testing.T) {
	const w = 159
	m := New(nil, "tripleS", ".", true, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 52})
	m = updated.(Model)
	m.memberID = 27
	m.appendMessages([]cosmo.Message{{
		ID: "a", SenderType: "artistMember", Content: "hi",
		CreatedAt: "2026-07-14T18:35:00.733Z",
	}}, true)
	for _, status := range []string{
		"translating… (12 left)",
		strings.Repeat("very long status ", 20),
	} {
		m.status = status
		for i, ln := range strings.Split(m.render(), "\n") {
			if lw := lipgloss.Width(ln); lw >= w {
				t.Errorf("status %q: row %d is %d cols, want < %d", status, i, lw, w)
			}
		}
	}
	m.status = ""
	updated, _ = m.Update(errMsg{errors.New(strings.Repeat("api unreachable ", 20))})
	m = updated.(Model)
	for i, ln := range strings.Split(m.render(), "\n") {
		if lw := lipgloss.Width(ln); lw >= w {
			t.Errorf("long error: row %d is %d cols, want < %d", i, lw, w)
		}
	}
}

// TestSwitchGroupClearsGate checks that cycling artists resets the gate and any
// shown error so one group's state never bleeds into the next.
func TestSwitchGroupClearsGate(t *testing.T) {
	m := sizedModel(t, 0)
	m.gate = gateNoTalk
	m.err = errors.New("stale")
	m.status = "stale"
	updated, cmd := m.Update(uimsg.GroupChanged{Group: "artms"})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected a channel reload command")
	}
	if m.gate != gateNone || m.err != nil || m.status != "" {
		t.Fatalf("state not reset: gate=%d err=%v status=%q", m.gate, m.err, m.status)
	}
	if strings.Contains(m.render(), "isn't available") {
		t.Fatal("the old gate notice should be gone after switching groups")
	}
}

// TestOlderPageMediaUpgrade checks that an older page containing media fires
// a cursored originals fetch, and a text-only page fires nothing.
func TestOlderPageMediaUpgrade(t *testing.T) {
	m := sizedModel(t, 5)
	m.loadingOlder = true
	updated, cmd := m.Update(olderLoadedMsg{memberID: m.memberID, messages: olderPage(3)})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("a text-only page should not fetch media originals")
	}

	page := []cosmo.Message{{
		ID:         "old-img",
		SenderType: "artistMember",
		Type:       "image",
		MediaURL:   "https://example.com/thumb.jpg",
		CreatedAt:  "2026-07-08T05:00:00.000Z",
	}}
	m.loadingOlder = true
	if _, cmd = m.Update(olderLoadedMsg{memberID: m.memberID, messages: page}); cmd == nil {
		t.Fatal("a page with media should fetch cursored originals")
	}
}

// TestDownloadSelectedMedia covers the no-media guard and the skip-if-present
// path (the actual transfer needs a live client).
func TestDownloadSelectedMedia(t *testing.T) {
	m := New(nil, "tripleS", t.TempDir(), true, false)
	m.memberName = "소현" // the download dir must come from memberOrig, not the display name
	m.memberOrig = "SoHyun"
	m.messages = []cosmo.Message{
		{ID: "txt", Type: "text", Content: "hi"},
		{ID: "img", Type: "image", MediaURL: "https://example.com/media/photo.jpg"},
	}

	m.selected = 0
	if cmd := m.downloadSelectedMedia(); cmd != nil {
		t.Fatal("text messages have nothing to download")
	}

	m.selected = 1
	dest := filepath.Join(m.downloadDir, "cosmo-tripleS", "SoHyun", "photo.jpg")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := m.downloadSelectedMedia()()
	done, ok := got.(downloadDoneMsg)
	if !ok || !done.skipped || done.path != dest {
		t.Fatalf("got %#v, want skipped downloadDoneMsg for %s", got, dest)
	}
}

// TestStickerMessage checks a sticker renders as a named attachment rather than
// "(no text)", and that o/D act on it like they do on media.
func TestStickerMessage(t *testing.T) {
	m := New(nil, "tripleS", t.TempDir(), true, false)
	m.memberOrig = "ShiOn"
	sticker := cosmo.Message{
		ID:         "20912",
		Type:       "sticker",
		SenderType: "artistMember",
		SenderName: "쇼니♡",
		StickerURL: "https://resources.cosmo.fans/images/stickers/136.gif",
		CreatedAt:  "2026-07-20T12:20:36.015Z",
	}
	m.messages = []cosmo.Message{sticker}
	m.selected = 0

	block := m.renderMessage(sticker)
	if strings.Contains(block, "(no text)") {
		t.Errorf("sticker rendered as (no text):\n%s", block)
	}
	if !strings.Contains(block, "136.gif") {
		t.Errorf("sticker block missing its filename:\n%s", block)
	}

	if cmd := m.openSelectedMedia(); cmd == nil {
		t.Error("o should open a sticker")
	}

	// Stickers are a global asset pool, so they save to a shared dir rather
	// than the sending member's — otherwise the same file is downloaded once
	// per member who ever sent it.
	dest := filepath.Join(m.downloadDir, "stickers", "136.gif")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := m.downloadSelectedMedia()()
	done, ok := got.(downloadDoneMsg)
	if !ok || done.path != dest {
		t.Fatalf("got %#v, want downloadDoneMsg for %s", got, dest)
	}
	if !done.skipped {
		t.Error("an already-saved sticker should be skipped")
	}

	// The same sticker from a different member's channel resolves to that same
	// shared path, so it is skipped rather than saved a second time.
	m.memberOrig = "DaHyun"
	if got := m.downloadSelectedMedia()(); got != (downloadDoneMsg{path: dest, skipped: true}) {
		t.Fatalf("got %#v, want the sticker skipped at the shared path %s", got, dest)
	}
}

// TestPageSelection checks that d/u move the highlight a viewport-height of
// lines, always at least one message, and clamp at both ends of the history.
func TestPageSelection(t *testing.T) {
	m := sizedModel(t, 30)
	m.selected = 0
	m.rerender()

	m.pageSelection(1)
	if m.selected == 0 {
		t.Fatal("expected the selection to move down a page, still at 0")
	}
	if m.selected >= 29 {
		t.Fatalf("one page must not reach the end of 30 messages, got %d", m.selected)
	}
	// The landing message starts within a viewport-height of the top.
	if m.msgStart[m.selected] > m.viewport.Height() {
		t.Fatalf("landed %d lines down, beyond the %d-line page",
			m.msgStart[m.selected], m.viewport.Height())
	}

	after := m.selected
	m.pageSelection(-1)
	if m.selected >= after {
		t.Fatalf("expected the selection to move back up from %d, got %d", after, m.selected)
	}
	if m.selected != 0 {
		t.Fatalf("a page up from one page down should clamp to 0, got %d", m.selected)
	}

	// Paging down repeatedly clamps at the newest message.
	for i := 0; i < 20; i++ {
		m.pageSelection(1)
	}
	if m.selected != 29 {
		t.Fatalf("expected the selection clamped at 29, got %d", m.selected)
	}
	// And one more page down stays put rather than running off the end.
	m.pageSelection(1)
	if m.selected != 29 {
		t.Fatalf("selection ran past the end: %d", m.selected)
	}
}

// TestPasteIntoReplyBox checks pasted text lands in the reply box at the
// cursor. The chord that reads the clipboard belongs to the shell (see
// internal/tui/clip); the page only ever sees the text.
func TestPasteIntoReplyBox(t *testing.T) {
	m := sizedModel(t, 1)
	m.memberID = 7
	m.setMode(modeInsert)
	m.reply.Focus()

	// The box is one row: a multi-line paste has to arrive flattened.
	updated, _ := m.Update(tea.PasteMsg{Content: "hello\nthere"})
	m = updated.(Model)
	if m.reply.Value() != "hello there" {
		t.Fatalf("reply box = %q, want the pasted text on one line", m.reply.Value())
	}
	updated, _ = m.Update(tea.PasteMsg{Content: "!"})
	m = updated.(Model)
	if m.reply.Value() != "hello there!" {
		t.Fatalf("reply box = %q, want the second paste appended", m.reply.Value())
	}
}

// TestPasteIntoSidebarFilter checks a paste while the channel list is filtering
// goes to the filter box (and asks for the matches) rather than the reply.
func TestPasteIntoSidebarFilter(t *testing.T) {
	m := sizedModel(t, 1)
	m.memberID = 7
	m.setMode(modeSidebar)
	m.sidebar.SetItems([]list.Item{
		channelItem{name: "chaewon"},
		channelItem{name: "yeonjung"},
	})
	updated, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(Model)
	if !m.AcceptsText() {
		t.Fatal("/ should open the channel filter")
	}

	updated, cmd := m.Update(tea.PasteMsg{Content: "chae"})
	m = updated.(Model)
	if got := m.sidebar.FilterInput.Value(); got != "chae" {
		t.Fatalf("filter box = %q, want the pasted text", got)
	}
	if m.reply.Value() != "" {
		t.Fatalf("reply box = %q, want the paste to have gone to the filter", m.reply.Value())
	}
	if cmd == nil {
		t.Fatal("a changed filter should ask for the matches")
	}
	// The matches come back the way a typed filter's do.
	m = runFilter(m, cmd)
	if got := len(m.sidebar.VisibleItems()); got != 1 {
		t.Fatalf("visible channels = %d, want the one match", got)
	}
}

// TestPasteOutsideInsertMode checks pasted text is dropped rather than banked
// when no box is up — insert mode can be left while the read runs.
func TestPasteOutsideInsertMode(t *testing.T) {
	m := sizedModel(t, 1)
	m.memberID = 7 // normal mode

	updated, _ := m.Update(tea.PasteMsg{Content: "hello"})
	m = updated.(Model)
	if m.reply.Value() != "" {
		t.Fatalf("reply box = %q, want nothing pasted outside insert mode", m.reply.Value())
	}
}

// runFilter drains the commands a filter change produces and hands the match
// set back to the page, standing in for the shell's broadcast.
func runFilter(m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	msgs := []tea.Msg{cmd()}
	if batch, ok := msgs[0].(tea.BatchMsg); ok {
		msgs = nil
		for _, c := range batch {
			if c != nil {
				msgs = append(msgs, c())
			}
		}
	}
	for _, msg := range msgs {
		if matches, ok := msg.(list.FilterMatchesMsg); ok {
			updated, _ := m.Update(matches)
			m = updated.(Model)
		}
	}
	return m
}

// limited returns a sized model whose open channel has a known allowance.
func limited(t *testing.T, lim cosmo.ReplyLimits) Model {
	t.Helper()
	m := sizedModel(t, 3)
	m.memberID = 7
	updated, _ := m.Update(limitsMsg{memberID: 7, limits: lim})
	return updated.(Model)
}

func fullAllowance() cosmo.ReplyLimits {
	return cosmo.ReplyLimits{RemainingCount: 5, MaxCount: 5, CanReply: true, TextMaxLength: 200}
}

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want int
	}{
		{"ascii", "hello", 5},
		// Korean syllables are BMP: one code unit each, same as ASCII.
		{"hangul", "안녕하세요", 5},
		// Astral emoji are surrogate pairs, so they cost two apiece — this is
		// where a rune count would disagree and let an over-long reply through.
		{"emoji", "💜💜", 4},
		{"mixed", "hi 💜", 5},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := utf16Len(tt.s); got != tt.want {
				t.Errorf("utf16Len(%q) = %d, want %d", tt.s, got, tt.want)
			}
		})
	}
}

// truncUTF16 must never split a surrogate pair: an emoji that would straddle
// the cap is dropped whole, leaving the value one unit short rather than
// half-encoded.
func TestTruncUTF16(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		limit int
		want  string
	}{
		{"under", "hello", 200, "hello"},
		{"exact", "hello", 5, "hello"},
		{"over", "hello", 3, "hel"},
		{"emoji fits", "💜💜", 4, "💜💜"},
		{"emoji straddles", "💜💜", 3, "💜"},
		{"emoji cut whole", "a💜", 2, "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncUTF16(tt.s, tt.limit)
			if got != tt.want {
				t.Errorf("truncUTF16(%q, %d) = %q, want %q", tt.s, tt.limit, got, tt.want)
			}
			if utf16Len(got) > tt.limit {
				t.Errorf("result %q exceeds the limit %d", got, tt.limit)
			}
		})
	}
}

// The box is clamped to the channel's reported cap, counted in UTF-16 units —
// so 200 emoji (400 units) come back as 100, not 200.
func TestClampReplyToReportedCap(t *testing.T) {
	m := limited(t, fullAllowance())

	m.reply.SetValue(strings.Repeat("a", 260))
	m.clampReply()
	if got := utf16Len(m.reply.Value()); got != 200 {
		t.Fatalf("ascii clamped to %d units, want 200", got)
	}

	m.reply.SetValue(strings.Repeat("💜", 200))
	m.clampReply()
	if got := utf16Len(m.reply.Value()); got != 200 {
		t.Fatalf("emoji clamped to %d units, want 200", got)
	}
	if got := len([]rune(m.reply.Value())); got != 100 {
		t.Fatalf("emoji clamped to %d runes, want 100 (two units each)", got)
	}
}

// With no allowance fetched there is no cap to enforce, and the API stays the
// only authority on length — the box must not invent one.
func TestClampReplyNoLimitsIsNoOp(t *testing.T) {
	m := sizedModel(t, 1)
	m.memberID = 7
	long := strings.Repeat("a", 400)
	m.reply.SetValue(long)
	m.clampReply()
	if m.reply.Value() != long {
		t.Fatalf("clamped to %d chars with no known cap, want untouched", len(m.reply.Value()))
	}
	if m.replyCounter() != "" {
		t.Fatalf("counter = %q, want empty until a cap is known", m.replyCounter())
	}
}

// A cap that lands after the user has already typed still applies.
func TestLimitsArrivingClampsTypedText(t *testing.T) {
	m := sizedModel(t, 1)
	m.memberID = 7
	m.reply.SetValue(strings.Repeat("a", 260))

	updated, _ := m.Update(limitsMsg{memberID: 7, limits: fullAllowance()})
	m = updated.(Model)
	if got := utf16Len(m.reply.Value()); got != 200 {
		t.Fatalf("value = %d units after the cap arrived, want 200", got)
	}
}

// An allowance for a channel the user has already left must not be shown
// against whichever channel is open by then.
func TestLimitsStayInTheirChannel(t *testing.T) {
	m := sizedModel(t, 1)
	m.memberID = 7
	updated, _ := m.Update(limitsMsg{memberID: 9, limits: fullAllowance()})
	m = updated.(Model)
	if m.hasLimits {
		t.Fatal("took an allowance belonging to another channel")
	}
}

func TestReplyCounterCountsUTF16(t *testing.T) {
	m := limited(t, fullAllowance())
	m.reply.SetValue("hi 💜")
	if got := m.replyCounter(); got != "chars [5/200]" {
		t.Fatalf("counter = %q, want chars [5/200]", got)
	}
	// The counter belongs to the composing line, which is where it changes.
	m.setMode(modeInsert)
	if got := m.render(); !strings.Contains(got, "chars [5/200]") {
		t.Fatal("counter not rendered while composing")
	}
}

func TestReplyAllowanceLine(t *testing.T) {
	m := limited(t, cosmo.ReplyLimits{RemainingCount: 3, MaxCount: 5, CanReply: true, TextMaxLength: 200})
	if got := m.replyAllowance(); got != "replies [3/5]" {
		t.Fatalf("allowance = %q, want replies [3/5]", got)
	}
	// The allowance belongs to the composing line, alongside the counter.
	m.setMode(modeInsert)
	if got := m.render(); !strings.Contains(got, "replies [3/5]") {
		t.Fatal("allowance not rendered while composing")
	}
}

// Neither limit says anything actionable with the box closed, so the
// normal-mode line keeps its key hints to itself.
func TestLimitsHiddenOutsideInsertMode(t *testing.T) {
	m := limited(t, cosmo.ReplyLimits{RemainingCount: 3, MaxCount: 5, CanReply: true, TextMaxLength: 200})
	m.reply.SetValue("hi")
	got := m.render()
	if strings.Contains(got, "replies [") {
		t.Error("allowance shown outside insert mode")
	}
	if strings.Contains(got, "chars [") {
		t.Error("counter shown outside insert mode")
	}
	if !strings.Contains(got, "i: reply") {
		t.Error("expected the normal-mode key hints")
	}
}

// Exhausted channels name the reset instead of the count: "0 left" alone
// doesn't tell the user when they can write again.
func TestReplyAllowanceExhaustedNamesReset(t *testing.T) {
	m := limited(t, cosmo.ReplyLimits{
		RemainingCount: 0, MaxCount: 5, CanReply: false,
		NextResetAt: "2026-08-02T09:01:19.188Z", TextMaxLength: 200,
	})
	got := m.replyAllowance()
	if !strings.HasPrefix(got, "replies [0/5, recharges ") || !strings.HasSuffix(got, "]") {
		t.Fatalf("allowance = %q, want the recharge inside the brackets", got)
	}
	if strings.Contains(got, "2026-08-02T09") {
		t.Fatalf("allowance = %q, want a formatted local time not the raw stamp", got)
	}
}

// The reset stamp is rendered in the viewer's zone, not the API's UTC — the
// same treatment message times get.
func TestFmtResetIsLocal(t *testing.T) {
	const utc = "2026-08-02T09:01:19.188Z"
	want := time.Date(2026, 8, 2, 9, 1, 19, 0, time.UTC).Local().Format("Jan 2 15:04")
	if got := fmtReset(utc); got != want {
		t.Errorf("fmtReset(%s) = %q, want %q", utc, got, want)
	}
	if fmtReset("") != "" {
		t.Error("an absent reset should render as nothing, not a zero time")
	}
	if fmtReset("not a timestamp") != "" {
		t.Error("an unparseable reset should render as nothing")
	}
}

// With no window running the API sends a null reset; the line then just says
// there are none left rather than trailing an empty "until".
func TestReplyAllowanceExhaustedWithoutReset(t *testing.T) {
	m := limited(t, cosmo.ReplyLimits{RemainingCount: 0, MaxCount: 5, CanReply: false, TextMaxLength: 200})
	if got := m.replyAllowance(); got != "replies [0/5]" {
		t.Fatalf("allowance = %q, want the bare bracketed count", got)
	}
}

// A send with nothing left never reaches the API, and says nothing about it:
// the allowance already on screen is the whole explanation, so enter is inert
// exactly as it is on an empty box. The composed text stays put so it can go
// out after the recharge.
func TestSendBlockedWhenExhausted(t *testing.T) {
	m := limited(t, cosmo.ReplyLimits{
		RemainingCount: 0, MaxCount: 5, CanReply: false,
		NextResetAt: "2026-08-02T09:01:19.188Z", TextMaxLength: 200,
	})
	m.setMode(modeInsert)
	m.reply.SetValue("안녕하세요")

	updated, cmd := m.send()
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("expected nothing dispatched: no send, and no notice to retire")
	}
	if m.status != "" {
		t.Fatalf("status = %q, want the line left alone", m.status)
	}
	if m.reply.Value() != "안녕하세요" {
		t.Fatalf("reply box = %q, want the typed text kept", m.reply.Value())
	}
	if m.mode != modeInsert {
		t.Fatal("a send that never left should keep the reply box")
	}
	// The standing allowance is what explains the silence, so it has to still
	// be on screen for the silence to be readable.
	if got := m.render(); !strings.Contains(got, "replies [0/5, recharges ") {
		t.Fatalf("allowance missing from the composing line: %q", got)
	}
}

// The allowance only gates when it is known: an unfetched one must not block
// sending, or a failed reply-count call would take the whole feature down.
func TestSendNotBlockedWithoutLimits(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 7
	m.setMode(modeInsert)
	m.reply.SetValue("안녕하세요")

	updated, cmd := m.send()
	m = updated.(Model)
	if cmd == nil || m.mode != modeNormal {
		t.Fatal("expected the send to go out with no allowance known")
	}
}

// Opening a channel drops the previous one's allowance rather than showing it
// against the new member until the fetch lands.
func TestOpenChannelResetsLimits(t *testing.T) {
	m := limited(t, fullAllowance())
	m.client = tokenless() // openChannel starts a stream; keep it off the network
	updated, _ := m.openChannel(9, "YuBin", "YuBin")
	m = updated.(Model)
	defer m.msgStream.Close()
	if m.hasLimits {
		t.Fatal("kept the previous channel's allowance across a switch")
	}
	if got := m.replyAllowance(); got != "" {
		t.Fatalf("allowance = %q, want nothing shown until the fetch lands", got)
	}
}

// welcomeMsg is the greeting as the API serves it: an artist message with a
// negative synthetic id and no cursor.
func welcomeMsg(memberID int, id string) cosmo.Message {
	return cosmo.Message{
		ID:         id,
		Type:       "text",
		SenderType: "artistMember",
		SenderID:   memberID,
		SenderName: "YuBin",
		Content:    "안녕하세요 공유빈입니다",
		CreatedAt:  "2026-07-01T09:22:29.668Z",
		IsWelcome:  true,
	}
}

// A channel with no messages at all (YuBin's) is already at its beginning, so
// the opening page — empty — must ask for the welcome message straight away
// rather than waiting for a scroll that has nothing to scroll.
func TestEmptyChannelFetchesWelcomeOnOpen(t *testing.T) {
	m := New(nil, "tripleS", ".", true, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.memberID = 8

	updated, cmd := m.Update(messagesLoadedMsg{memberID: 8, initial: true, messages: nil})
	m = updated.(Model)
	if !m.historyEnd {
		t.Fatal("an empty opening page is the whole history; want historyEnd")
	}
	if !m.welcomeDone {
		t.Fatal("expected the welcome fetch to have been issued")
	}
	if cmd == nil {
		t.Fatal("expected a command batch carrying the welcome fetch")
	}
}

// A short opening page means the channel holds less than one page of history,
// so its beginning is already on screen and the greeting is due now.
func TestShortOpeningPageFetchesWelcome(t *testing.T) {
	m := sizedModel(t, 0)
	m.memberID = 26

	updated, _ := m.Update(messagesLoadedMsg{
		memberID: 26, initial: true,
		messages: []cosmo.Message{{
			ID: "10419", SenderType: "artistMember", Content: "hi",
			CreatedAt: "2026-07-10T01:51:59.016Z",
		}},
	})
	m = updated.(Model)
	if !m.welcomeDone {
		t.Fatal("expected the welcome fetch after a short opening page")
	}
}

// A full opening page says nothing about where history ends, so the greeting
// must wait until paging up actually runs out.
func TestFullOpeningPageDefersWelcome(t *testing.T) {
	m := sizedModel(t, 0)
	m.memberID = 26
	msgs := make([]cosmo.Message, pageSize)
	for i := range msgs {
		msgs[i] = cosmo.Message{
			ID: fmt.Sprintf("m%02d", i), SenderType: "artistMember", Content: "hi",
			CreatedAt: fmt.Sprintf("2026-07-10T01:%02d:00.000Z", i),
		}
	}
	updated, _ := m.Update(messagesLoadedMsg{memberID: 26, initial: true, messages: msgs})
	m = updated.(Model)
	if m.historyEnd || m.welcomeDone {
		t.Fatal("a full page must not be mistaken for the beginning of history")
	}
}

// Paging up until a page comes back empty is the other way to reach the
// beginning. The greeting is fetched then, and the end-of-history notice is
// held back so it doesn't flash in front of the message that answers it.
func TestEmptyOlderPageFetchesWelcome(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 26

	updated, cmd := m.Update(olderLoadedMsg{memberID: 26})
	m = updated.(Model)
	if !m.historyEnd || !m.welcomeDone {
		t.Fatal("an empty older page is the beginning; want the welcome fetch")
	}
	if cmd == nil {
		t.Fatal("expected the welcome fetch to be dispatched")
	}
	if m.status != "" {
		t.Fatalf("status = %q, want the notice held while the greeting loads", m.status)
	}
}

// The greeting lands above the history that was already loaded, keeping the
// ascending order the viewport renders in.
func TestWelcomePrependsAboveHistory(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 26
	first := m.messages[0].ID

	updated, _ := m.Update(welcomeLoadedMsg{memberID: 26, msg: welcomeMsg(26, "-21")})
	m = updated.(Model)

	if len(m.messages) != 4 {
		t.Fatalf("len(messages) = %d, want 4", len(m.messages))
	}
	if m.messages[0].ID != "-21" {
		t.Fatalf("messages[0] = %q, want the welcome message above %q", m.messages[0].ID, first)
	}
	if !m.messages[0].IsWelcome {
		t.Error("the prepended message lost its IsWelcome mark")
	}
	if got := m.render(); !strings.Contains(got, "안녕하세요 공유빈입니다") {
		t.Fatal("welcome message not rendered")
	}
}

// On a channel with no history the greeting is the only message, which is the
// case that would otherwise leave the selection pointing past the end.
func TestWelcomeIntoEmptyChannelSelectsCleanly(t *testing.T) {
	m := sizedModel(t, 0)
	m.memberID = 8

	updated, _ := m.Update(welcomeLoadedMsg{memberID: 8, msg: welcomeMsg(8, "-8")})
	m = updated.(Model)

	if len(m.messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(m.messages))
	}
	if m.selected != 0 {
		t.Fatalf("selected = %d, want 0 — the only message there is", m.selected)
	}
	if got := m.render(); !strings.Contains(got, "안녕하세요 공유빈입니다") {
		t.Fatal("welcome message not rendered into the empty channel")
	}
}

// A channel whose only content is the greeting is still repliable: the reply
// endpoint accepts the greeting's negative id, so the send goes out keyed to it
// rather than answering "nothing to reply to yet".
func TestWelcomeIsAReplyTarget(t *testing.T) {
	m := sizedModel(t, 0)
	m.memberID = 8
	updated, _ := m.Update(welcomeLoadedMsg{memberID: 8, msg: welcomeMsg(8, "-8")})
	m = updated.(Model)

	target := m.latestArtistMessage()
	if target == nil {
		t.Fatal("the welcome message should be a valid reply target")
	}
	if target.ID != "-8" {
		t.Fatalf("reply target = %q, want the greeting -8", target.ID)
	}

	m.setMode(modeInsert)
	m.reply.SetValue("안녕하세요")
	updated, cmd := m.send()
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected the reply to be dispatched")
	}
	if m.status == "nothing to reply to yet" {
		t.Fatal("refused to reply to a channel that has a greeting to reply to")
	}
	if m.mode != modeNormal || m.reply.Value() != "" {
		t.Fatal("expected the send path to hand the keys back and empty the box")
	}
}

// A real artist message still wins as the reply target with the greeting above
// it — the skip must not take the rest of the history with it.
func TestRealMessageStillReplyTargetWithWelcome(t *testing.T) {
	m := sizedModel(t, 2)
	m.memberID = 26
	updated, _ := m.Update(welcomeLoadedMsg{memberID: 26, msg: welcomeMsg(26, "-21")})
	m = updated.(Model)

	target := m.latestArtistMessage()
	if target == nil || target.IsWelcome {
		t.Fatalf("reply target = %+v, want the newest real artist message", target)
	}
	if target.ID != "msg-01" {
		t.Errorf("reply target = %q, want msg-01", target.ID)
	}
}

// The greeting is asked for once per channel visit, whatever came back, so
// sitting at the top pressing k doesn't refetch it.
func TestWelcomeFetchedOnlyOnce(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 26

	updated, _ := m.Update(olderLoadedMsg{memberID: 26})
	m = updated.(Model)
	updated, _ = m.Update(welcomeLoadedMsg{memberID: 26, absent: true})
	m = updated.(Model)

	// Another empty page: history is still ended, but the greeting is settled.
	updated, cmd := m.Update(olderLoadedMsg{memberID: 26})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected the end-of-history notice tick")
	}
	if m.status != "beginning of history" {
		t.Fatalf("status = %q, want the notice once the greeting is settled", m.status)
	}
}

// A greeting that arrives after the user has moved on belongs to the channel it
// was asked for, not whichever one is open now.
func TestWelcomeStaysInItsChannel(t *testing.T) {
	m := sizedModel(t, 2)
	m.memberID = 26

	updated, _ := m.Update(welcomeLoadedMsg{memberID: 8, msg: welcomeMsg(8, "-8")})
	m = updated.(Model)
	if len(m.messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2 — the greeting was for another channel", len(m.messages))
	}
}

// Auto-translate is for messages that arrive while you are watching; loaded
// history is left alone. The greeting is the oldest history there is, so it is
// left alone too — matching what paging older messages in already does.
func TestWelcomeIsNotAutoTranslated(t *testing.T) {
	m := sizedModel(t, 1)
	m.memberID = 8
	m.autoTranslate = true

	updated, cmd := m.Update(welcomeLoadedMsg{memberID: 8, msg: welcomeMsg(8, "-8")})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("expected nothing dispatched: the greeting is history, not an arrival")
	}
	if _, pending := m.transPending["-8"]; pending {
		t.Fatal("the greeting was auto-translated; history should be untouched")
	}
	if len(m.transQueue) != 0 {
		t.Fatalf("transQueue = %d, want the greeting left out of it", len(m.transQueue))
	}
}

// t on the greeting translates it like any other artist message: the endpoint
// accepts its negative id, so nothing about it should be skipped.
func TestWelcomeTranslatesOnDemand(t *testing.T) {
	m := sizedModel(t, 0)
	m.memberID = 8
	updated, _ := m.Update(welcomeLoadedMsg{memberID: 8, msg: welcomeMsg(8, "-8")})
	m = updated.(Model)

	m.selected = 0
	updated, cmd := m.translateSelected()
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected a translation request for the greeting")
	}
	if _, pending := m.transPending["-8"]; !pending {
		t.Fatal("the greeting was skipped by translateSelected")
	}
}

// Returning to a channel must not refetch a greeting already settled there.
func TestWelcomeDoneSurvivesChannelSwitch(t *testing.T) {
	m := sizedModel(t, 2)
	m.client = tokenless()
	m.memberID = 26
	m.historyEnd = true
	m.welcomeDone = true

	updated, _ := m.openChannel(8, "YuBin", "YuBin")
	m = updated.(Model)
	defer m.msgStream.Close()
	if m.welcomeDone {
		t.Fatal("carried the previous channel's welcome state into a new one")
	}

	updated, _ = m.openChannel(26, "ChaeWon", "ChaeWon")
	m = updated.(Model)
	defer m.msgStream.Close()
	if !m.welcomeDone || !m.historyEnd {
		t.Fatal("returning to a channel lost that its greeting was already settled")
	}
}

// A greeting lost to a transient error must not stay lost: k at the top asks
// again rather than reporting the end of history over a gap.
func TestWelcomeRetriesAfterFailure(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 26
	m.client = tokenless()

	updated, _ := m.Update(olderLoadedMsg{memberID: 26})
	m = updated.(Model)
	if !m.welcomeDone {
		t.Fatal("expected the first welcome fetch to be issued")
	}

	updated, _ = m.Update(welcomeLoadedMsg{memberID: 26, err: errors.New("boom")})
	m = updated.(Model)
	if m.err == nil {
		t.Fatal("expected the failure to surface")
	}
	if m.welcomeDone {
		t.Fatal("a failed fetch should leave the greeting retryable")
	}

	// k at the top now asks again instead of announcing the end.
	m.selected = 0
	updated, cmd := m.loadOlder()
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected the retry to be dispatched")
	}
	if m.status == "beginning of history" {
		t.Fatal("announced the end of history with the greeting still missing")
	}
	if !m.welcomeDone {
		t.Fatal("the retry should mark the fetch as issued again")
	}
}

// Once the greeting is settled, k at the top goes back to reporting the end.
func TestLoadOlderNoticesEndAfterWelcomeSettled(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 26
	m.historyEnd = true
	m.welcomeDone = true

	updated, cmd := m.loadOlder()
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected the notice tick")
	}
	if m.status != "beginning of history" {
		t.Fatalf("status = %q, want the end-of-history notice", m.status)
	}
}

// mediaPage builds n gallery items in the oldest-first order
// cosmo.MediaMessages returns them in, ids counting up from base.
func mediaPage(base, n int) []cosmo.Message {
	var items []cosmo.Message
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%d", base+i)
		items = append(items, cosmo.Message{
			ID:         id,
			Cursor:     id,
			SenderType: "artistMember",
			Type:       "image",
			MediaURL:   "https://example.com/media/" + id + ".jpg",
			CreatedAt:  fmt.Sprintf("2026-07-08T06:%02d:00.000Z", i),
		})
	}
	return items
}

// keyM is the gallery toggle.
var keyM = tea.KeyPressMsg{Code: 'm', Text: "m"}

// TestGallerySwapsThePane checks that m parks the messages, shows the media
// page in their place, and that a second m puts the messages back where they
// were — with the media list kept for the next visit.
func TestGallerySwapsThePane(t *testing.T) {
	m := sizedModel(t, 8)
	m.memberID = 7
	m.mode = modeNormal
	m.selected = 3
	m.historyEnd = true
	chat := m.messages

	updated, cmd := m.Update(keyM)
	m = updated.(Model)
	if !m.gallery || cmd == nil {
		t.Fatal("m should open the gallery and fetch its first page")
	}
	if m.chatBuf == nil || len(m.chatBuf.messages) != len(chat) || m.chatBuf.selected != 3 {
		t.Fatal("the messages should be parked with their selection")
	}
	if len(m.messages) != 0 || m.historyEnd {
		t.Fatal("the gallery should open on an empty, unfinished list")
	}
	if !strings.Contains(m.render(), "loading media…") {
		t.Fatal("expected the gallery load to be announced")
	}

	updated, _ = m.Update(mediaLoadedMsg{memberID: 7, initial: true, items: mediaPage(100, 3)})
	m = updated.(Model)
	if len(m.messages) != 3 {
		t.Fatalf("gallery holds %d items, want 3", len(m.messages))
	}
	if !m.historyEnd {
		t.Fatal("a first page shorter than a full one is the whole list")
	}
	view := m.render()
	if !strings.Contains(view, "media gallery · m: chat") {
		t.Fatalf("expected the gallery hint\n%s", view)
	}
	if !strings.Contains(m.viewport.View(), "100.jpg") {
		t.Fatalf("expected the media in the pane\n%s", m.viewport.View())
	}

	updated, _ = m.Update(keyM)
	m = updated.(Model)
	if m.gallery || m.chatBuf != nil {
		t.Fatal("m again should give the pane back to the messages")
	}
	if len(m.messages) != len(chat) || m.selected != 3 {
		t.Fatalf("messages not restored: %d msgs, selected %d", len(m.messages), m.selected)
	}
	if m.galleryBuf == nil || len(m.galleryBuf.messages) != 3 {
		t.Fatal("the media list should be kept for the next m")
	}

	// A third m restores it rather than starting over.
	updated, _ = m.Update(keyM)
	m = updated.(Model)
	if len(m.messages) != 3 || m.galleryBuf != nil {
		t.Fatal("reopening the gallery should restore the kept list")
	}
}

// TestGalleryPagesOlderMedia checks that k at the top of the gallery pages the
// media endpoint (not the message one), that the item its inclusive cursor
// repeats is deduped away, and that a page of nothing but that repeat ends the
// list.
func TestGalleryPagesOlderMedia(t *testing.T) {
	m := sizedModel(t, 0)
	m.memberID = 7
	m.mode = modeNormal
	m.gallery = true
	m.chatBuf = &channelState{seen: map[string]bool{}}
	m.appendMessages(mediaPage(200, 3), true)

	m.selected = 0
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	m = updated.(Model)
	if cmd == nil || !m.loadingOlder {
		t.Fatal("k on the oldest item should page older media in")
	}
	if m.status != "loading older media…" {
		t.Fatalf("status = %q, want the media load notice", m.status)
	}

	// The page repeats the item its cursor named, then two older ones.
	page := append(mediaPage(150, 2), m.messages[0])
	updated, _ = m.Update(mediaLoadedMsg{memberID: 7, items: page})
	m = updated.(Model)
	if len(m.messages) != 5 {
		t.Fatalf("history holds %d items, want 5 (the repeat deduped)", len(m.messages))
	}
	if m.historyEnd {
		t.Fatal("a page that added items is not the end of the list")
	}

	m.selected = 0
	updated, _ = m.loadOlder()
	m = updated.(Model)
	updated, _ = m.Update(mediaLoadedMsg{memberID: 7, items: []cosmo.Message{m.messages[0]}})
	m = updated.(Model)
	if !m.historyEnd || len(m.messages) != 5 {
		t.Fatal("a page of nothing but the repeat is the beginning of the media")
	}
	if m.status != "beginning of media" {
		t.Fatalf("status = %q, want the end-of-media notice", m.status)
	}
}

// TestGalleryTakesLiveMessages checks that what arrives while the gallery is up
// lands in the parked message buffer whatever it is, and in the gallery as well
// when it is media.
func TestGalleryTakesLiveMessages(t *testing.T) {
	m := sizedModel(t, 2)
	m.memberID = 7
	m.mode = modeNormal
	updated, _ := m.Update(keyM)
	m = updated.(Model)
	updated, _ = m.Update(mediaLoadedMsg{memberID: 7, initial: true, items: mediaPage(300, 1)})
	m = updated.(Model)

	text := `{"id":"400","content":"안녕","createdAt":"2026-07-08T08:00:00.000Z"}`
	updated, _ = m.Update(sseMsg{memberID: 7, ev: cosmo.SSEEvent{Event: "user-message.artist", Data: text}})
	m = updated.(Model)
	if len(m.messages) != 1 {
		t.Fatal("a text message does not belong in the gallery")
	}
	if !m.chatBuf.seen["400"] {
		t.Fatal("the parked messages should still take it")
	}

	photo := `{"id":"401","type":"image","mediaUrl":"https://example.com/media/401.jpg",` +
		`"createdAt":"2026-07-08T08:01:00.000Z"}`
	updated, _ = m.Update(sseMsg{memberID: 7, ev: cosmo.SSEEvent{Event: "user-message.artist", Data: photo}})
	m = updated.(Model)
	if len(m.messages) != 2 || m.messages[1].ID != "401" {
		t.Fatal("a media message should show in the gallery as it arrives")
	}
	if !m.chatBuf.seen["401"] {
		t.Fatal("the parked messages should take media too")
	}
}

// TestGalleryRefusesReplies checks that the gallery takes no reply: i is
// refused, the box is off the frame, and the row it would take goes to the
// list — then comes back with the messages.
func TestGalleryRefusesReplies(t *testing.T) {
	m := sizedModel(t, 3)
	m.memberID = 7
	m.mode = modeNormal
	chatRows := m.viewport.Height()
	if !strings.Contains(m.render(), m.reply.Prompt) {
		t.Fatal("the chat should show its reply box")
	}

	updated, _ := m.Update(keyM)
	m = updated.(Model)
	updated, _ = m.Update(mediaLoadedMsg{memberID: 7, initial: true, items: mediaPage(700, 2)})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'i', Text: "i"})
	m = updated.(Model)
	if m.mode == modeInsert || m.AcceptsText() {
		t.Fatal("i should not open a reply from the gallery")
	}
	view := m.render()
	if strings.Contains(view, m.reply.Prompt) {
		t.Fatalf("the reply box should be hidden in the gallery\n%s", view)
	}
	if strings.Contains(view, "i: reply") {
		t.Fatal("the gallery hint should not offer a key it refuses")
	}
	if got := m.viewport.Height(); got != chatRows+1 {
		t.Fatalf("pane is %d rows, want %d (the reply box's row)", got, chatRows+1)
	}
	// The frame must still fit the window it was sized for.
	if got := strings.Count(view, "\n") + 1; got != 30 {
		t.Fatalf("frame is %d rows, want the window's 30", got)
	}

	updated, _ = m.Update(keyM)
	m = updated.(Model)
	if m.viewport.Height() != chatRows || !strings.Contains(m.render(), m.reply.Prompt) {
		t.Fatal("the reply box should come back with the messages")
	}
}

// TestGalleryLeftOnChannelSwitch checks that opening another channel from the
// gallery caches the messages for the channel being left — not the media list
// that was over them — and drops the media with it.
func TestGalleryLeftOnChannelSwitch(t *testing.T) {
	m := sizedModel(t, 4)
	m.client = tokenless() // openChannel starts a stream; keep it off the network
	m.memberID = 7
	m.mode = modeNormal
	updated, _ := m.Update(keyM)
	m = updated.(Model)
	updated, _ = m.Update(mediaLoadedMsg{memberID: 7, initial: true, items: mediaPage(600, 2)})
	m = updated.(Model)

	updated, _ = m.openChannel(9, "Nien", "Nien")
	m = updated.(Model)
	if m.gallery || m.chatBuf != nil || m.galleryBuf != nil {
		t.Fatal("the gallery belongs to the channel being left")
	}
	st, ok := m.cache[7]
	if !ok || len(st.messages) != 4 {
		t.Fatalf("cached %v for the old channel, want its 4 messages", st)
	}
}

// TestGalleryEmptyChannel checks that a channel with no media says so instead
// of leaving a pane that reads like a failed load.
func TestGalleryEmptyChannel(t *testing.T) {
	m := sizedModel(t, 2)
	m.memberID = 7
	m.mode = modeNormal
	updated, _ := m.Update(keyM)
	m = updated.(Model)
	updated, _ = m.Update(mediaLoadedMsg{memberID: 7, initial: true})
	m = updated.(Model)
	if !strings.Contains(m.render(), "no media in this channel") {
		t.Fatalf("expected the empty-gallery notice\n%s", m.render())
	}
}
