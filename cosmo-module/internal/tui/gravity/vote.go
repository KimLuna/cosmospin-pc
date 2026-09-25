package gravity

// Casting a gravity vote. A vote is not a REST call: it is an on-chain ERC-1155
// transfer of COMO to the gravity's poll contract, signed by the user's
// embedded wallet, carrying an opaque payload that Cosmo signs to name the
// candidate (see cosmo.FabricateVote). It therefore reuses the whole objekt
// send pipeline - gas station, AGW signing, sponsored paymaster, broadcast -
// with different calldata.
//
// The flow is modal over the detail view: pick a candidate, enter an amount,
// confirm the irreversible spend, then watch it settle. COMO is spent for real
// and cannot be recovered, so the amount is checked against the live balance
// and the confirm step names both the candidate and the cost.

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"codeberg.org/djvu/cosmo-tui/internal/chain"
	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/tui/keynav"
	"codeberg.org/djvu/cosmo-tui/internal/tui/style"
	"codeberg.org/djvu/cosmo-tui/internal/tui/textfmt"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// votePhase tracks the vote flow layered over the detail view. voteOff means no
// vote is in progress.
type votePhase int

const (
	voteOff votePhase = iota
	voteChoice
	voteAmount
	voteConfirm
	voteSending
	voteDone
)

// voteState is everything the vote flow needs while it runs. The gravity and
// poll are captured when the flow starts so the screens never index back into
// the loaded detail mid-flight.
type voteState struct {
	phase   votePhase
	gravity cosmo.Gravity
	poll    cosmo.Poll

	cursor  int // into poll.Choices
	amount  textinput.Model
	balance int64
	spend   int64

	hash      string
	confirmed bool
	err       error
}

type (
	// voteBalanceMsg carries the live COMO balance for the vote screens.
	voteBalanceMsg struct {
		balance int64
		err     error
	}
	// voteResultMsg is the outcome of a cast vote. hash is set once the
	// transaction is on the wire, even if no receipt was seen (confirmed false).
	voteResultMsg struct {
		hash      string
		confirmed bool
		err       error
	}
)

// newAmountInput builds the COMO amount box.
func newAmountInput() textinput.Model {
	in := textinput.New()
	in.Placeholder = "COMO"
	in.Prompt = "amount: "
	style.PlainInput(&in)
	style.FitInput(&in)
	return in
}

// canVote reports whether the vote keybind should do anything for the gravity
// currently on screen: it must be open on Abstract and a signing wallet must be
// provisioned.
func (m Model) canVote() bool {
	return m.wallet != nil && m.detail.Votable()
}

// startVote opens the vote flow over the detail view and refreshes the balance.
func (m Model) startVote() (tea.Model, tea.Cmd) {
	poll, ok := m.detail.OpenPoll()
	if !ok {
		// The window closed under a detail that was loaded while it was open;
		// canVote is re-read every frame, so this is the narrow race where it
		// closes between the keypress and here.
		m.vote.err = fmt.Errorf("voting is not open on this gravity")
		m.vote.phase = voteDone
		return m, nil
	}
	if len(poll.Choices) == 0 {
		// The candidate list never loaded; without it there is no choiceId to send.
		m.vote.err = fmt.Errorf("no candidates are available to vote on")
		m.vote.phase = voteDone
		return m, nil
	}
	m.vote = voteState{
		phase:   voteChoice,
		gravity: m.detail,
		poll:    poll,
		amount:  newAmountInput(),
	}
	client, group := m.client, m.group
	return m, func() tea.Msg {
		b, err := client.ComoBalance(context.Background(), group)
		return voteBalanceMsg{balance: b, err: err}
	}
}

// handleVoteKey routes a keypress while the vote flow is open. It returns
// handled=false when the flow is not running, so the caller falls through to
// the normal detail keys.
func (m Model) handleVoteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.vote.phase == voteOff {
		return m, nil, false
	}
	k := msg.String()

	switch m.vote.phase {
	case voteChoice:
		switch {
		case k == "esc":
			// Not q: outside the amount box the shell keeps the plain-letter
			// globals, so q quits the app as it does everywhere else.
			m.vote = voteState{}
			return m, nil, true
		case k == "j" || k == "down":
			if m.vote.cursor < len(m.vote.poll.Choices)-1 {
				m.vote.cursor++
			}
			return m, nil, true
		case k == "k" || k == "up":
			if m.vote.cursor > 0 {
				m.vote.cursor--
			}
			return m, nil, true
		case k == "enter":
			m.vote.phase = voteAmount
			m.vote.amount.Focus()
			return m, nil, true
		}
		return m, nil, true

	case voteAmount:
		switch k {
		case "esc":
			m.vote.amount.Blur()
			m.vote.phase = voteChoice
			m.vote.err = nil
			return m, nil, true
		case "enter":
			n, err := parseSpend(m.vote.amount.Value(), m.vote.balance)
			if err != nil {
				m.vote.err = err
				return m, nil, true
			}
			m.vote.spend = n
			m.vote.err = nil
			m.vote.amount.Blur()
			m.vote.phase = voteConfirm
			return m, nil, true
		}
		var cmd tea.Cmd
		m.vote.amount, cmd = m.vote.amount.Update(msg)
		style.FitInput(&m.vote.amount)
		return m, cmd, true

	case voteConfirm:
		switch k {
		case "y", "enter":
			m.vote.phase = voteSending
			m.vote.err = nil
			return m, m.castVoteCmd(), true
		case "n", "esc":
			m.vote.phase = voteAmount
			m.vote.amount.Focus()
			return m, nil, true
		}
		return m, nil, true

	case voteSending:
		// The transaction is in flight; swallow everything so a stray key
		// cannot dismiss a spend that is already happening.
		return m, nil, true

	case voteDone:
		// Leaving takes the uniform back motion, as the live page's finished
		// download panel does; the flow stays modal until then so a stray key
		// cannot drop the outcome of a real COMO spend before it is read.
		if !keynav.Ascend(msg) {
			return m, nil, true
		}
		// Reload the detail so a settled vote shows up.
		m.vote = voteState{}
		if m.detail.ID != 0 {
			return m, m.loadDetail(m.detail.ID), true
		}
		return m, nil, true
	}
	return m, nil, true
}

// parseSpend validates a COMO amount against the live balance.
func parseSpend(s string, balance int64) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("enter an amount")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a whole number", s)
	}
	if n < 1 {
		return 0, fmt.Errorf("a vote must spend at least 1 COMO")
	}
	if n > balance {
		return 0, fmt.Errorf("only %s COMO available", fmtComo(balance))
	}
	return n, nil
}

// castVoteCmd runs the vote and waits for it to settle.
func (m Model) castVoteCmd() tea.Cmd {
	client, w, group := m.client, m.wallet, m.group
	g, poll := m.vote.gravity, m.vote.poll
	choice := poll.Choices[m.vote.cursor]
	spend := m.vote.spend

	return func() tea.Msg {
		ctx := context.Background()
		hash, target, err := castVote(ctx, client, w, group, g, poll, choice.ID, spend)
		if err != nil {
			return voteResultMsg{hash: hash, err: err}
		}
		confirmed, err := awaitVote(ctx, client, hash, target)
		return voteResultMsg{hash: hash, confirmed: confirmed, err: err}
	}
}

// castVote builds, signs and broadcasts the COMO transfer that casts a vote,
// returning the transaction hash and the poll contract the COMO was sent to.
// The hash is returned even alongside an error once the transaction is on the
// wire, so the caller can still show what was broadcast.
func castVote(ctx context.Context, client *cosmo.Client, w *wallet.Wallet, group string,
	g cosmo.Gravity, poll cosmo.Poll, choiceID string, spend int64) (string, string, error) {

	pollAddr, onAbstract := g.PollContract()
	if !onAbstract {
		return "", "", fmt.Errorf("this gravity is not on Abstract and cannot be voted on")
	}

	// The COMO id is per-artist; spending the wrong id would move another
	// artist's COMO, so it is read from the API rather than assumed.
	artist, err := client.Artist(ctx, group)
	if err != nil {
		return "", "", fmt.Errorf("artist: %w", err)
	}
	if artist.ComoTokenID == 0 {
		return "", "", fmt.Errorf("no COMO token id for %s", group)
	}

	me, err := client.Profile(ctx, group)
	if err != nil {
		return "", "", fmt.Errorf("profile: %w", err)
	}
	from, err := wallet.ParseAddress(me.Address)
	if err != nil {
		return "", "", fmt.Errorf("wallet address: %w", err)
	}
	target, err := wallet.ParseAddress(pollAddr)
	if err != nil {
		return "", "", fmt.Errorf("poll contract: %w", err)
	}

	// Cosmo signs the payload naming the candidate; without it the vote cannot
	// be built. This does not spend anything on its own.
	voteData, err := client.FabricateVote(ctx, poll.ID, choiceID, spend)
	if err != nil {
		return "", "", fmt.Errorf("authorize vote: %w", err)
	}

	// The transaction goes to the COMO contract; the poll is inside the calldata.
	data := wallet.VoteCalldata(from, target, big.NewInt(artist.ComoTokenID), big.NewInt(spend), voteData)
	hash, err := chain.Send(ctx, client, w, from, wallet.ComoToken, data)
	if err != nil {
		return "", "", err
	}
	return hash, pollAddr, nil
}

// awaitVote waits for a broadcast vote to be mined and confirms it actually
// moved COMO to the poll contract. The receipt's TransferSingle event is the
// only trustworthy proof: a transaction can mine successfully having moved
// nothing, and Cosmo's own status endpoint reports zero until the poll is
// revealed, so it cannot confirm a vote either.
func awaitVote(ctx context.Context, c chain.ReceiptReader, hash, pollAddr string) (bool, error) {
	return chain.AwaitReceipt(ctx, c, hash, chain.Proof{
		Moved: func(rec cosmo.AbstractReceipt) bool {
			return rec.MovedComo(wallet.ComoToken.Hex(), pollAddr)
		},
		Reverted: fmt.Errorf("the vote reverted on-chain; no COMO was spent"),
		Unproven: fmt.Errorf("the transaction was mined but recorded no COMO transfer to the poll (%s)",
			textfmt.ShortAddr(pollAddr)),
	})
}

// renderVote draws whichever vote screen is open, full-page over the detail.
// The body is padded out to the frame height so the hint line sits at the
// bottom of the terminal rather than floating up under the content, matching
// the detail view.
func (m Model) renderVote() string {
	body, hint := m.voteBody()

	h := m.viewport.Height()
	if h < 1 {
		h = 1
	}
	body = lipgloss.Place(m.width, h, lipgloss.Left, lipgloss.Top, body)
	status := lipgloss.NewStyle().MaxWidth(m.width).Render(statusStyle.Render(hint))
	return lipgloss.JoinVertical(lipgloss.Left, body, status)
}

// voteBody renders the current vote screen's content and the hint line that
// belongs at the foot of the frame.
func (m Model) voteBody() (body, hint string) {
	v := m.vote
	var b strings.Builder
	b.WriteString(titleStyle.Render("Vote · "+v.gravity.Title) + "\n")
	b.WriteString(metaStyle.Render(fmt.Sprintf("balance: %s COMO", fmtComo(v.balance))) + "\n\n")

	switch v.phase {
	case voteChoice:
		b.WriteString(headingStyle.Render("Pick a candidate") + "\n")
		for i, c := range v.poll.Choices {
			cursor := "  "
			name := c.Title
			if i == v.cursor {
				cursor = "> "
				name = okStyle.Render(c.Title)
			}
			line := cursor + name
			if c.Description != "" {
				line += metaStyle.Render(" (" + c.Description + ")")
			}
			b.WriteString(line + "\n")
		}
		hint = "j/k: move · enter: choose · esc: cancel"

	case voteAmount:
		b.WriteString(headingStyle.Render("How much COMO?") + "\n")
		b.WriteString("voting for " + okStyle.Render(v.poll.Choices[v.cursor].Title) + "\n\n")
		b.WriteString(v.amount.View() + "\n")
		if v.err != nil {
			b.WriteString(errStyle.Render(v.err.Error()) + "\n")
		}
		hint = "enter: continue · esc: back"

	case voteConfirm:
		b.WriteString(headingStyle.Render("Confirm") + "\n")
		b.WriteString(fmt.Sprintf("Spend %s for %s.\n",
			okStyle.Render(fmtComo(v.spend)+" COMO"), okStyle.Render(v.poll.Choices[v.cursor].Title)))
		b.WriteString(warnStyle.Render("This spends COMO on-chain and cannot be undone.") + "\n")
		hint = "y/enter: vote · n/esc: back"

	case voteSending:
		b.WriteString(statusStyle.Render("casting vote…") + "\n")
		if v.hash != "" {
			b.WriteString(metaStyle.Render(v.hash) + "\n")
		}
		hint = "working…"

	case voteDone:
		switch {
		case v.err != nil:
			b.WriteString(errStyle.Render("vote failed: "+v.err.Error()) + "\n")
		case v.confirmed:
			b.WriteString(okStyle.Render(fmt.Sprintf("voted: %s COMO for %s",
				fmtComo(v.spend), v.poll.Choices[v.cursor].Title)) + "\n")
		default:
			// Broadcast but unproven: never claim it landed.
			b.WriteString(warnStyle.Render("broadcast, but not yet confirmed on-chain") + "\n")
		}
		if v.hash != "" {
			b.WriteString(metaStyle.Render("tx "+v.hash) + "\n")
		}
		hint = "h: back"
	}
	return b.String(), hint
}
