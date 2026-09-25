package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"

	"codeberg.org/djvu/cosmo-tui/internal/cosmo"
	"codeberg.org/djvu/cosmo-tui/internal/wallet"
)

const sessionCookieName = "cosmo_session"

// userSession keeps all authenticated COSMO state private to one browser session.
// mu serializes transfer and SPIN operations for that user and protects pendingSpin.
type userSession struct {
	mu sync.Mutex

	client         *cosmo.Client
	signer         *wallet.Wallet
	currentAccount *accountResult
	pendingSpin    *spinSession
}

type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*userSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*userSession)}
}

func (s *sessionStore) create(r *http.Request, w http.ResponseWriter, client *cosmo.Client, signer *wallet.Wallet, account accountResult) error {
	id, err := newSessionID()
	if err != nil {
		return err
	}

	accountCopy := account
	s.mu.Lock()
	if oldID := sessionIDFromRequest(r); oldID != "" {
		delete(s.sessions, oldID)
	}
	s.sessions[id] = &userSession{
		client:         client,
		signer:         signer,
		currentAccount: &accountCopy,
	}
	s.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *sessionStore) get(r *http.Request) (*userSession, bool) {
	id := sessionIDFromRequest(r)
	if id == "" {
		return nil, false
	}
	s.mu.RLock()
	session, ok := s.sessions[id]
	s.mu.RUnlock()
	return session, ok
}

func (s *sessionStore) delete(r *http.Request, w http.ResponseWriter) {
	if id := sessionIDFromRequest(r); id != "" {
		s.mu.Lock()
		delete(s.sessions, id)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func sessionIDFromRequest(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func newSessionID() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", errors.New("세션 ID를 생성하지 못했습니다")
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
