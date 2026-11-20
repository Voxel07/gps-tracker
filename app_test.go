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
	login := request(t, handler, "GET", "/auth/login", "", "", nil, 302)
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
	if response.Code != 303 || response.Header().Get("Location") != "/" {
		t.Fatal("OIDC callback did not finish sign-in")
	}
	var session string
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "__Host-gps_session" && cookie.Secure && cookie.HttpOnly {
			session = cookie.Value
		}
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
