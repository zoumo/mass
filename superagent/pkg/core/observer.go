//go:build observerweb

package core

import (
	"crypto/rand"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed ui/*
var observerFiles embed.FS

func serveObserver(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/observer/")
	if path == "" || path == r.URL.Path || !strings.Contains(path, ".") {
		path = "index.html"
	}
	if strings.Contains(path, "..") {
		http.NotFound(w, r)
		return
	}
	root, _ := fs.Sub(observerFiles, "ui")
	if path == "index.html" {
		b, err := fs.ReadFile(root, path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		nonce := observerNonce()
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'nonce-"+nonce+"'; style-src-attr 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(strings.ReplaceAll(string(b), "__CSP_NONCE__", nonce)))
		return
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Path = "/" + path
	clone.URL = &u
	http.FileServer(http.FS(root)).ServeHTTP(w, clone)
}

func observerNonce() string {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawStdEncoding.EncodeToString(b[:])
}
