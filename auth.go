package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
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

// safeNext limits post-login redirects to local paths of the tracker itself.
func safeNext(value string) string {
	if value == "" || len(value) > 1024 || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") ||
		strings.HasPrefix(value, "/auth/") || strings.ContainsAny(value, "\\\r\n\t") {
		return "/"
	}
	return value
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		signInUnavailable(w)
		return
	}
	state, nonce, verifier := newToken(), newToken(), oauth2.GenerateVerifier()
	next := base64.RawURLEncoding.EncodeToString([]byte(safeNext(r.URL.Query().Get("next"))))
	a.cookie(w, "gps_login", state+"."+nonce+"."+verifier+"."+next, 600)
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *app) signInFailed(w http.ResponseWriter) {
	authPage(w, http.StatusBadRequest, "Sign-in failed",
		"Authentik did not complete the sign-in. Your account may not have access to GPS Tracker, or the sign-in took too long.",
		"/auth/login", "Try again")
}

func (a *app) signedOut(w http.ResponseWriter, r *http.Request) {
	authPage(w, http.StatusOK, "Signed out", "You have signed out of GPS Tracker.", "/auth/login", "Sign in again")
}

func signInUnavailable(w http.ResponseWriter) {
	authPage(w, http.StatusServiceUnavailable, "Sign-in unavailable",
		"Authentik sign-in is not configured. Ask your server operator to set OIDC_ISSUER, OIDC_CLIENT_ID and OIDC_CLIENT_SECRET.", "", "")
}

func (a *app) callback(w http.ResponseWriter, r *http.Request) {
	flow := strings.Split(a.cookieValue(r, "gps_login"), ".")
	a.cookie(w, "gps_login", "", -1)
	code := r.URL.Query().Get("code")
	if a.oidc == nil || len(flow) != 4 || code == "" || subtle.ConstantTimeCompare([]byte(flow[0]), []byte(r.URL.Query().Get("state"))) != 1 {
		log.Print("OIDC callback rejected: missing login cookie, authorization code, or state mismatch")
		a.signInFailed(w)
		return
	}
	next := "/"
	if decoded, err := base64.RawURLEncoding.DecodeString(flow[3]); err == nil {
		next = safeNext(string(decoded))
	}
	ctx := oidc.ClientContext(r.Context(), a.httpClient)
	token, err := a.oauth.Exchange(ctx, code, oauth2.VerifierOption(flow[2]))
	if err != nil {
		log.Printf("OIDC token exchange failed: %v", err)
		a.signInFailed(w)
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
		a.signInFailed(w)
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
	http.Redirect(w, r, next, http.StatusSeeOther)
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

// refreshReuseGrace tolerates a second use of a refresh token shortly after it
// was rotated, for example when several tabs reopen at the same time. Such a
// request is rejected but does not revoke the token family.
const refreshReuseGrace = 30

func (a *app) clearSessionCookies(w http.ResponseWriter) {
	a.cookie(w, "gps_session", "", -1)
	a.cookie(w, "gps_refresh", "", -1)
}

// rotateRefresh exchanges a refresh token for a new session and refresh token
// and sets both cookies. A non-empty reason means the caller must sign in again.
func (a *app) rotateRefresh(ctx context.Context, w http.ResponseWriter, rawRefresh string) (user account, expiresAt int64, reason string, err error) {
	if rawRefresh == "" {
		a.clearSessionCookies(w)
		return user, 0, "Sign in to continue", nil
	}
	now := time.Now()
	nowUnix := now.Unix()
	refreshHash := tokenHash(rawRefresh)

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return user, 0, "", err
	}
	defer tx.Rollback()

	var familyID, oldSessionHash []byte
	var userID, expires int64
	var revoked sql.NullInt64
	err = tx.QueryRowContext(ctx,
		"SELECT family_id, user_id, session_hash, expires, revoked FROM refresh_tokens WHERE hash = ?",
		refreshHash).Scan(&familyID, &userID, &oldSessionHash, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		a.clearSessionCookies(w)
		return user, 0, "Sign in to continue", nil
	}
	if err != nil {
		return user, 0, "", err
	}

	if revoked.Valid {
		if nowUnix-revoked.Int64 <= refreshReuseGrace {
			// A concurrent request already rotated this token. Leave the cookies
			// alone so the newer tokens set by that request survive.
			return user, 0, "Sign in to continue", nil
		}
		// Refresh token reuse detected: revoke the entire token family.
		_, _ = tx.ExecContext(ctx, "DELETE FROM sessions WHERE hash IN (SELECT session_hash FROM refresh_tokens WHERE family_id = ? AND session_hash IS NOT NULL)", familyID)
		_, _ = tx.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE family_id = ?", familyID)
		_ = tx.Commit()
		a.clearSessionCookies(w)
		return user, 0, "Session revoked. Sign in again.", nil
	}

	if expires <= nowUnix {
		_, _ = tx.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE hash = ?", refreshHash)
		_ = tx.Commit()
		a.clearSessionCookies(w)
		return user, 0, "Session expired. Sign in again.", nil
	}

	if err := tx.QueryRowContext(ctx, "SELECT id, name FROM users WHERE id = ?", userID).Scan(&user.ID, &user.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.clearSessionCookies(w)
			return user, 0, "Sign in to continue", nil
		}
		return user, 0, "", err
	}

	if _, err := tx.ExecContext(ctx, "UPDATE refresh_tokens SET revoked = ? WHERE hash = ?", nowUnix, refreshHash); err != nil {
		return user, 0, "", err
	}
	if len(oldSessionHash) > 0 {
		_, _ = tx.ExecContext(ctx, "DELETE FROM sessions WHERE hash = ?", oldSessionHash)
	}

	newSession := newToken()
	newRefresh := newToken()
	newSessionHash := tokenHash(newSession)
	sessionExpires := now.Add(sessionTokenAge).Unix()
	refreshExpires := now.Add(refreshTokenAge).Unix()

	if _, err := tx.ExecContext(ctx, "INSERT INTO sessions (hash, user_id, expires) VALUES (?, ?, ?)", newSessionHash, userID, sessionExpires); err != nil {
		return user, 0, "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO refresh_tokens (hash, family_id, user_id, session_hash, expires) VALUES (?, ?, ?, ?, ?)",
		tokenHash(newRefresh), familyID, userID, newSessionHash, refreshExpires); err != nil {
		return user, 0, "", err
	}
	_, _ = tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires <= ?", nowUnix)
	_, _ = tx.ExecContext(ctx, "DELETE FROM refresh_tokens WHERE expires <= ?", nowUnix)

	if err := tx.Commit(); err != nil {
		return user, 0, "", err
	}
	a.cookie(w, "gps_session", newSession, int(sessionTokenAge.Seconds()))
	a.cookie(w, "gps_refresh", newRefresh, int(refreshTokenAge.Seconds()))
	return user, sessionExpires * 1000, "", nil
}

func (a *app) refresh(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != a.origin {
		problem(w, http.StatusForbidden, "Request must come from the tracker")
		return
	}
	user, expiresAt, reason, err := a.rotateRefresh(r.Context(), w, a.cookieValue(r, "gps_refresh"))
	if err != nil {
		serverError(w, err)
		return
	}
	if reason != "" {
		problem(w, http.StatusUnauthorized, reason)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"user": user, "expiresAt": expiresAt})
}

// isNavigation reports whether a browser is loading a page, as opposed to a
// script, stylesheet or fetch request.
func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// requireSignIn protects the web app itself. The manifest and icons stay public
// because browsers fetch them without cookies when installing the app.
func (a *app) requireSignIn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/manifest.webmanifest" || strings.HasPrefix(r.URL.Path, "/icons/") {
			next(w, r)
			return
		}
		if a.oidc == nil {
			signInUnavailable(w)
			return
		}
		_, _, err := a.lookupSession(r)
		if err == nil {
			next(w, r)
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			serverError(w, err)
			return
		}
		navigation := isNavigation(r)
		if refresh := a.cookieValue(r, "gps_refresh"); navigation && refresh != "" {
			_, _, reason, err := a.rotateRefresh(r.Context(), w, refresh)
			if err != nil {
				serverError(w, err)
				return
			}
			if reason == "" {
				next(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		if navigation {
			http.Redirect(w, r, "/auth/login?next="+url.QueryEscape(safeNext(r.URL.RequestURI())), http.StatusFound)
			return
		}
		problem(w, http.StatusUnauthorized, "Sign in to continue")
	}
}

var authPageTemplate = template.Must(template.New("auth").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<link rel="icon" href="/icons/icon-192.png">
<title>{{.Title}} · GPS Tracker</title>
<style>
:root { --bg: #f6f1e7; --panel: #ffffff; --text: #060517; --muted: #4b4a5c; --accent: #cf5c36; --on-accent: #ffffff; }
@media (prefers-color-scheme: dark) { :root { --bg: #060517; --panel: #12112a; --text: #f4ecdd; --muted: #b9b3c9; --accent: #efc88b; --on-accent: #060517; } }
* { box-sizing: border-box; }
body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 16px; background: var(--bg); color: var(--text); font: 16px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
main { width: 100%; max-width: 420px; padding: 32px 28px; border-radius: 16px; background: var(--panel); box-shadow: 0 12px 40px rgb(0 0 0 / 0.18); }
.brand { display: flex; align-items: center; gap: 10px; margin-bottom: 24px; font-weight: 600; }
.brand img { width: 32px; height: 32px; border-radius: 8px; }
h1 { margin: 0 0 8px; font-size: 1.4rem; }
p { margin: 0 0 24px; color: var(--muted); }
a.button { display: inline-block; padding: 10px 18px; border-radius: 10px; background: var(--accent); color: var(--on-accent); font-weight: 600; text-decoration: none; }
a.button:focus-visible { outline: 3px solid var(--text); outline-offset: 2px; }
</style>
</head>
<body>
<main>
<div class="brand"><img src="/icons/icon-192.png" alt="">GPS Tracker</div>
<h1>{{.Title}}</h1>
<p>{{.Message}}</p>
{{if .Link}}<a class="button" href="{{.Link}}">{{.LinkText}}</a>{{end}}
</main>
</body>
</html>
`))

func authPage(w http.ResponseWriter, status int, title, message, link, linkText string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := authPageTemplate.Execute(w, map[string]string{"Title": title, "Message": message, "Link": link, "LinkText": linkText}); err != nil {
		log.Printf("write auth page: %v", err)
	}
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
