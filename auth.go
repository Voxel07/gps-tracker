package main

import (
	"context"
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

const (
	sessionTokenAge = 15 * time.Minute
	refreshTokenAge = 30 * 24 * time.Hour
)

type account struct {
	ID   int64  `json:"-"`
	Name string `json:"name"`
}

func (a *app) issueTokens(ctx context.Context, w http.ResponseWriter, userID int64, familyID []byte) (int64, error) {
	if len(familyID) == 0 {
		familyID = randBytes(32)
	}
	session := newToken()
	refresh := newToken()
	now := time.Now()
	sessionExpires := now.Add(sessionTokenAge).Unix()
	refreshExpires := now.Add(refreshTokenAge).Unix()
	sessionHash := tokenHash(session)
	refreshHash := tokenHash(refresh)

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO sessions (hash, user_id, expires) VALUES (?, ?, ?)", sessionHash, userID, sessionExpires); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO refresh_tokens (hash, family_id, user_id, session_hash, expires) VALUES (?, ?, ?, ?, ?)",
		refreshHash, familyID, userID, sessionHash, refreshExpires); err != nil {
		return 0, err
	}
	_, _ = tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires <= ?", now.Unix())
	_, _ = tx.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE expires <= ?", now.Unix())
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	a.cookie(w, "gps_session", session, int(sessionTokenAge.Seconds()))
	a.cookie(w, "gps_refresh", refresh, int(refreshTokenAge.Seconds()))
	return sessionExpires * 1000, nil
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
	if _, err := a.issueTokens(ctx, w, userID, nil); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) lookupSession(r *http.Request) (account, int64, error) {
	var user account
	var expires int64
	token := a.cookieValue(r, "gps_session")
	if token == "" {
		return user, 0, sql.ErrNoRows
	}
	err := a.reader().QueryRowContext(r.Context(), `SELECT users.id, users.name, sessions.expires FROM sessions JOIN users ON users.id = sessions.user_id
		WHERE sessions.hash = ? AND sessions.expires > ?`, tokenHash(token), time.Now().Unix()).Scan(&user.ID, &user.Name, &expires)
	return user, expires * 1000, err
}

func (a *app) hasRefreshToken(r *http.Request) bool {
	token := a.cookieValue(r, "gps_refresh")
	if token == "" {
		return false
	}
	var exists bool
	err := a.reader().QueryRowContext(r.Context(),
		"SELECT EXISTS(SELECT 1 FROM refresh_tokens WHERE hash = ? AND revoked IS NULL AND expires > ?)",
		tokenHash(token), time.Now().Unix()).Scan(&exists)
	return err == nil && exists
}

func (a *app) sessionStatus(w http.ResponseWriter, r *http.Request) {
	user, expiresAt, err := a.lookupSession(r)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		serverError(w, err)
		return
	}
	var signedIn *account
	var exp *int64
	if err == nil {
		signedIn = &user
		exp = &expiresAt
	}
	refreshable := signedIn == nil && a.hasRefreshToken(r)
	jsonResponse(w, http.StatusOK, map[string]any{
		"user":            signedIn,
		"expiresAt":       exp,
		"refreshable":     refreshable,
		"signInAvailable": a.oidc != nil,
	})
}

func (a *app) refresh(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != a.origin {
		problem(w, http.StatusForbidden, "Request must come from the tracker")
		return
	}
	rawRefresh := a.cookieValue(r, "gps_refresh")
	if rawRefresh == "" {
		a.cookie(w, "gps_session", "", -1)
		a.cookie(w, "gps_refresh", "", -1)
		problem(w, http.StatusUnauthorized, "Sign in to continue")
		return
	}
	ctx := r.Context()
	now := time.Now()
	nowUnix := now.Unix()
	refreshHash := tokenHash(rawRefresh)

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		serverError(w, err)
		return
	}
	defer tx.Rollback()

	var familyID, oldSessionHash []byte
	var userID, expires int64
	var revoked sql.NullInt64
	err = tx.QueryRowContext(ctx,
		"SELECT family_id, user_id, session_hash, expires, revoked FROM refresh_tokens WHERE hash = ?",
		refreshHash).Scan(&familyID, &userID, &oldSessionHash, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		a.cookie(w, "gps_session", "", -1)
		a.cookie(w, "gps_refresh", "", -1)
		problem(w, http.StatusUnauthorized, "Sign in to continue")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}

	if revoked.Valid {
		// Refresh token reuse detected: revoke the entire token family.
		_, _ = tx.ExecContext(ctx, "DELETE FROM sessions WHERE hash IN (SELECT session_hash FROM refresh_tokens WHERE family_id = ? AND session_hash IS NOT NULL)", familyID)
		_, _ = tx.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE family_id = ?", familyID)
		_ = tx.Commit()
		a.cookie(w, "gps_session", "", -1)
		a.cookie(w, "gps_refresh", "", -1)
		problem(w, http.StatusUnauthorized, "Session revoked. Sign in again.")
		return
	}

	if expires <= nowUnix {
		_, _ = tx.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE hash = ?", refreshHash)
		_ = tx.Commit()
		a.cookie(w, "gps_session", "", -1)
		a.cookie(w, "gps_refresh", "", -1)
		problem(w, http.StatusUnauthorized, "Session expired. Sign in again.")
		return
	}

	var user account
	if err := tx.QueryRowContext(ctx, "SELECT id, name FROM users WHERE id = ?", userID).Scan(&user.ID, &user.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.cookie(w, "gps_session", "", -1)
			a.cookie(w, "gps_refresh", "", -1)
			problem(w, http.StatusUnauthorized, "Sign in to continue")
			return
		}
		serverError(w, err)
		return
	}

	if _, err := tx.ExecContext(ctx, "UPDATE refresh_tokens SET revoked = ? WHERE hash = ?", nowUnix, refreshHash); err != nil {
		serverError(w, err)
		return
	}
	if len(oldSessionHash) > 0 {
		_, _ = tx.ExecContext(ctx, "DELETE FROM sessions WHERE hash = ?", oldSessionHash)
	}

	newSession := newToken()
	newRefresh := newToken()
	newSessionHash := tokenHash(newSession)
	newRefreshHash := tokenHash(newRefresh)
	sessionExpires := now.Add(sessionTokenAge).Unix()
	refreshExpires := now.Add(refreshTokenAge).Unix()

	if _, err := tx.ExecContext(ctx, "INSERT INTO sessions (hash, user_id, expires) VALUES (?, ?, ?)", newSessionHash, userID, sessionExpires); err != nil {
		serverError(w, err)
		return
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO refresh_tokens (hash, family_id, user_id, session_hash, expires) VALUES (?, ?, ?, ?, ?)",
		newRefreshHash, familyID, userID, newSessionHash, refreshExpires); err != nil {
		serverError(w, err)
		return
	}
	_, _ = tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires <= ?", nowUnix)
	_, _ = tx.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE expires <= ?", nowUnix)

	if err := tx.Commit(); err != nil {
		serverError(w, err)
		return
	}
	a.cookie(w, "gps_session", newSession, int(sessionTokenAge.Seconds()))
	a.cookie(w, "gps_refresh", newRefresh, int(refreshTokenAge.Seconds()))
	jsonResponse(w, http.StatusOK, map[string]any{"user": user, "expiresAt": sessionExpires * 1000})
}

func (a *app) signedIn(handler func(http.ResponseWriter, *http.Request, account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != a.origin {
			problem(w, http.StatusForbidden, "Request must come from the tracker")
			return
		}
		user, _, err := a.lookupSession(r)
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

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != a.origin {
		problem(w, http.StatusForbidden, "Request must come from the tracker")
		return
	}
	ctx := r.Context()
	if session := a.cookieValue(r, "gps_session"); session != "" {
		if _, err := a.db.ExecContext(ctx, "DELETE FROM sessions WHERE hash = ?", tokenHash(session)); err != nil {
			serverError(w, err)
			return
		}
	}
	if refresh := a.cookieValue(r, "gps_refresh"); refresh != "" {
		tx, err := a.db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		var familyID []byte
		err = tx.QueryRowContext(ctx, "SELECT family_id FROM refresh_tokens WHERE hash = ?", tokenHash(refresh)).Scan(&familyID)
		if err == nil && len(familyID) > 0 {
			_, _ = tx.ExecContext(ctx, "DELETE FROM sessions WHERE hash IN (SELECT session_hash FROM refresh_tokens WHERE family_id = ? AND session_hash IS NOT NULL)", familyID)
			_, _ = tx.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE family_id = ?", familyID)
		}
		_ = tx.Commit()
	}
	a.cookie(w, "gps_session", "", -1)
	a.cookie(w, "gps_refresh", "", -1)
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
