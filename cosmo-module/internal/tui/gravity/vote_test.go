package gravity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/chain"
	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"

	tea "charm.land/bubbletea/v2"
)

// testWallet is a throwaway signing key. Nothing here signs; the vote flow only
// checks that a wallet is present before offering to vote.
func testWallet(t *testing.T) *wallet.Wallet {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	w, err := wallet.FromKeyBytes(key)
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	return w
}

// openGravity is a votable gravity: open, single-poll, on Abstract.
func openGravity() cosmo.Gravity {
	return cosmo.Gravity{
		ID: 194, Title: "msnz Cover Stage", PollType: cosmo.PollSingle,
		ContractOutlink: "https://abscan.org/address/0xF1A787da84af2A6e8227aD87112a21181B7b9b39",
		Polls: []cosmo.Poll{{
			ID: 235, Finalized: false,
			Choices: []cosmo.Choice{
				{ID: "dejavu", Title: "Deja Vu", Description: "TXT"},
				{ID: "psycho", Title: "Psycho", Description: "Red Velvet"},
			},
		}},
	}
}

// votingModel is a sized page sitting on an open gravity's detail with a wallet.
func votingModel(t *testing.T) Model {
	t.Helper()
	m := sized(t)
	m.wallet = testWallet(t)
	m.detail = openGravity()
	m.setFocus(focusDetail)
	return m
}

// TestCanVoteGating voting is offered only for an open Abstract gravity and only
// when a signing wallet exists.
func TestCanVoteGating(t *testing.T) {
	m := votingModel(t)
	if !m.canVote() {
		t.Fatal("an open Abstract gravity with a wallet should be votable")
	}

	noWallet := m
	noWallet.wallet = nil
	if noWallet.canVote() {
		t.Fatal("voting must be disabled without a signing wallet")
	}

	finished := m
	finished.detail.Polls = []cosmo.Poll{{ID: 235, Finalized: true}}
	if finished.canVote() {
		t.Fatal("a finished gravity must not be votable")
	}

	polygon := m
	polygon.detail.ContractOutlink = "https://polygonscan.com/address/0xc3E5ad11aE2F00c740E74B81f134426A3331D950"
	if polygon.canVote() {
		t.Fatal("a Polygon-era gravity must not be votable")
	}
}

// TestVoteFlowReachesConfirm walks candidate -> amount -> confirm and checks the
// confirm screen names the cost and the candidate and warns it is irreversible.
func TestVoteFlowReachesConfirm(t *testing.T) {
	m := votingModel(t)

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(Model)
	if m.vote.phase != voteChoice {
		t.Fatalf("phase = %d, want voteChoice", m.vote.phase)
	}
	m.vote.balance = 5 // the balance load is a command; set it directly

	// Move to the second candidate and choose it.
	m = key(m, "j")
	if m.vote.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.vote.cursor)
	}
	m = key(m, "enter")
	if m.vote.phase != voteAmount {
		t.Fatalf("phase = %d, want voteAmount", m.vote.phase)
	}

	m = key(m, "3")
	m = key(m, "enter")
	if m.vote.phase != voteConfirm {
		t.Fatalf("phase = %d, want voteConfirm (err %v)", m.vote.phase, m.vote.err)
	}
	if m.vote.spend != 3 {
		t.Fatalf("spend = %d, want 3", m.vote.spend)
	}
	out := m.render()
	for _, want := range []string{"Confirm", "3 COMO", "Psycho", "cannot be undone"} {
		if !strings.Contains(out, want) {
			t.Fatalf("confirm screen missing %q:\n%s", want, out)
		}
	}
}

// TestVoteAmountRejectsOverspend an amount above the live balance is refused at
// the amount step rather than sent on to be signed.
func TestVoteAmountRejectsOverspend(t *testing.T) {
	m := votingModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(Model)
	m.vote.balance = 2
	m = key(m, "enter") // pick the first candidate
	m = key(m, "9")
	m = key(m, "enter")
	if m.vote.phase != voteAmount {
		t.Fatalf("phase = %d, want to stay on voteAmount", m.vote.phase)
	}
	if m.vote.err == nil || !strings.Contains(m.vote.err.Error(), "only 2 COMO available") {
		t.Fatalf("err = %v, want an over-balance message", m.vote.err)
	}
}

func TestParseSpend(t *testing.T) {
	if _, err := parseSpend("", 5); err == nil {
		t.Error("empty amount should fail")
	}
	if _, err := parseSpend("abc", 5); err == nil {
		t.Error("non-numeric amount should fail")
	}
	if _, err := parseSpend("0", 5); err == nil {
		t.Error("zero should fail")
	}
	if _, err := parseSpend("-1", 5); err == nil {
		t.Error("negative should fail")
	}
	if _, err := parseSpend("6", 5); err == nil {
		t.Error("over balance should fail")
	}
	n, err := parseSpend(" 3 ", 5)
	if err != nil || n != 3 {
		t.Fatalf("parseSpend(3) = %d, %v", n, err)
	}
	if n, err := parseSpend("5", 5); err != nil || n != 5 {
		t.Fatalf("spending the whole balance should be allowed, got %d, %v", n, err)
	}
}

// fakeChain serves canned receipts to awaitVote.
type fakeChain struct {
	rec cosmo.AbstractReceipt
	err error
}

func (f fakeChain) AbstractTxState(context.Context, string) (cosmo.AbstractReceipt, error) {
	return f.rec, f.err
}

const (
	pollAddr = "0xF1A787da84af2A6e8227aD87112a21181B7b9b39"
	comoAddr = "0xd0ee3ba23a384a8eefd43f33a957ded60ed12706"
)

// addrTopic renders an address as a 32-byte topic word.
func addrTopic(addr string) string {
	return "0x" + strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(addr, "0x"))
}

func fastPolling(t *testing.T) {
	t.Helper()
	oldI, oldN := chain.PollInterval, chain.PollTries
	chain.PollInterval, chain.PollTries = time.Millisecond, 2
	t.Cleanup(func() { chain.PollInterval, chain.PollTries = oldI, oldN })
}

// TestAwaitVoteConfirmed a mined receipt carrying a TransferSingle of COMO to
// the poll contract is what proves the vote landed.
func TestAwaitVoteConfirmed(t *testing.T) {
	fastPolling(t)
	c := fakeChain{rec: cosmo.AbstractReceipt{
		Status: cosmo.TxSuccess,
		Logs: []cosmo.AbstractLog{{
			Address: comoAddr,
			Topics: []string{
				cosmo.TransferSingleTopic,
				addrTopic("0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A"), // operator
				addrTopic("0x3a6E4EFfEb030f950870C98834eb40f5BE9c7f2A"), // from
				addrTopic(pollAddr),                                     // to
			},
		}},
	}}
	ok, err := awaitVote(context.Background(), c, "0xabc", pollAddr)
	if err != nil || !ok {
		t.Fatalf("awaitVote = %v, %v; want true, nil", ok, err)
	}
}

// TestAwaitVoteReverted a reverted transaction spends nothing and must say so.
func TestAwaitVoteReverted(t *testing.T) {
	fastPolling(t)
	c := fakeChain{rec: cosmo.AbstractReceipt{Status: cosmo.TxReverted}}
	ok, err := awaitVote(context.Background(), c, "0xabc", pollAddr)
	if ok || err == nil || !strings.Contains(err.Error(), "no COMO was spent") {
		t.Fatalf("awaitVote = %v, %v; want false and a reverted error", ok, err)
	}
}

// TestAwaitVoteMinedWithoutTransfer mining is not proof: a receipt whose logs
// record no COMO moving to the poll must not be reported as a vote.
func TestAwaitVoteMinedWithoutTransfer(t *testing.T) {
	fastPolling(t)
	c := fakeChain{rec: cosmo.AbstractReceipt{
		Status: cosmo.TxSuccess,
		Logs: []cosmo.AbstractLog{{
			Address: "0x000000000000000000000000000000000000800a",
			Topics:  []string{cosmo.TransferTopic, "0x0", "0x0"},
		}},
	}}
	ok, err := awaitVote(context.Background(), c, "0xabc", pollAddr)
	if ok || err == nil {
		t.Fatalf("awaitVote = %v, %v; want false and an error", ok, err)
	}
}

// TestAwaitVoteNoReceipt a vote still in the mempool is unconfirmed, not failed.
func TestAwaitVoteNoReceipt(t *testing.T) {
	fastPolling(t)
	c := fakeChain{err: errors.New("rpc hiccup")}
	ok, err := awaitVote(context.Background(), c, "0xabc", pollAddr)
	if ok || err != nil {
		t.Fatalf("awaitVote = %v, %v; want false, nil", ok, err)
	}
}

// TestVoteDoneNeverOverclaims a broadcast whose receipt was never seen must be
// reported as unconfirmed, not as a successful vote.
func TestVoteDoneNeverOverclaims(t *testing.T) {
	m := votingModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(Model)
	m.vote.spend = 1

	updated, _ = m.Update(voteResultMsg{hash: "0xdead", confirmed: false})
	m = updated.(Model)
	if m.vote.phase != voteDone {
		t.Fatalf("phase = %d, want voteDone", m.vote.phase)
	}
	out := m.render()
	if !strings.Contains(out, "not yet confirmed") {
		t.Fatalf("unconfirmed vote must say so:\n%s", out)
	}
	if strings.Contains(out, "voted: ") {
		t.Fatalf("must not claim the vote succeeded:\n%s", out)
	}
	if !strings.Contains(out, "0xdead") {
		t.Fatalf("the broadcast hash should still be shown:\n%s", out)
	}
}

// TestVoteDoneConfirmed a proven vote reports what was spent, and explains why
// Cosmo will not show it yet.
func TestVoteDoneConfirmed(t *testing.T) {
	m := votingModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(Model)
	m.vote.spend = 2

	updated, _ = m.Update(voteResultMsg{hash: "0xbeef", confirmed: true})
	m = updated.(Model)
	out := m.render()
	for _, want := range []string{"voted: 2 COMO", "Deja Vu", "0xbeef"} {
		if !strings.Contains(out, want) {
			t.Fatalf("confirmed screen missing %q:\n%s", want, out)
		}
	}
}

// TestVoteDoneOnlyLeavesOnBack the outcome of a real COMO spend is dismissed
// only by the uniform back motion, matching the live page's finished download
// panel; a stray key must not drop it before it has been read.
func TestVoteDoneOnlyLeavesOnBack(t *testing.T) {
	m := votingModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(Model)
	m.vote.spend = 1
	updated, _ = m.Update(voteResultMsg{hash: "0xdead", confirmed: true})
	m = updated.(Model)
	if m.vote.phase != voteDone {
		t.Fatalf("phase = %d, want voteDone", m.vote.phase)
	}

	for _, k := range []string{"x", "enter", "esc", "j", "y"} {
		m = key(m, k)
		if m.vote.phase != voteDone {
			t.Fatalf("%q dismissed the vote outcome; only the back motion should", k)
		}
	}
	if !strings.Contains(m.render(), "h: back") {
		t.Fatalf("the hint should name the one exit:\n%s", m.render())
	}
	m = key(m, "h")
	if m.vote.phase != voteOff {
		t.Fatalf("phase = %d, want voteOff after h", m.vote.phase)
	}
}

// TestVoteSendingSwallowsKeys once a spend is in flight no keypress may dismiss
// or restart it.
func TestVoteSendingSwallowsKeys(t *testing.T) {
	m := votingModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(Model)
	m.vote.phase = voteSending

	for _, k := range []string{"esc", "enter", "y"} {
		m = key(m, k)
		if m.vote.phase != voteSending {
			t.Fatalf("%q changed phase to %d while sending", k, m.vote.phase)
		}
	}
}

// TestOnlyAmountCapturesText the global keybinds stay live across the vote
// screens; only the COMO amount box claims plain letters, so a typed "q" there
// is a character rather than a quit.
func TestOnlyAmountCapturesText(t *testing.T) {
	m := votingModel(t)
	if m.AcceptsText() {
		t.Fatal("browsing should not capture text")
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(Model)
	if m.vote.phase != voteChoice {
		t.Fatalf("phase = %d, want voteChoice", m.vote.phase)
	}
	if m.AcceptsText() {
		t.Fatal("the candidate list must leave the globals to the shell")
	}

	m = key(m, "enter") // into the amount box
	if m.vote.phase != voteAmount {
		t.Fatalf("phase = %d, want voteAmount", m.vote.phase)
	}
	if !m.AcceptsText() {
		t.Fatal("the amount box must capture text")
	}

	m = key(m, "esc") // back to the candidate list
	if m.AcceptsText() {
		t.Fatal("capture should stop on leaving the amount box")
	}

	m.vote.phase = voteConfirm
	if m.AcceptsText() {
		t.Fatal("the confirm screen must leave the globals to the shell")
	}
	m.vote.phase = voteDone
	if m.AcceptsText() {
		t.Fatal("the result screen must leave the globals to the shell")
	}
}

// TestVoteHintPinnedToBottom the hint line belongs on the last row of the frame,
// not directly under the content it follows.
func TestVoteHintPinnedToBottom(t *testing.T) {
	m := votingModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(Model)

	lines := strings.Split(m.render(), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "j/k: move") {
		t.Fatalf("hint should be the final line, got %q\nfull:\n%s", last, m.render())
	}
	// The frame is the body height plus the hint row, as the detail view is.
	if want := m.viewport.Height() + 1; len(lines) != want {
		t.Fatalf("frame is %d lines, want %d", len(lines), want)
	}
	// The candidates sit up at the top, far above the pinned hint.
	var candidateRow int
	for i, l := range lines {
		if strings.Contains(l, "Deja Vu") {
			candidateRow = i
			break
		}
	}
	if candidateRow == 0 || len(lines)-1-candidateRow < 2 {
		t.Fatalf("candidates at row %d of %d: hint is not pinned away from them", candidateRow, len(lines))
	}
}

// TestConfirmHintListsBothKeys enter confirms a vote just as y does, so the hint
// must name it: an unlisted key that spends COMO is a trap.
func TestConfirmHintListsBothKeys(t *testing.T) {
	m := votingModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(Model)
	m.vote.balance = 5
	m = key(m, "enter") // choose the first candidate
	m = key(m, "1")
	m = key(m, "enter") // to the confirm screen
	if m.vote.phase != voteConfirm {
		t.Fatalf("phase = %d, want voteConfirm", m.vote.phase)
	}

	_, hint := m.voteBody()
	for _, want := range []string{"y/enter", "n/esc"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("confirm hint %q should mention %q", hint, want)
		}
	}

	// And enter really does confirm, which is what makes listing it necessary.
	confirmed := key(m, "enter")
	if confirmed.vote.phase != voteSending {
		t.Fatalf("enter should confirm the vote, phase = %d", confirmed.vote.phase)
	}
	if y := key(m, "y"); y.vote.phase != voteSending {
		t.Fatalf("y should confirm the vote, phase = %d", y.vote.phase)
	}
}

// TestPasteIntoAmountBox checks a pasted COMO amount lands in the vote flow's
// amount box and is spendable like a typed one.
func TestPasteIntoAmountBox(t *testing.T) {
	m := votingModel(t)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = updated.(Model)
	m.vote.balance = 5
	m = key(m, "enter") // pick the first candidate
	if m.vote.phase != voteAmount {
		t.Fatalf("phase = %d, want voteAmount", m.vote.phase)
	}

	updated, _ = m.Update(tea.PasteMsg{Content: "3"})
	m = updated.(Model)
	if got := m.vote.amount.Value(); got != "3" {
		t.Fatalf("amount box = %q, want the pasted amount", got)
	}
	m = key(m, "enter")
	if m.vote.phase != voteConfirm || m.vote.spend != 3 {
		t.Fatalf("phase = %d spend = %d, want the pasted amount confirmed", m.vote.phase, m.vote.spend)
	}
}
