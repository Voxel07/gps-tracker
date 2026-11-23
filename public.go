package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:public
var publicFiles embed.FS

var dist, _ = fs.Sub(publicFiles, "public/dist")
var static = http.FileServerFS(dist)

func serveWeb(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		if _, err := fs.Stat(dist, "index.html"); err != nil {
			http.Error(w, "Build the frontend with bun run build in web/.", http.StatusServiceUnavailable)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		// private: assets sit behind sign-in, so shared proxies must not cache them.
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	static.ServeHTTP(w, r)
}
