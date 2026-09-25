package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"codeberg.org/djvu/cosmo-tui/internal/chain"
	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"
)

const currentObjektContract = "0x99Bb83AE9bb0C0A6be865CaCF67760947f91Cb70"

type accountResult struct {
	Nickname string `json:"nickname"`
	Address  string `json:"address"`
	EOA      string `json:"eoa"`
}

type sessionDTO struct {
	Authenticated bool           `json:"authenticated"`
	Account       *accountResult `json:"account,omitempty"`
}

type copyDTO struct {
	Serial       int64  `json:"serial"`
	ObjektID     int64  `json:"objektId"`
	TokenID      int64  `json:"tokenId"`
	TokenAddress string `json:"tokenAddress"`
	Transferable bool   `json:"transferable"`
	UsedForGrid  bool   `json:"usedForGrid"`
	Status       string `json:"status"`
}

type collectionDTO struct {
	CollectionNo string    `json:"collectionNo"`
	Season       string    `json:"season"`
	Class        string    `json:"class"`
	Member       string    `json:"member"`
	FrontImage   string    `json:"frontImage"`
	AccentColor  string    `json:"accentColor"`
	Count        int       `json:"count"`
	Transferable int       `json:"transferableCount"`
	Copies       []copyDTO `json:"copies"`
}

type transferResult struct {
	Hash         string `json:"hash"`
	ObjektID     int64  `json:"objektId"`
	TokenAddress string `json:"tokenAddress"`
}

type spinSession struct {
	SpinID       int64     `json:"spinId"`
	ObjektID     int64     `json:"objektId"`
	TransferHash string    `json:"transferHash"`
	Phase        string    `json:"phase"`
	StartedAt    time.Time `json:"-"`
}

type spinTicketDTO struct {
	AvailableTicketsCount int    `json:"availableTicketsCount"`
	NextReceiveAt         string `json:"nextReceiveAt"`
}

type spinPriceDTO struct {
	Price int64 `json:"price"`
}

type pointDTO struct {
	Balance     int64 `json:"balance"`
	FreeBalance int64 `json:"freeBalance"`
}

type seasonListDTO struct {
	Seasons []struct {
		Title    string `json:"title"`
		Spinable bool   `json:"spinable"`
		Ongoing  bool   `json:"ongoing"`
	} `json:"seasons"`
}

type spinStatusDTO struct {
	Tickets          int          `json:"tickets"`
	NextReceiveAt    string       `json:"nextReceiveAt"`
	Price            int64        `json:"price"`
	PointBalance     int64        `json:"pointBalance"`
	FreePointBalance int64        `json:"freePointBalance"`
	SpinableSeasons  []string     `json:"spinableSeasons"`
	Pending          *spinSession `json:"pending,omitempty"`
}

type spinStartRequest struct {
	ObjektID int64 `json:"objektId"`
}

type spinCompleteRequest struct {
	Index int `json:"index"`
}

type spinCollectionResult struct {
	Season          string `json:"season"`
	CollectionNo    string `json:"collectionNo"`
	Class           string `json:"class"`
	Member          string `json:"member"`
	ThumbnailImage  string `json:"thumbnailImage"`
	FrontImage      string `json:"frontImage"`
	BackImage       string `json:"backImage"`
	AccentColor     string `json:"accentColor"`
	BackgroundColor string `json:"backgroundColor"`
	TextColor       string `json:"textColor"`
	ObjektNo        int64  `json:"objektNo,omitempty"`
	CollectionID    string `json:"collectionId"`
}

type spinStartResult struct {
	SpinID       int64  `json:"spinId"`
	ObjektID     int64  `json:"objektId"`
	TransferHash string `json:"transferHash"`
	Slots        int    `json:"slots"`
}

type spinCompleteResult struct {
	SpinID        int64                   `json:"spinId"`
	WinnerIndex   int                     `json:"winnerIndex"`
	Selected      *spinCollectionResult   `json:"selected"`
	Results       []*spinCollectionResult `json:"results"`
	Tickets       int                     `json:"tickets"`
	NextReceiveAt string                  `json:"nextReceiveAt"`
}

type sendCodeRequest struct {
	Email string `json:"email"`
}
type loginRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}
type searchRequest struct {
	Query string `json:"query"`
}
type transferRequest struct {
	ObjektID  int64  `json:"objektId"`
	Recipient string `json:"recipient"`
}

func withTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func getSession(s *userSession) (*cosmo.Client, *wallet.Wallet, error) {
	if s == nil || s.client == nil || s.signer == nil {
		return nil, nil, errors.New("먼저 Cosmo 계정에 로그인해 주세요")
	}
	return s.client, s.signer, nil
}

func apiErrorDetail(prefix string, err error) error {
	var ae *cosmo.APIError
	if errors.As(err, &ae) {
		parts := []string{fmt.Sprintf("HTTP %d", ae.Status)}
		if s := strings.TrimSpace(ae.Code); s != "" {
			parts = append(parts, s)
		}
		if s := strings.TrimSpace(ae.Message); s != "" {
			parts = append(parts, s)
		}
		body := strings.TrimSpace(ae.Body)
		if body != "" && ae.Message == "" && ae.Code == "" {
			if len(body) > 300 {
				body = body[:300] + "…"
			}
			parts = append(parts, body)
		}
		return fmt.Errorf("%s: %s", prefix, strings.Join(parts, " / "))
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

func sendCode(email string) (any, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, errors.New("이메일을 입력해 주세요")
	}
	ctx, cancel := withTimeout(30 * time.Second)
	defer cancel()
	if err := cosmo.SendCode(ctx, email); err != nil {
		return nil, apiErrorDetail("인증번호 발송 실패", err)
	}
	return map[string]bool{"ok": true}, nil
}

func loginCosmo(email, code string) (*cosmo.Client, *wallet.Wallet, accountResult, error) {
	email = strings.TrimSpace(email)
	code = strings.TrimSpace(code)
	if email == "" || code == "" {
		return nil, nil, accountResult{}, errors.New("이메일과 인증번호를 모두 입력해 주세요")
	}

	ctx, cancel := withTimeout(120 * time.Second)
	defer cancel()

	creds, privy, err := cosmo.Login(ctx, email, code)
	if err != nil {
		return nil, nil, accountResult{}, apiErrorDetail("로그인 실패", err)
	}
	if strings.TrimSpace(privy.AccessToken) == "" || strings.TrimSpace(privy.EOA) == "" {
		return nil, nil, accountResult{}, errors.New("Privy 지갑 정보를 확인하지 못했습니다")
	}

	w, err := wallet.Provision(ctx, privy.AccessToken, privy.EOA)
	if err != nil {
		return nil, nil, accountResult{}, fmt.Errorf("전송용 지갑 준비 실패: %w", err)
	}
	c := cosmo.New(creds, nil)

	result := accountResult{EOA: privy.EOA}
	if p, profileErr := c.Profile(ctx, "tripleS"); profileErr == nil {
		result.Nickname = p.Nickname
		result.Address = p.Address
	}
	return c, w, result, nil
}

func sessionInfo(s *userSession) sessionDTO {
	if s == nil {
		return sessionDTO{Authenticated: false}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil || s.signer == nil || s.currentAccount == nil {
		return sessionDTO{Authenticated: false}
	}
	account := *s.currentAccount
	return sessionDTO{Authenticated: true, Account: &account}
}

func collections(s *userSession) (any, error) {
	c, _, err := getSession(s)
	if err != nil {
		return nil, err
	}
	ctx, cancel := withTimeout(90 * time.Second)
	defer cancel()

	all, err := c.AllObjektCollections(ctx, "tripleS", cosmo.ObjektSortNewest)
	if err != nil {
		return nil, apiErrorDetail("Objekt 불러오기 실패", err)
	}

	out := make([]collectionDTO, 0, len(all.Collections))
	for _, owned := range all.Collections {
		col := owned.Collection
		dto := collectionDTO{
			CollectionNo: col.CollectionNo,
			Season:       col.Season,
			Class:        col.Class,
			Member:       col.Member,
			FrontImage:   col.FrontImage,
			AccentColor:  col.AccentColor,
			Count:        owned.Count,
			Copies:       make([]copyDTO, 0, len(owned.Objekts)),
		}
		for _, o := range owned.Objekts {
			transferable := col.Transferable && o.Transferable && !o.UsedForGrid
			if transferable {
				dto.Transferable++
			}
			dto.Copies = append(dto.Copies, copyDTO{
				Serial:       o.ObjektNo,
				ObjektID:     o.ObjektID,
				TokenID:      o.TokenID,
				TokenAddress: o.TokenAddress,
				Transferable: transferable,
				UsedForGrid:  o.UsedForGrid,
				Status:       o.Status,
			})
		}
		out = append(out, dto)
	}
	return out, nil
}

func searchUsers(s *userSession, query string) (any, error) {
	c, _, err := getSession(s)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return []cosmo.UserSearchResult{}, nil
	}
	ctx, cancel := withTimeout(30 * time.Second)
	defer cancel()
	users, err := c.SearchUsers(ctx, query)
	if err != nil {
		return nil, apiErrorDetail("사용자 검색 실패", err)
	}
	return users, nil
}

func resolveTokenContract(ctx context.Context, c *cosmo.Client, own cosmo.ObjektOwnership) (string, error) {
	candidates := []string{strings.TrimSpace(own.TokenAddress), currentObjektContract}
	seen := map[string]bool{}
	var lastErr error
	for _, candidate := range candidates {
		key := strings.ToLower(candidate)
		if candidate == "" || seen[key] {
			continue
		}
		seen[key] = true
		chainOwner, err := c.AbstractOwnerOf(ctx, candidate, own.ObjektID)
		if err != nil {
			lastErr = err
			continue
		}
		if strings.EqualFold(chainOwner, own.Owner) {
			return candidate, nil
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("실제 Objekt 계약 확인 실패: %w", lastErr)
	}
	return "", errors.New("현재 소유자와 일치하는 Objekt 계약을 확인하지 못했습니다. 전송을 중단했습니다")
}

func transfer(s *userSession, objektID int64, recipient string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return transferLocked(s, objektID, recipient)
}

func transferLocked(s *userSession, objektID int64, recipient string) (any, error) {
	c, w, err := getSession(s)
	if err != nil {
		return nil, err
	}
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		return nil, errors.New("받는 사람을 선택해 주세요")
	}

	ctx, cancel := withTimeout(100 * time.Second)
	defer cancel()

	own, err := c.OwnedObjektDetail(ctx, objektID)
	if err != nil {
		return nil, apiErrorDetail("Objekt 상태 재확인 실패", err)
	}
	if !own.Transferable {
		return nil, errors.New("이 Objekt는 현재 전송할 수 없습니다")
	}
	if own.ObjektID != objektID {
		return nil, errors.New("Objekt ID 검증에 실패했습니다")
	}

	from, err := wallet.ParseAddress(own.Owner)
	if err != nil {
		return nil, fmt.Errorf("소유자 주소 오류: %w", err)
	}
	toUser, err := wallet.ParseAddress(recipient)
	if err != nil {
		return nil, fmt.Errorf("받는 사람 주소 오류: %w", err)
	}
	if from == toUser {
		return nil, errors.New("본인 계정으로는 전송하지 않습니다")
	}

	tokenAddress, err := resolveTokenContract(ctx, c, own)
	if err != nil {
		return nil, err
	}
	tokenContract, err := wallet.ParseAddress(tokenAddress)
	if err != nil {
		return nil, fmt.Errorf("Objekt 계약 주소 오류: %w", err)
	}

	data := wallet.TransferCalldata(from, toUser, big.NewInt(own.ObjektID))
	hash, err := chain.Send(ctx, c, w, from, tokenContract, data)
	if err != nil {
		return nil, fmt.Errorf("전송 트랜잭션 생성/전송 실패: %w", err)
	}

	moved, err := chain.AwaitReceipt(ctx, c, hash, chain.Proof{
		Moved:    func(r cosmo.AbstractReceipt) bool { return r.MovedToken(tokenAddress, own.ObjektID, recipient) },
		Reverted: errors.New("전송 트랜잭션이 체인에서 되돌려졌습니다"),
		Unproven: errors.New("트랜잭션은 처리됐지만 Objekt 이동 로그가 확인되지 않았습니다"),
	})
	if err != nil {
		return nil, fmt.Errorf("전송 결과 확인 실패 (%s): %w", hash, err)
	}
	if !moved {
		return nil, fmt.Errorf("전송 확인 시간이 초과되었습니다. 해시 %s 를 확인한 뒤 재전송 여부를 결정해 주세요", hash)
	}

	return transferResult{Hash: hash, ObjektID: own.ObjektID, TokenAddress: tokenAddress}, nil
}

const cosmoAPIBase = "https://api.cosmo.fans"

func bearer(token string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return token
	}
	return "Bearer " + token
}

func doCosmoRequest(ctx context.Context, c *cosmo.Client, method, path string, payload any, encrypted bool, out any) error {
	token, err := c.EnsureToken(ctx)
	if err != nil {
		return fmt.Errorf("Cosmo 로그인 토큰 확인 실패: %w", err)
	}

	var body io.Reader
	contentType := ""
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if encrypted {
			encoded, err := cosmo.WebbridgeEncryptJSON(c, raw)
			if err != nil {
				return fmt.Errorf("Cosmo 요청 암호화 실패: %w", err)
			}
			body = strings.NewReader(encoded)
			contentType = "text/plain"
		} else {
			body = bytes.NewReader(raw)
			contentType = "application/json"
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, cosmoAPIBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", bearer(token))
	req.Header.Set("User-Agent", cosmo.UserAgent)
	// The captured official app flow used 2.49.0. This header is advisory; auth still comes from the logged-in session.
	req.Header.Set("AppVersion", "2.49.0")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if encrypted {
		req.Header.Set("X-Cosmo-Encrypted", "1")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := strings.TrimSpace(string(raw))
		if len(text) > 500 {
			text = text[:500] + "…"
		}
		if text == "" {
			text = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("Cosmo API HTTP %d: %s", resp.StatusCode, text)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("Cosmo 응답을 읽지 못했습니다: %w", err)
	}
	return nil
}

func spinStatus(s *userSession) (spinStatusDTO, error) {
	c, _, err := getSession(s)
	if err != nil {
		return spinStatusDTO{}, err
	}
	ctx, cancel := withTimeout(45 * time.Second)
	defer cancel()

	var tickets spinTicketDTO
	if err := doCosmoRequest(ctx, c, http.MethodGet, "/bff/v3/spin/tickets/tripleS", nil, false, &tickets); err != nil {
		return spinStatusDTO{}, fmt.Errorf("Spin Ticket 조회 실패: %w", err)
	}
	var price spinPriceDTO
	if err := doCosmoRequest(ctx, c, http.MethodGet, "/bff/v3/spin/price", nil, false, &price); err != nil {
		return spinStatusDTO{}, fmt.Errorf("Spin 가격 조회 실패: %w", err)
	}
	var point pointDTO
	if err := doCosmoRequest(ctx, c, http.MethodGet, "/bff/v3/point?artistId=tripleS", nil, false, &point); err != nil {
		return spinStatusDTO{}, fmt.Errorf("Point 조회 실패: %w", err)
	}
	var seasons seasonListDTO
	if err := doCosmoRequest(ctx, c, http.MethodGet, "/bff/v4/seasons/tripleS", nil, false, &seasons); err != nil {
		return spinStatusDTO{}, fmt.Errorf("Spin 가능 시즌 조회 실패: %w", err)
	}
	spinable := make([]string, 0, len(seasons.Seasons))
	for _, s := range seasons.Seasons {
		if s.Spinable {
			spinable = append(spinable, s.Title)
		}
	}

	s.mu.Lock()
	var p *spinSession
	if s.pendingSpin != nil {
		copy := *s.pendingSpin
		p = &copy
	}
	s.mu.Unlock()
	return spinStatusDTO{
		Tickets:          tickets.AvailableTicketsCount,
		NextReceiveAt:    tickets.NextReceiveAt,
		Price:            price.Price,
		PointBalance:     point.Balance,
		FreePointBalance: point.FreeBalance,
		SpinableSeasons:  spinable,
		Pending:          p,
	}, nil
}

func resolveCosmoSpin(ctx context.Context, c *cosmo.Client) (string, error) {
	users, err := c.SearchUsers(ctx, "cosmo-spin")
	if err != nil {
		return "", apiErrorDetail("cosmo-spin 계정 확인 실패", err)
	}
	for _, u := range users {
		if strings.EqualFold(strings.TrimSpace(u.Nickname), "cosmo-spin") && strings.TrimSpace(u.Address) != "" {
			return strings.TrimSpace(u.Address), nil
		}
	}
	return "", errors.New("공식 cosmo-spin 수신 계정을 확인하지 못했습니다. SPIN을 중단했습니다")
}

func spinStart(s *userSession, objektID int64) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingSpin != nil {
		return nil, fmt.Errorf("이미 진행 중인 SPIN이 있습니다 (단계: %s). 중복 실행하지 않습니다", s.pendingSpin.Phase)
	}
	c, _, err := getSession(s)
	if err != nil {
		return nil, err
	}

	// Always ask the server immediately before a spin. We do not locally refill or invent tickets.
	ctxStatus, cancelStatus := withTimeout(30 * time.Second)
	var tickets spinTicketDTO
	err = doCosmoRequest(ctxStatus, c, http.MethodGet, "/bff/v3/spin/tickets/tripleS", nil, false, &tickets)
	cancelStatus()
	if err != nil {
		return nil, fmt.Errorf("Spin Ticket 재확인 실패: %w", err)
	}
	if tickets.AvailableTicketsCount < 1 {
		return nil, errors.New("현재 서버 기준 Spin Ticket이 0장입니다. Point 결제는 이번 보수적 버전에서 자동 실행하지 않습니다")
	}

	ctxCheck, cancelCheck := withTimeout(30 * time.Second)
	own, err := c.OwnedObjektDetail(ctxCheck, objektID)
	if err != nil {
		cancelCheck()
		return nil, apiErrorDetail("SPIN Objekt 상태 확인 실패", err)
	}
	if !own.Transferable || own.ObjektID != objektID {
		cancelCheck()
		return nil, errors.New("선택한 Objekt는 현재 SPIN에 사용할 수 있는 전송 가능 상태가 아닙니다")
	}
	spinRecipient, err := resolveCosmoSpin(ctxCheck, c)
	cancelCheck()
	if err != nil {
		return nil, err
	}

	// Captured official flow: pre-sign first, then the on-chain Objekt transfer, then /spin.
	ctxPre, cancelPre := withTimeout(30 * time.Second)
	var pre struct {
		SpinID int64 `json:"spinId"`
	}
	err = doCosmoRequest(ctxPre, c, http.MethodPost, "/bff/v3/spin/pre-sign", map[string]int64{"usedTokenId": own.ObjektID}, false, &pre)
	cancelPre()
	if err != nil {
		return nil, fmt.Errorf("SPIN 사전 승인 실패: %w", err)
	}
	if pre.SpinID <= 0 {
		return nil, errors.New("SPIN ID를 받지 못해 중단했습니다")
	}
	s.pendingSpin = &spinSession{SpinID: pre.SpinID, ObjektID: objektID, Phase: "pre-signed"}

	trAny, err := transferLocked(s, objektID, spinRecipient)
	if err != nil {
		s.pendingSpin.Phase = "transfer-error"
		return nil, fmt.Errorf("SPIN용 Objekt 이동 단계에서 중단되었습니다. 자동 재시도하지 않습니다: %w", err)
	}
	tr := trAny.(transferResult)
	s.pendingSpin.TransferHash = tr.Hash
	s.pendingSpin.Phase = "transferred"

	ctxSpin, cancelSpin := withTimeout(30 * time.Second)
	// Captured body length and the pre-sign response match the official {spinId} request shape.
	err = doCosmoRequest(ctxSpin, c, http.MethodPost, "/bff/v3/spin", map[string]int64{"spinId": pre.SpinID}, true, nil)
	cancelSpin()
	if err != nil {
		s.pendingSpin.Phase = "spin-request-error"
		return nil, fmt.Errorf("Objekt는 이동했지만 SPIN 시작 요청 확인에 실패했습니다. 자동 재전송은 하지 않습니다: %w", err)
	}
	s.pendingSpin.Phase = "started"
	s.pendingSpin.StartedAt = time.Now()
	return spinStartResult{SpinID: pre.SpinID, ObjektID: objektID, TransferHash: tr.Hash, Slots: 16}, nil
}

func spinComplete(s *userSession, index int) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingSpin == nil || s.pendingSpin.Phase != "started" {
		return nil, errors.New("완료할 SPIN이 없습니다. 먼저 SPIN을 시작해 주세요")
	}
	if index < 0 || index > 15 {
		return nil, errors.New("SPIN 칸 번호가 올바르지 않습니다")
	}
	c, _, err := getSession(s)
	if err != nil {
		return nil, err
	}

	ctx, cancel := withTimeout(45 * time.Second)
	defer cancel()
	var results []*spinCollectionResult
	spinID := s.pendingSpin.SpinID

	// The successful capture's encrypted /spin/complete body is 64 bytes.
	// {spinId,index} fits that framing, while the earlier selectedIndex guess
	// does not. Send the user's single 0-based board choice once; never retry.
	payload := map[string]any{"spinId": spinID, "index": index}
	if err := doCosmoRequest(ctx, c, http.MethodPost, "/bff/v3/spin/complete", payload, true, &results); err != nil {
		s.pendingSpin.Phase = "handoff-required"
		return nil, fmt.Errorf("SPIN 결과 확정 실패. 이 프로그램에서는 자동 재시도하지 않습니다. 공식 Cosmo 앱을 열면 진행 중인 SPIN을 이어서 완료할 수 있습니다: %w", err)
	}
	if len(results) != 16 {
		s.pendingSpin.Phase = "handoff-required"
		return nil, fmt.Errorf("서버가 16칸이 아닌 %d칸을 반환했습니다. 이 프로그램에서는 자동 재시도하지 않습니다. 공식 Cosmo 앱에서 진행 중인 SPIN을 확인해 주세요", len(results))
	}

	winnerIndex := -1
	var selected *spinCollectionResult
	for i, card := range results {
		if card == nil || card.ObjektNo <= 0 {
			continue
		}
		// The captured successful response had the actually acquired Objekt marked
		// with objektNo. If more than one appears, keep the board but avoid making
		// an unsupported single-winner claim in the UI.
		if winnerIndex >= 0 {
			winnerIndex = -1
			selected = nil
			break
		}
		winnerIndex = i
		selected = card
	}

	s.pendingSpin = nil

	var t spinTicketDTO
	_ = doCosmoRequest(ctx, c, http.MethodGet, "/bff/v3/spin/tickets/tripleS", nil, false, &t)
	return spinCompleteResult{
		SpinID:        spinID,
		WinnerIndex:   winnerIndex,
		Selected:      selected,
		Results:       results,
		Tickets:       t.AvailableTicketsCount,
		NextReceiveAt: t.NextReceiveAt,
	}, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiHandler(method string, fn func(http.ResponseWriter, *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "허용되지 않은 요청입니다"})
			return
		}
		result, err := fn(w, r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func decode(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		return errors.New("요청 내용을 읽지 못했습니다")
	}
	return nil
}

func openBrowser(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	_ = cmd.Start()
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(exe))
	siteDir := filepath.Join(root, "site")
	if _, err := os.Stat(filepath.Join(siteDir, "index.html")); err != nil {
		log.Fatalf("site 폴더를 찾을 수 없습니다: %v", err)
	}

	sessions := newSessionStore()
	requireSession := func(r *http.Request) (*userSession, error) {
		session, ok := sessions.get(r)
		if !ok {
			return nil, errors.New("먼저 Cosmo 계정에 로그인해 주세요")
		}
		return session, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/send-code", apiHandler(http.MethodPost, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req sendCodeRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return sendCode(req.Email)
	}))
	mux.HandleFunc("/api/login", apiHandler(http.MethodPost, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req loginRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		c, signer, account, err := loginCosmo(req.Email, req.Code)
		if err != nil {
			return nil, err
		}
		if err := sessions.create(r, w, c, signer, account); err != nil {
			return nil, err
		}
		return account, nil
	}))
	mux.HandleFunc("/api/session", apiHandler(http.MethodGet, func(w http.ResponseWriter, r *http.Request) (any, error) {
		session, _ := sessions.get(r)
		return sessionInfo(session), nil
	}))
	mux.HandleFunc("/api/collections", apiHandler(http.MethodGet, func(w http.ResponseWriter, r *http.Request) (any, error) {
		session, err := requireSession(r)
		if err != nil {
			return nil, err
		}
		return collections(session)
	}))
	mux.HandleFunc("/api/search-users", apiHandler(http.MethodPost, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req searchRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		session, err := requireSession(r)
		if err != nil {
			return nil, err
		}
		return searchUsers(session, req.Query)
	}))
	mux.HandleFunc("/api/transfer", apiHandler(http.MethodPost, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req transferRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		session, err := requireSession(r)
		if err != nil {
			return nil, err
		}
		return transfer(session, req.ObjektID, req.Recipient)
	}))
	mux.HandleFunc("/api/spin/status", apiHandler(http.MethodGet, func(w http.ResponseWriter, r *http.Request) (any, error) {
		session, err := requireSession(r)
		if err != nil {
			return nil, err
		}
		return spinStatus(session)
	}))
	mux.HandleFunc("/api/spin/start", apiHandler(http.MethodPost, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req spinStartRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		session, err := requireSession(r)
		if err != nil {
			return nil, err
		}
		return spinStart(session, req.ObjektID)
	}))
	mux.HandleFunc("/api/spin/complete", apiHandler(http.MethodPost, func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req spinCompleteRequest
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		session, err := requireSession(r)
		if err != nil {
			return nil, err
		}
		return spinComplete(session, req.Index)
	}))
	mux.HandleFunc("/api/logout", apiHandler(http.MethodPost, func(w http.ResponseWriter, r *http.Request) (any, error) {
		sessions.delete(r, w)
		return map[string]bool{"ok": true}, nil
	}))
	mux.HandleFunc("/api/health", apiHandler(http.MethodGet, func(w http.ResponseWriter, r *http.Request) (any, error) {
		return map[string]any{"ok": true, "mode": "native-local"}, nil
	}))

	fs := http.FileServer(http.Dir(siteDir))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		fs.ServeHTTP(w, r)
	}))

	var ln net.Listener
	var addr string
	for port := 8787; port <= 8797; port++ {
		addr = fmt.Sprintf("127.0.0.1:%d", port)
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
	}
	if ln == nil {
		log.Fatal("실행할 포트를 찾지 못했습니다. 이미 실행 중인 창이 있는지 확인해 주세요.")
	}

	target := "http://" + addr + "/"
	fmt.Println("====================================================")
	fmt.Println(" tripleS Cosmo Tool v4 + SPIN")
	fmt.Println("====================================================")
	fmt.Println()
	fmt.Println("브라우저에서 프로그램을 열었습니다:")
	fmt.Println(target)
	fmt.Println()
	fmt.Println("로그인/조회/전송은 이 PC에서 직접 처리됩니다.")
	fmt.Println("이 검은 창은 프로그램 실행 중에 그대로 두세요.")
	fmt.Println("사용을 끝내려면 이 창을 닫으면 됩니다.")
	fmt.Println()

	go func() { time.Sleep(600 * time.Millisecond); openBrowser(target) }()
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
