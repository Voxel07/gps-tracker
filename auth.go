package main

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type account struct {
	ID   int64  `json:"-"`
	Name string `json:"name"`
}

func (a *app) cookie(w http.ResponseWriter, name, value string, age int) {
	if strings.HasPrefix(a.origin, "https://") {
		name = "__Host-" + name
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: age,
		HttpOnly: true, Secure: strings.HasPrefix(a.origin, "https://"), SameSite: http.SameSiteLaxMode})
}

func (a *app) cookieValue(r *http.Request, name string) string {
	if strings.HasPrefix(a.origin, "https://") {
		name = "__Host-" + name
	}
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		problem(w, http.StatusServiceUnavailable, "Authentik sign-in is not configured")
		return
	}
	state, nonce, verifier := newToken(), newToken(), oauth2.GenerateVerifier()
	a.cookie(w, "gps_login", state+"."+nonce+"."+verifier, 600)
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *app) callback(w http.ResponseWriter, r *http.Request) {
	flow := strings.Split(a.cookieValue(r, "gps_login"), ".")
	a.cookie(w, "gps_login", "", -1)
	code := r.URL.Query().Get("code")
	if a.oidc == nil || len(flow) != 3 || code == "" || subtle.ConstantTimeCompare([]byte(flow[0]), []byte(r.URL.Query().Get("state"))) != 1 {
		log.Print("OIDC callback rejected: missing login cookie, authorization code, or state mismatch")
		http.Redirect(w, r, "/?signin=failed", http.StatusSeeOther)
		return
	}
	ctx := oidc.ClientContext(r.Context(), a.httpClient)
	token, err := a.oauth.Exchange(ctx, code, oauth2.VerifierOption(flow[2]))
	if err != nil {
		log.Printf("OIDC token exchange failed: %v", err)
		http.Redirect(w, r, "/?signin=failed", http.StatusSeeOther)
		return
	}
	raw, _ := token.Extra("id_token").(string)
	verified, err := a.oidc.Verify(ctx, raw)
	if err != nil || verified.Subject == "" || subtle.ConstantTimeCompare([]byte(verified.Nonce), []byte(flow[1])) != 1 {
		if err != nil {
			log.Printf("OIDC ID token verification failed: %v", err)
		} else {
			log.Print("OIDC ID token rejected: missing subject or nonce mismatch")
		}
		http.Redirect(w, r, "/?signin=failed", http.StatusSeeOther)
		return
	}
	var claims struct {
		Name     string `json:"name"`
		Username string `json:"preferred_username"`
	}
	_ = verified.Claims(&claims)
	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = strings.TrimSpace(claims.Username)
	}
	if name == "" {
		name = "Signed-in user"
	}
	var userID int64
	err = a.db.QueryRowContext(ctx, `INSERT INTO users (issuer, subject, name) VALUES (?, ?, ?)
		ON CONFLICT(issuer, subject) DO UPDATE SET name = excluded.name RETURNING id`, verified.Issuer, verified.Subject, name).Scan(&userID)
	if err != nil {
		serverError(w, err)
		return
	}
	session := newToken()
	const sessionAge = 7 * 24 * time.Hour
	_, err = a.db.ExecContext(ctx, "INSERT INTO sessions VALUES (?, ?, ?)", tokenHash(session), userID, time.Now().Add(sessionAge).Unix())
	if err != nil {
		serverError(w, err)
		return
	}
	_, _ = a.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires <= ?", time.Now().Unix())
	a.cookie(w, "gps_session", session, int(sessionAge.Seconds()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) lookupSession(r *http.Request) (account, error) {
	var user account
	token := a.cookieValue(r, "gps_session")
	if token == "" {
		return user, sql.ErrNoRows
	}
	err := a.reader().QueryRowContext(r.Context(), `SELECT users.id, users.name FROM sessions JOIN users ON users.id = sessions.user_id
		WHERE sessions.hash = ? AND sessions.expires > ?`, tokenHash(token), time.Now().Unix()).Scan(&user.ID, &user.Name)
	return user, err
}

func (a *app) sessionStatus(w http.ResponseWriter, r *http.Request) {
	user, err := a.lookupSession(r)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		serverError(w, err)
		return
	}
	var signedIn *account
	if err == nil {
		signedIn = &user
	}
	jsonResponse(w, http.StatusOK, map[string]any{"user": signedIn, "signInAvailable": a.oidc != nil})
}

func (a *app) signedIn(handler func(http.ResponseWriter, *http.Request, account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != a.origin {
			problem(w, http.StatusForbidden, "Request must come from the tracker")
			return
		}
		user, err := a.lookupSession(r)
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, http.StatusUnauthorized, "Sign in to continue")
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		handler(w, r, user)
	}
}

func (a *app) logout(w http.ResponseWriter, r *http.Request, _ account) {
	_, err := a.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE hash = ?", tokenHash(a.cookieValue(r, "gps_session")))
	if err != nil {
		serverError(w, err)
		return
	}
	a.cookie(w, "gps_session", "", -1)
	w.WriteHeader(http.StatusNoContent)
}

type deviceKey struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	LastUsed *int64 `json:"lastUsed"`
	Revoked  *int64 `json:"revoked"`
}

func (a *app) listKeys(w http.ResponseWriter, r *http.Request, user account) {
	rows, err := a.reader().QueryContext(r.Context(), "SELECT id, name, last_used, revoked FROM device_keys WHERE user_id = ? ORDER BY id DESC", user.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()
	keys := make([]deviceKey, 0)
	for rows.Next() {
		var key deviceKey
		if err := rows.Scan(&key.ID, &key.Name, &key.LastUsed, &key.Revoked); err != nil {
			serverError(w, err)
			return
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		serverError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, keys)
}

func (a *app) createKey(w http.ResponseWriter, r *http.Request, user account) {
	var input struct{ Name string }
	if !readJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 100 {
		problem(w, http.StatusBadRequest, "Enter a device name up to 100 bytes")
		return
	}
	key := newToken()
	_, err := a.db.ExecContext(r.Context(), "INSERT INTO device_keys (user_id, name, hash) VALUES (?, ?, ?)", user.ID, input.Name, tokenHash(key))
	if err != nil {
		serverError(w, err)
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]string{"key": key, "url": a.origin + "/api/upload"})
}

func (a *app) revokeKey(w http.ResponseWriter, r *http.Request, user account) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid device key ID")
		return
	}
	result, err := a.db.ExecContext(r.Context(), "UPDATE device_keys SET revoked = ? WHERE id = ? AND user_id = ? AND revoked IS NULL",
		time.Now().Unix(), id, user.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		problem(w, http.StatusNotFound, "Active device key not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
