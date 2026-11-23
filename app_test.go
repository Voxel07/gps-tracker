package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func testApp(t *testing.T) (*app, string, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "tracker.sqlite")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	readDB, err := openReadDB(dbPath)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = readDB.Close()
		_ = db.Close()
	})
	a := &app{db: db, readDB: readDB, origin: "https://tracker.example", importSem: make(chan struct{}, 2)}
	runSQL := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	alice, bob := newToken(), newToken()
	runSQL("INSERT INTO users (id, issuer, subject, name) VALUES (1,'test','alice','Alice'),(2,'test','bob','Bob')")
	runSQL("INSERT INTO sessions VALUES (?, 1, ?), (?, 2, ?)", tokenHash(alice), time.Now().Add(time.Hour).Unix(), tokenHash(bob), time.Now().Add(time.Hour).Unix())
	return a, alice, bob
}

func request(t *testing.T, handler http.Handler, method, path, session, key string, body io.Reader, status int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Origin", "https://tracker.example")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "__Host-gps_session", Value: session})
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != status {
		if path == "/api/upload" || !strings.HasPrefix(path, "/api/route?") {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, out.Code, status, out.Body.String())
		}
		t.Fatalf("%s route returned %d, want %d", method, out.Code, status)
	}
	return out
}

func decode[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

type routeResult struct {
	Count    int
	Distance float64
	Points   []fix
	Latest   *fix
}

func TestAccountIsolationAndRouteEditing(t *testing.T) {
	a, alice, bob := testApp(t)
	handler := a.routes()
	key := func(session string) string {
		result := request(t, handler, "POST", "/api/keys", session, "", strings.NewReader(`{"name":"Phone"}`), 201)
		return decode[struct{ Key string }](t, result).Key
	}
	aliceKey, bobKey := key(alice), key(bob)
	upload := func(key, payload string, status int) {
		request(t, handler, "POST", "/api/upload", "", key, strings.NewReader(payload), status)
	}
	first := `{"lat":51.5,"lon":0.1,"time":"2025-01-01T09:00:00.123Z"}`
	upload(aliceKey, first, 204)
	upload(aliceKey, first, 204)
	upload(bobKey, first, 204)
	upload(aliceKey, `{"lat":52,"lon":0.2,"time":"2025-01-01T11:00:00Z"}`, 204)
	upload(aliceKey, `{"lon":0.2,"time":"2025-01-01T11:00:00Z"}`, 400)
	routePath := "/api/route?from=1735689600000&to=1735776000000&detail=1"
	request(t, handler, "GET", routePath, "", aliceKey, nil, 401)
	get := func(session string) routeResult {
		return decode[routeResult](t, request(t, handler, "GET", routePath, session, "", nil, 200))
	}
	route := get(alice)
	if route.Count != 2 || len(route.Points) != 2 || !route.Points[1].BreakBefore || get(bob).Count != 1 {
		t.Fatal("upload did not preserve the gap or isolate accounts")
	}
	left, right := route.Points[0], route.Points[1]
	move := fmt.Sprintf("/api/points/%d", left.ID)
	edit := `{"lat":50,"lon":0.1,"revision":0}`
	request(t, handler, "PATCH", move, bob, "", strings.NewReader(edit), 409)
	request(t, handler, "PATCH", move, alice, "", strings.NewReader(edit), 204)
	request(t, handler, "PATCH", move, alice, "", strings.NewReader(edit), 409)
	upload(aliceKey, first, 204)
	moved := get(alice).Points[0]
	if moved.Lat != 50 || moved.Time != left.Time || get(alice).Count != 2 {
		t.Fatal("edit changed the recording time or replay added a duplicate")
	}
	request(t, handler, "PATCH", move, alice, "", strings.NewReader(`{"revert":true,"revision":1}`), 204)
	if get(alice).Points[0].Lat != left.Lat {
		t.Fatal("original fix was not restored")
	}
	waypoint := fmt.Sprintf(`{"after":%d,"before":%d,"points":[{"lat":51.75,"lon":0.15,"time":%d}]}`, left.ID, right.ID, (left.Time+right.Time)/2)
	request(t, handler, "POST", "/api/points", alice, "", strings.NewReader(waypoint), 201)
	route = get(alice)
	if route.Count != 3 || !route.Points[1].Estimated || route.Points[2].BreakBefore {
		t.Fatal("inserted point did not connect the recording gap")
	}
	request(t, handler, "DELETE", "/api/keys/1", bob, "", nil, 404)
	request(t, handler, "DELETE", "/api/keys/1", alice, "", nil, 204)
	upload(aliceKey, first, 401)
	upload(bobKey, first, 204)
}

func TestImportPreviewAndRepeat(t *testing.T) {
	a, alice, bob := testApp(t)
	handler := a.routes()
	const gpx = `<gpx xmlns="http://www.topografix.com/GPX/1/1"><trk>
		<trkseg><trkpt lat="51.2" lon="0.1"><time>2025-01-01T09:00:00Z</time></trkpt></trkseg>
		<trkseg><trkpt lat="51.2" lon="0.2"/><trkpt lat="51.2" lon="0.3"><time>2025-01-01T09:01:00Z</time></trkpt></trkseg>
		</trk></gpx>`
	importFile := func(session string, preview bool) importSummary {
		path := "/api/import?format=gpx"
		if preview {
			path += "&preview=1"
		}
		out := request(t, handler, "POST", path, session, "", strings.NewReader(gpx), 200)
		return decode[importSummary](t, out)
	}
	initial := importFile(alice, true)
	committed := importFile(alice, false)
	repeated := importFile(alice, false)
	if initial.Added != 2 || committed.Added != 2 || repeated.Duplicates != 2 || committed.Invalid != 1 || importFile(bob, false).Added != 2 {
		t.Fatal("import preview, validation, or account-scoped deduplication failed")
	}
	result := request(t, handler, "GET", "/api/route?from=1735689600000&to=1735776000000", alice, "", nil, 200)
	route := decode[routeResult](t, result)
	if route.Count != 2 || !route.Points[1].BreakBefore {
		t.Fatal("GPX segment break was lost")
	}
	const csv = "time,lat,lon\n2025-01-01T09:01:00Z,51.2,0.3\n2025-01-01T10:00:00Z,51.3,0.3\n"
	summary := decode[importSummary](t, request(t, handler, "POST", "/api/import?format=csv", alice, "", strings.NewReader(csv), 200))
	if summary.Added != 1 || summary.Duplicates != 1 {
		t.Fatal("CSV import did not deduplicate against GPX")
	}
}

func TestRealGPXImport(t *testing.T) {
	path := os.Getenv("GPS_TRACKER_TEST_GPX")
	if path == "" {
		t.Skip("set GPS_TRACKER_TEST_GPX to run the real-file import test")
	}
	a, alice, _ := testApp(t)
	handler := a.routes()
	importFile := func(query string) importSummary {
		t.Helper()
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		started := time.Now()
		result := decode[importSummary](t, request(t, handler, "POST", "/api/import?format=gpx"+query, alice, "", file, 200))
		t.Logf("GPX %q took %s: %d valid, %d invalid, %d added, %d duplicates", query, time.Since(started).Round(time.Millisecond),
			result.Valid, result.Invalid, result.Added, result.Duplicates)
		return result
	}
	preview := importFile("&preview=1")
	saved := importFile("")
	repeat := importFile("")
	if preview.Added == 0 || saved.From == nil || saved.To == nil || preview.Added != saved.Added || repeat.Duplicates != repeat.Valid || repeat.Added != 0 {
		t.Fatal("real import preview, commit, or repeat failed")
	}
	started := time.Now()
	routePath := fmt.Sprintf("/api/route?from=%d&to=%d", *saved.From, *saved.To+1)
	response := request(t, handler, "GET", routePath, alice, "", nil, 200)
	result := decode[routeResult](t, response)
	if result.Count != saved.Added || result.Latest == nil || result.Latest.Time != *saved.To || len(result.Points) > 4000 {
		t.Fatal("real history route did not return the stored range or simplify its geometry")
	}
	t.Logf("full-range route took %s and returned %d of %d points", time.Since(started).Round(time.Millisecond), len(result.Points), result.Count)
}

func TestLargeImportRejectedAtomically(t *testing.T) {
	a, alice, _ := testApp(t)
	handler := a.routes()
	invalid := bytes.NewBufferString(`<gpx><trk><trkseg><trkpt lat="50" lon="2"><time>2025-01-01T10:01:00Z</time></trkpt></bad>`)
	request(t, handler, "POST", "/api/import?format=gpx", alice, "", invalid, 400)
	var count int
	if err := a.db.QueryRow("SELECT COUNT(*) FROM points").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid GPX partially committed")
	}
}

type testTransport func(*http.Request) (*http.Response, error)

func (test testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return test(r) }

func TestOIDCSignIn(t *testing.T) {
	a, _, _ := testApp(t)
	const issuer = "https://auth.example/application/o/tracker"
	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var nonce, challenge string
	encode := base64.RawURLEncoding.EncodeToString
	client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		var body any
		switch r.URL.Path {
		case "/application/o/tracker/.well-known/openid-configuration":
			body = map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token",
				"jwks_uri": issuer + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			}
		case "/application/o/tracker/token":
			data, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			form, err := url.ParseQuery(string(data))
			if err != nil || form.Get("code") != "fixture-code" || encode(tokenHash(form.Get("code_verifier"))) != challenge {
				return nil, fmt.Errorf("authorization exchange did not include the requested PKCE verifier")
			}
			claims, err := json.Marshal(map[string]any{
				"iss": issuer, "sub": "new-user", "aud": "tracker", "name": "New user",
				"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce,
			})
			if err != nil {
				return nil, err
			}
			input := encode([]byte(`{"alg":"RS256","kid":"auth-test"}`)) + "." + encode(claims)
			signature, err := rsa.SignPKCS1v15(rand.Reader, signingKey, crypto.SHA256, tokenHash(input))
			if err != nil {
				return nil, err
			}
			body = map[string]any{"access_token": "test", "token_type": "bearer", "id_token": input + "." + encode(signature)}
		case "/application/o/tracker/jwks":
			body = map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "kid": "auth-test", "alg": "RS256", "use": "sig",
				"n": encode(signingKey.N.Bytes()), "e": "AQAB",
			}}}
		default:
			return nil, fmt.Errorf("unexpected OIDC path %s", r.URL.Path)
		}
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
	ctx := oidc.ClientContext(context.Background(), client)
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		t.Fatal(err)
	}
	a.httpClient = client
	a.oidc = provider.VerifierContext(ctx, &oidc.Config{ClientID: "tracker"})
	a.oauth = oauth2.Config{ClientID: "tracker", ClientSecret: "fixture", Endpoint: provider.Endpoint(), RedirectURL: a.origin + "/auth/callback"}
	handler := a.routes()
	login := request(t, handler, "GET", "/auth/login?next="+url.QueryEscape("/?view=month"), "", "", nil, 302)
	target, err := url.Parse(login.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := target.Query()
	nonce, challenge = query.Get("nonce"), query.Get("code_challenge")
	if nonce == "" || challenge == "" || query.Get("code_challenge_method") != "S256" || query.Get("state") == "" {
		t.Fatal("login did not send state, nonce and PKCE challenge")
	}
	callback := httptest.NewRequest("GET", "/auth/callback?code=fixture-code&state="+url.QueryEscape(query.Get("state")), nil)
	for _, cookie := range login.Result().Cookies() {
		callback.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, callback)
	if response.Code != 303 || response.Header().Get("Location") != "/?view=month" {
		t.Fatalf("OIDC callback did not finish sign-in: %d %s", response.Code, response.Header().Get("Location"))
	}
	var session, refresh string
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "__Host-gps_session" && cookie.Secure && cookie.HttpOnly {
			session = cookie.Value
		}
		if cookie.Name == "__Host-gps_refresh" && cookie.Secure && cookie.HttpOnly {
			refresh = cookie.Value
		}
	}
	if session == "" || refresh == "" {
		t.Fatal("OIDC callback did not set both session and refresh cookies")
	}
	status := request(t, handler, "GET", "/api/session", session, "", nil, 200)
	if !strings.Contains(status.Body.String(), "New user") {
		t.Fatal("new account was not signed in")
	}
}

func TestImmichConnectionAndPhotoMatching(t *testing.T) {
	a, alice, bob := testApp(t)
	a.immichURL = "https://immich.example"
	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	a.secrets, err = cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	const tagged = "10000000-0000-4000-8000-000000000001"
	const untagged = "10000000-0000-4000-8000-000000000002"
	const outside = "10000000-0000-4000-8000-000000000003"
	a.httpClient = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("x-api-key") != "example-key" {
			t.Fatal("Immich request did not authenticate with the account key")
		}
		switch r.URL.Path {
		case "/api/search/metadata":
			body := fmt.Sprintf(`{"assets":{"items":[
			  {"id":%q,"fileCreatedAt":"2025-01-01T09:01:00Z","exifInfo":{"latitude":51.501,"longitude":0.1}},
			  {"id":%q,"fileCreatedAt":"2025-01-01T09:01:00Z"},
			  {"id":%q,"fileCreatedAt":"2025-01-01T09:01:00Z","exifInfo":{"latitude":30,"longitude":30}}
			],"nextPage":null}}`, tagged, untagged, outside)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		case "/api/assets/" + tagged + "/thumbnail":
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/jpeg"}}, Body: io.NopCloser(strings.NewReader("image"))}, nil
		default:
			return nil, fmt.Errorf("unexpected Immich path %s", r.URL.Path)
		}
	})}
	handler := a.routes()
	request(t, handler, "PUT", "/api/immich", alice, "", strings.NewReader(`{"key":"example-key"}`), 204)
	var encrypted []byte
	if err := a.db.QueryRow("SELECT immich_key FROM users WHERE id = 1").Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte("example-key")) {
		t.Fatal("Immich key was not encrypted at rest")
	}
	const csv = "time,lat,lon\n2025-01-01T09:00:00Z,51.5,0.1\n2025-01-01T09:02:00Z,51.502,0.1\n"
	request(t, handler, "POST", "/api/import?format=csv", alice, "", strings.NewReader(csv), 200)
	routePath := "/api/photos?from=1735689600000&to=1735776000000"
	getPhotos := func(session string) []photo {
		return decode[struct{ Photos []photo }](t, request(t, handler, "GET", routePath, session, "", nil, 200)).Photos
	}
	matches := getPhotos(alice)
	if len(matches) != 2 || matches[0].ID != tagged || matches[0].Estimated || matches[1].ID != untagged || !matches[1].Estimated || len(getPhotos(bob)) != 0 {
		t.Fatal("photo overlay did not match photos or isolate accounts")
	}
	request(t, handler, "GET", "/api/photos/"+tagged+"/thumbnail", alice, "", nil, 200)
	request(t, handler, "GET", "/api/photos/"+tagged+"/thumbnail", bob, "", nil, 404)
	request(t, handler, "DELETE", "/api/immich", alice, "", nil, 204)
	if len(getPhotos(alice)) != 0 {
		t.Fatal("Immich disconnect kept returning photos")
	}
}

func TestSecurityHeadersCompressionAndDeletion(t *testing.T) {
	a, alice, bob := testApp(t)
	handler := a.routes()
	const csv = "time,lat,lon\n2025-01-01T09:00:00Z,51.5,0.1\n2025-01-01T09:00:00Z,51.5,0.1\n2025-01-01T09:01:00Z,51.51,0.1\n"
	preview := decode[importSummary](t, request(t, handler, "POST", "/api/import?format=csv&preview=1", alice, "", strings.NewReader(csv), 200))
	if preview.Added != 2 || preview.Duplicates != 1 {
		t.Fatalf("read-only preview miscounted in-file duplicate: %+v", preview)
	}
	request(t, handler, "POST", "/api/import?format=csv", alice, "", strings.NewReader(csv), 200)

	// Verify gzip compression and security headers on /api/route
	req := httptest.NewRequest("GET", "/api/route?from=1735689600000&to=1735776000000&detail=1", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-gps_session", Value: alice})
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("gzip route returned %d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Content-Security-Policy") == "" || rec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("missing gzip or security headers: %v", rec.Header())
	}
	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	var route routeResult
	if err := json.NewDecoder(gz).Decode(&route); err != nil {
		t.Fatal(err)
	}
	_ = gz.Close()
	if route.Count != 2 || route.Distance < 1000 || route.Distance > 1200 {
		t.Fatalf("unexpected route count/distance: count=%d distance=%f", route.Count, route.Distance)
	}

	// Verify CSRF Origin rejection on mutating requests
	csrfReq := httptest.NewRequest("DELETE", fmt.Sprintf("/api/points/%d?revision=0", route.Points[0].ID), nil)
	csrfReq.AddCookie(&http.Cookie{Name: "__Host-gps_session", Value: alice})
	csrfReq.Header.Set("Origin", "https://evil.example")
	csrfRec := httptest.NewRecorder()
	handler.ServeHTTP(csrfRec, csrfReq)
	if csrfRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin DELETE, got %d", csrfRec.Code)
	}

	// Verify deletePoint account isolation, revision check, and success
	delPath := fmt.Sprintf("/api/points/%d", route.Points[0].ID)
	request(t, handler, "DELETE", delPath, alice, "", nil, 400)
	request(t, handler, "DELETE", delPath+"?revision=0", bob, "", nil, 409)
	request(t, handler, "DELETE", delPath+"?revision=9", alice, "", nil, 409)
	request(t, handler, "DELETE", delPath+"?revision=0", alice, "", nil, 204)
	after := decode[routeResult](t, request(t, handler, "GET", "/api/route?from=1735689600000&to=1735776000000&detail=1", alice, "", nil, 200))
	if after.Count != 1 {
		t.Fatalf("point was not deleted, count=%d", after.Count)
	}
}

func TestTrimOverviewAndValidationEdgeCases(t *testing.T) {
	points := make([]fix, 5000)
	for i := range points {
		points[i] = fix{ID: int64(i + 1), Time: int64(i * 1000), Stream: "s1", BreakBefore: i == 2500}
	}
	trimmed := trimOverview(points, 100)
	if len(trimmed) != 100 || trimmed[0].ID != 1 || trimmed[len(trimmed)-1].ID != 5000 {
		t.Fatalf("trimOverview did not preserve endpoints: len=%d first=%d last=%d", len(trimmed), trimmed[0].ID, trimmed[len(trimmed)-1].ID)
	}
	var breaks int
	for _, p := range trimmed {
		if p.BreakBefore {
			breaks++
		}
	}
	if breaks != 1 {
		t.Fatalf("trimOverview lost segment break: breaks=%d", breaks)
	}
	if !validServerURL("https://tracker.example", true) || !validServerURL("http://localhost:8080", true) {
		t.Fatal("valid origin rejected")
	}
	if validServerURL("http://tracker.example", true) || validServerURL("https://user:pass@tracker.example", false) || validServerURL("https://tracker.example/path", true) {
		t.Fatal("invalid server URL accepted")
	}
}

func TestSessionAndRefreshTokens(t *testing.T) {
	a, _, _ := testApp(t)
	handler := a.routes()

	// Issue a fresh session + refresh token pair for Alice (user 1)
	rec := httptest.NewRecorder()
	if _, err := a.issueTokens(context.Background(), rec, 1, nil); err != nil {
		t.Fatal(err)
	}
	var session1, refresh1 string
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case "__Host-gps_session":
			session1 = c.Value
		case "__Host-gps_refresh":
			refresh1 = c.Value
		}
	}
	if session1 == "" || refresh1 == "" {
		t.Fatalf("expected both session and refresh cookies, got session=%q refresh=%q", session1, refresh1)
	}

	// Expire session1 manually to simulate short-lived session expiration
	if _, err := a.db.Exec("UPDATE sessions SET expires = ? WHERE hash = ?", time.Now().Add(-time.Minute).Unix(), tokenHash(session1)); err != nil {
		t.Fatal(err)
	}
	// Session status reports refreshable=true
	statusReq := httptest.NewRequest("GET", "/api/session", nil)
	statusReq.AddCookie(&http.Cookie{Name: "__Host-gps_session", Value: session1})
	statusReq.AddCookie(&http.Cookie{Name: "__Host-gps_refresh", Value: refresh1})
	statusRec := httptest.NewRecorder()
	handler.ServeHTTP(statusRec, statusReq)
	statusBody := decode[struct {
		User        *account
		Refreshable bool
	}](t, statusRec)
	if statusBody.User != nil || !statusBody.Refreshable {
		t.Fatalf("expected user=nil and refreshable=true, got %+v", statusBody)
	}

	// Refresh rotates both tokens
	refreshReq := httptest.NewRequest("POST", "/auth/refresh", nil)
	refreshReq.Header.Set("Origin", "https://tracker.example")
	refreshReq.AddCookie(&http.Cookie{Name: "__Host-gps_refresh", Value: refresh1})
	refreshRec := httptest.NewRecorder()
	handler.ServeHTTP(refreshRec, refreshReq)
	if refreshRec.Code != 200 {
		t.Fatalf("refresh returned %d: %s", refreshRec.Code, refreshRec.Body.String())
	}
	var session2, refresh2 string
	for _, c := range refreshRec.Result().Cookies() {
		switch c.Name {
		case "__Host-gps_session":
			session2 = c.Value
		case "__Host-gps_refresh":
			refresh2 = c.Value
		}
	}
	if session2 == "" || refresh2 == "" || session2 == session1 || refresh2 == refresh1 {
		t.Fatal("refresh did not rotate session and refresh tokens")
	}

	// New session works on protected route
	request(t, handler, "GET", "/api/keys", session2, "", nil, 200)

	// A concurrent reuse right after rotation (e.g. two tabs) is rejected
	// without revoking the family.
	graceReq := httptest.NewRequest("POST", "/auth/refresh", nil)
	graceReq.Header.Set("Origin", "https://tracker.example")
	graceReq.AddCookie(&http.Cookie{Name: "__Host-gps_refresh", Value: refresh1})
	graceRec := httptest.NewRecorder()
	handler.ServeHTTP(graceRec, graceReq)
	if graceRec.Code != 401 || len(graceRec.Result().Cookies()) != 0 {
		t.Fatalf("reuse within grace returned %d with cookies %v, want 401 without cookies", graceRec.Code, graceRec.Result().Cookies())
	}
	request(t, handler, "GET", "/api/keys", session2, "", nil, 200)

	// Reusing old refresh1 later revokes the entire token family
	if _, err := a.db.Exec("UPDATE refresh_tokens SET revoked = ? WHERE hash = ?", time.Now().Add(-time.Minute).Unix(), tokenHash(refresh1)); err != nil {
		t.Fatal(err)
	}
	reuseReq := httptest.NewRequest("POST", "/auth/refresh", nil)
	reuseReq.Header.Set("Origin", "https://tracker.example")
	reuseReq.AddCookie(&http.Cookie{Name: "__Host-gps_refresh", Value: refresh1})
	reuseRec := httptest.NewRecorder()
	handler.ServeHTTP(reuseRec, reuseReq)
	if reuseRec.Code != 401 {
		t.Fatalf("reused refresh token returned %d, want 401", reuseRec.Code)
	}

	// Because the family was revoked, refresh2 and session2 must now also fail
	afterReuse := httptest.NewRequest("POST", "/auth/refresh", nil)
	afterReuse.Header.Set("Origin", "https://tracker.example")
	afterReuse.AddCookie(&http.Cookie{Name: "__Host-gps_refresh", Value: refresh2})
	afterReuseRec := httptest.NewRecorder()
	handler.ServeHTTP(afterReuseRec, afterReuse)
	if afterReuseRec.Code != 401 {
		t.Fatalf("revoked family refresh2 returned %d, want 401", afterReuseRec.Code)
	}
	request(t, handler, "GET", "/api/keys", session2, "", nil, 401)
}

func TestRecentUploadsPointTimeEditAndStreamImport(t *testing.T) {
	a, alice, bob := testApp(t)
	handler := a.routes()

	keyResp := request(t, handler, "POST", "/api/keys", alice, "", strings.NewReader(`{"name":"Pixel"}`), 201)
	aliceKey := decode[struct{ Key string }](t, keyResp).Key

	// Upload 12 points (including 1 duplicate)
	base := time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)
	for i := range 11 {
		payload := fmt.Sprintf(`{"lat":%.4f,"lon":0.1,"time":%q}`, 51.0+float64(i)*0.01, base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339))
		request(t, handler, "POST", "/api/upload", "", aliceKey, strings.NewReader(payload), 204)
	}
	dupPayload := fmt.Sprintf(`{"lat":51.1000,"lon":0.1,"time":%q}`, base.Add(10*time.Minute).Format(time.RFC3339))
	request(t, handler, "POST", "/api/upload", "", aliceKey, strings.NewReader(dupPayload), 204)

	recent := decode[[]recentUpload](t, request(t, handler, "GET", "/api/points/recent", alice, "", nil, 200))
	if len(recent) != 10 {
		t.Fatalf("expected 10 recent uploads, got %d", len(recent))
	}
	if !recent[0].Duplicate || recent[0].DeviceName != "Pixel" {
		t.Fatalf("unexpected newest recent upload: %+v", recent[0])
	}
	bobRecent := decode[[]recentUpload](t, request(t, handler, "GET", "/api/points/recent", bob, "", nil, 200))
	if len(bobRecent) != 0 {
		t.Fatalf("recent uploads leaked to bob: %d", len(bobRecent))
	}

	// Edit a point's timestamp and coordinates, then revert
	routePath := fmt.Sprintf("/api/route?from=%d&to=%d&detail=1", base.Add(-time.Hour).UnixMilli(), base.Add(2*time.Hour).UnixMilli())
	route := decode[routeResult](t, request(t, handler, "GET", routePath, alice, "", nil, 200))
	first := route.Points[0]
	newTime := first.Time + 30_000
	patchBody := fmt.Sprintf(`{"lat":51.55,"lon":0.22,"time":%d,"revision":%d}`, newTime, first.Revision)
	request(t, handler, "PATCH", fmt.Sprintf("/api/points/%d", first.ID), alice, "", strings.NewReader(patchBody), 204)
	afterEdit := decode[routeResult](t, request(t, handler, "GET", routePath, alice, "", nil, 200))
	if afterEdit.Points[0].Time != newTime || afterEdit.Points[0].Lat != 51.55 || !afterEdit.Points[0].Corrected {
		t.Fatalf("point time/coord edit did not persist: %+v", afterEdit.Points[0])
	}
	request(t, handler, "PATCH", fmt.Sprintf("/api/points/%d", first.ID), alice, "", strings.NewReader(`{"revert":true,"revision":1}`), 204)
	afterRevert := decode[routeResult](t, request(t, handler, "GET", routePath, alice, "", nil, 200))
	if afterRevert.Points[0].Time != first.Time || afterRevert.Points[0].Lat != first.Lat || afterRevert.Points[0].Corrected {
		t.Fatalf("revert did not restore original time and coordinates: %+v", afterRevert.Points[0])
	}

	// Verify streaming GPX preview and import NDJSON output
	const gpx = `<gpx xmlns="http://www.topografix.com/GPX/1/1"><trk><trkseg>
		<trkpt lat="48.1" lon="11.5"><time>2025-03-01T10:00:00Z</time></trkpt>
		<trkpt lat="48.2" lon="11.6"><time>2025-03-01T10:05:00Z</time></trkpt>
	</trkseg></trk></gpx>`
	for _, path := range []string{"/api/import?format=gpx&preview=1&stream=1", "/api/import?format=gpx&preview=0&stream=1"} {
		rec := request(t, handler, "POST", path, alice, "", strings.NewReader(gpx), 200)
		lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
		if len(lines) < 2 {
			t.Fatalf("expected streaming NDJSON events for %s, got %q", path, rec.Body.String())
		}
		var last importProgress
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
			t.Fatal(err)
		}
		if last.Type != "done" || last.Summary == nil || last.Summary.Added != 2 {
			t.Fatalf("unexpected final stream event for %s: %+v", path, last)
		}
	}
}

func TestExportGPX(t *testing.T) {
	a, alice, bob := testApp(t)
	handler := a.routes()

	const inputGPX = `<gpx xmlns="http://www.topografix.com/GPX/1/1"><trk>
		<trkseg>
			<trkpt lat="51.200000" lon="0.100000"><time>2025-04-01T09:00:00Z</time></trkpt>
			<trkpt lat="51.210000" lon="0.110000"><time>2025-04-01T09:02:00Z</time></trkpt>
		</trkseg>
		<trkseg>
			<trkpt lat="51.250000" lon="0.150000"><time>2025-04-01T11:00:00Z</time></trkpt>
		</trkseg>
		</trk><trk><trkseg>
			<trkpt lat="52.000000" lon="1.000000"><time>2025-04-05T10:00:00Z</time></trkpt>
		</trkseg></trk></gpx>`
	request(t, handler, "POST", "/api/import?format=gpx", alice, "", strings.NewReader(inputGPX), 200)

	from := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	to := time.Date(2025, 4, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
	path := fmt.Sprintf("/api/export?from=%d&to=%d", from, to)

	// Invalid range and empty account/range checks
	request(t, handler, "GET", fmt.Sprintf("/api/export?from=%d&to=%d", to, from), alice, "", nil, 400)
	request(t, handler, "GET", path, bob, "", nil, 404)

	// Export April 1 only (should contain 3 points, 2 trkseg, exclude April 5 point)
	rec := request(t, handler, "GET", path, alice, "", nil, 200)
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/gpx+xml") {
		t.Fatalf("unexpected Content-Type: %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "gps-tracker-2025-04-01.gpx") {
		t.Fatalf("unexpected Content-Disposition: %q", rec.Header().Get("Content-Disposition"))
	}
	points, invalid, err := parseGPX(strings.NewReader(rec.Body.String()), nil)
	if err != nil {
		t.Fatalf("exported GPX failed to parse: %v", err)
	}
	if len(points) != 3 || invalid != 0 || !points[2].BreakBefore {
		t.Fatalf("exported GPX mismatch: len=%d invalid=%d breakBefore=%v", len(points), invalid, len(points) == 3 && points[2].BreakBefore)
	}
}

func TestWebAppRequiresSignIn(t *testing.T) {
	a, alice, _ := testApp(t)
	page := func(path, mode string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		if mode != "" {
			req.Header.Set("Sec-Fetch-Mode", mode)
		}
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		a.routes().ServeHTTP(rec, req)
		return rec
	}

	// Without Authentik configured the app stays locked.
	if rec := page("/", "navigate"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured sign-in served the app with %d", rec.Code)
	}

	a.oidc = oidc.NewVerifier("https://auth.example", &oidc.StaticKeySet{}, &oidc.Config{ClientID: "tracker"})
	a.oauth = oauth2.Config{ClientID: "tracker", Endpoint: oauth2.Endpoint{AuthURL: "https://auth.example/authorize"}, RedirectURL: a.origin + "/auth/callback"}

	rec := page("/?view=month", "navigate")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/auth/login?next="+url.QueryEscape("/?view=month") {
		t.Fatalf("signed-out navigation returned %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := page("/assets/index.js", "no-cors"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("signed-out asset request returned %d, want 401", rec.Code)
	}
	if rec := page("/sw.js", "same-origin"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("signed-out service worker request returned %d, want 401", rec.Code)
	}
	for _, path := range []string{"/manifest.webmanifest", "/icons/icon-192.png"} {
		if rec := page(path, "cors"); rec.Code == http.StatusUnauthorized || rec.Code == http.StatusFound {
			t.Fatalf("install file %s returned %d, want it to stay public", path, rec.Code)
		}
	}
	if rec := page("/", "navigate", &http.Cookie{Name: "__Host-gps_session", Value: alice}); rec.Code == http.StatusFound || rec.Code == http.StatusUnauthorized {
		t.Fatalf("signed-in navigation returned %d", rec.Code)
	}

	// An expired session with a valid refresh token is renewed on navigation.
	issued := httptest.NewRecorder()
	if _, err := a.issueTokens(context.Background(), issued, 1, nil); err != nil {
		t.Fatal(err)
	}
	var refresh string
	for _, cookie := range issued.Result().Cookies() {
		if cookie.Name == "__Host-gps_refresh" {
			refresh = cookie.Value
		}
	}
	rec = page("/", "navigate", &http.Cookie{Name: "__Host-gps_refresh", Value: refresh})
	var renewed string
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "__Host-gps_session" && cookie.MaxAge > 0 {
			renewed = cookie.Value
		}
	}
	if rec.Code == http.StatusFound || renewed == "" {
		t.Fatalf("navigation with refresh token returned %d without a new session", rec.Code)
	}
	request(t, a.routes(), "GET", "/api/keys", renewed, "", nil, 200)
	// Asset requests never rotate tokens, so parallel loads cannot trip reuse detection.
	if rec := page("/assets/index.js", "no-cors", &http.Cookie{Name: "__Host-gps_refresh", Value: refresh}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("asset request with only a refresh token returned %d", rec.Code)
	}

	// Failed callbacks and sign-out show a public page instead of looping back to Authentik.
	if rec := page("/auth/callback?code=x&state=y", "navigate"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Sign-in failed") {
		t.Fatalf("failed callback returned %d", rec.Code)
	}
	if rec := page("/auth/signed-out", "navigate"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `href="/auth/login"`) {
		t.Fatalf("signed-out page returned %d", rec.Code)
	}

	for input, want := range map[string]string{
		"/?view=month": "/?view=month", "": "/", "//evil.example": "/", `/\evil.example`: "/",
		"https://evil.example": "/", "/auth/login": "/", "/x\r\nSet-Cookie: a=b": "/",
	} {
		if got := safeNext(input); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", input, got, want)
		}
	}
}
