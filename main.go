package main

import (
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	_ "modernc.org/sqlite"
)

type app struct {
	db         *sql.DB
	readDB     *sql.DB
	origin     string
	oidc       *oidc.IDTokenVerifier
	oauth      oauth2.Config
	httpClient *http.Client
	immichURL  string
	secrets    cipher.AEAD
	importSem  chan struct{}
}

func (a *app) reader() *sql.DB {
	if a.readDB != nil {
		return a.readDB
	}
	return a.db
}

func (a *app) acquireImport() (func(), bool) {
	if a.importSem == nil {
		return func() {}, true
	}
	select {
	case a.importSem <- struct{}{}:
		return func() { <-a.importSem }, true
	default:
		return nil, false
	}
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		client := &http.Client{Timeout: 3 * time.Second}
		host, port, err := net.SplitHostPort(env("LISTEN_ADDR", ":8080"))
		if err != nil {
			log.Fatal(err)
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		res, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
		if err != nil {
			log.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			log.Fatalf("health returned %s", res.Status)
		}
		return
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dbPath := env("DATABASE_PATH", "tracker.sqlite")
	db, err := openDB(dbPath)
	if err != nil {
		return err
	}
	readDB, err := openReadDB(dbPath)
	if err != nil {
		_ = db.Close()
		return err
	}
	defer func() {
		_ = readDB.Close()
		_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		_ = db.Close()
	}()
	origin := strings.TrimRight(env("APP_URL", "http://localhost:8080"), "/")
	if !validServerURL(origin, true) {
		return errors.New("APP_URL must be an HTTPS origin or an HTTP localhost origin")
	}
	immich := strings.TrimRight(os.Getenv("IMMICH_URL"), "/")
	if immich != "" && !validServerURL(immich, false) {
		return errors.New("IMMICH_URL must be a server URL without credentials, query or fragment")
	}
	client := &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	a := &app{
		db:         db,
		readDB:     readDB,
		origin:     origin,
		immichURL:  immich,
		httpClient: client,
		importSem:  make(chan struct{}, 2),
	}
	if immich != "" {
		secret, err := base64.StdEncoding.DecodeString(os.Getenv("APP_ENCRYPTION_KEY"))
		if err != nil || len(secret) != 32 {
			return errors.New("APP_ENCRYPTION_KEY must be a base64-encoded 32-byte key when Immich is enabled")
		}
		block, err := aes.NewCipher(secret)
		if err != nil {
			return err
		}
		a.secrets, err = cipher.NewGCM(block)
		if err != nil {
			return err
		}
	}
	issuer, clientID, clientSecret := os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_CLIENT_ID"), os.Getenv("OIDC_CLIENT_SECRET")
	if issuer != "" || clientID != "" || clientSecret != "" {
		if issuer == "" || clientID == "" || clientSecret == "" {
			return errors.New("OIDC_ISSUER, OIDC_CLIENT_ID and OIDC_CLIENT_SECRET must all be set")
		}
		if !validServerURL(issuer, false) {
			return errors.New("OIDC_ISSUER must be a server URL without credentials, query or fragment")
		}
		discovery, cancel := context.WithTimeout(oidc.ClientContext(ctx, client), 15*time.Second)
		provider, err := oidc.NewProvider(discovery, issuer)
		cancel()
		if err != nil {
			return fmt.Errorf("OIDC discovery: %w", err)
		}
		a.oidc = provider.VerifierContext(oidc.ClientContext(context.Background(), client), &oidc.Config{ClientID: clientID})
		a.oauth = oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: origin + "/auth/callback", Endpoint: provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "profile"}}
	} else {
		log.Print("Authentik is unconfigured. Browser sign-in is unavailable.")
	}
	server := &http.Server{
		Addr: env("LISTEN_ADDR", ":8080"), Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 3 * time.Minute, WriteTimeout: 4 * time.Minute, IdleTimeout: 60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		end, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(end)
	}()
	log.Printf("Listening on %s", server.Addr)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func validServerURL(value string, originOnly bool) bool {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if originOnly && u.Path != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && (!originOnly || u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
}

const contentSecurityPolicy = "default-src 'self'; img-src 'self' https://tile.openstreetmap.org https://*.basemaps.cartocdn.com https://*.tile.opentopomap.org https://server.arcgisonline.com data:; style-src 'self' 'unsafe-inline'; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.reader().PingContext(r.Context()); err != nil {
			problem(w, http.StatusServiceUnavailable, "Database unavailable")
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET /auth/callback", a.callback)
	mux.HandleFunc("POST /auth/refresh", a.refresh)
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.HandleFunc("POST /api/upload", a.upload)
	mux.HandleFunc("GET /api/session", a.sessionStatus)
	mux.HandleFunc("GET /api/keys", a.signedIn(a.listKeys))
	mux.HandleFunc("POST /api/keys", a.signedIn(a.createKey))
	mux.HandleFunc("DELETE /api/keys/{id}", a.signedIn(a.revokeKey))
	mux.HandleFunc("GET /api/route", a.signedIn(a.getRoute))
	mux.HandleFunc("GET /api/points/recent", a.signedIn(a.recentUploads))
	mux.HandleFunc("PATCH /api/points/{id}", a.signedIn(a.movePoint))
	mux.HandleFunc("DELETE /api/points/{id}", a.signedIn(a.deletePoint))
	mux.HandleFunc("POST /api/points", a.signedIn(a.addPoints))
	mux.HandleFunc("POST /api/import", a.signedIn(a.importTrack))
	mux.HandleFunc("GET /api/export", a.signedIn(a.exportGPX))
	mux.HandleFunc("GET /api/immich", a.signedIn(a.immichStatus))
	mux.HandleFunc("PUT /api/immich", a.signedIn(a.saveImmich))
	mux.HandleFunc("DELETE /api/immich", a.signedIn(a.deleteImmich))
	mux.HandleFunc("GET /api/photos", a.signedIn(a.photos))
	mux.HandleFunc("GET /api/photos/{id}/thumbnail", a.signedIn(a.thumbnail))
	mux.HandleFunc("GET /", serveWeb)
	return gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		if strings.HasPrefix(a.origin, "https://") {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	}))
}

var gzipPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

func isCompressibleType(contentType string) bool {
	media := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	return strings.HasPrefix(media, "text/") ||
		media == "application/json" ||
		media == "application/gpx+xml" ||
		media == "application/javascript" ||
		media == "application/manifest+json" ||
		media == "image/svg+xml"
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer      *gzip.Writer
	wroteHeader bool
	compress    bool
}

func (g *gzipResponseWriter) WriteHeader(status int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	if status != http.StatusNoContent && status != http.StatusNotModified &&
		g.Header().Get("Content-Encoding") == "" && isCompressibleType(g.Header().Get("Content-Type")) {
		g.compress = true
		g.Header().Del("Content-Length")
		g.Header().Set("Content-Encoding", "gzip")
		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(g.ResponseWriter)
		g.writer = gz
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.wroteHeader {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(p))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.compress {
		return g.writer.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

func (g *gzipResponseWriter) close() {
	if g.writer != nil {
		_ = g.writer.Close()
		g.writer.Reset(io.Discard)
		gzipPool.Put(g.writer)
		g.writer = nil
	}
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}

func problem(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

func serverError(w http.ResponseWriter, err error) {
	log.Printf("request failed: %v", err)
	problem(w, http.StatusInternalServerError, "The request could not be completed")
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(value); err != nil || decoder.Decode(new(any)) != io.EOF {
		problem(w, http.StatusBadRequest, "Invalid JSON request")
		return false
	}
	return true
}

func newToken() string {
	return base64.RawURLEncoding.EncodeToString(randBytes(32))
}

func randBytes(size int) []byte {
	buf := make([]byte, size)
	_, _ = rand.Read(buf)
	return buf
}

func tokenHash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}
