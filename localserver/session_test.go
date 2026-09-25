package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessionStoreKeepsBrowserSessionsSeparate(t *testing.T) {
	store := newSessionStore()

	firstRequest := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	firstResponse := httptest.NewRecorder()
	if err := store.create(firstRequest, firstResponse, nil, nil, accountResult{Nickname: "first"}); err != nil {
		t.Fatalf("create first session: %v", err)
	}
	firstCookie := firstResponse.Result().Cookies()[0]
	if !firstCookie.HttpOnly {
		t.Fatal("session cookie must be HttpOnly")
	}
	if firstCookie.Value == "" {
		t.Fatal("session cookie must have a random value")
	}

	secondRequest := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	secondResponse := httptest.NewRecorder()
	if err := store.create(secondRequest, secondResponse, nil, nil, accountResult{Nickname: "second"}); err != nil {
		t.Fatalf("create second session: %v", err)
	}
	secondCookie := secondResponse.Result().Cookies()[0]
	if secondCookie.Value == firstCookie.Value {
		t.Fatal("separate browsers received the same session ID")
	}

	firstSessionRequest := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	firstSessionRequest.AddCookie(firstCookie)
	first, ok := store.get(firstSessionRequest)
	if !ok || first.currentAccount.Nickname != "first" {
		t.Fatal("first browser did not receive its own session state")
	}

	secondSessionRequest := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	secondSessionRequest.AddCookie(secondCookie)
	second, ok := store.get(secondSessionRequest)
	if !ok || second.currentAccount.Nickname != "second" {
		t.Fatal("second browser did not receive its own session state")
	}

	logoutResponse := httptest.NewRecorder()
	store.delete(firstSessionRequest, logoutResponse)
	if _, ok := store.get(firstSessionRequest); ok {
		t.Fatal("logout did not remove the current browser session")
	}
	if _, ok := store.get(secondSessionRequest); !ok {
		t.Fatal("logout removed another browser session")
	}
	if cookie := logoutResponse.Result().Cookies()[0]; cookie.MaxAge >= 0 {
		t.Fatal("logout did not expire the session cookie")
	}
}
