package core

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zoumo/mass/superagent/web"
)

const webCSP = "default-src 'none'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"

func registerWeb(router *chi.Mux) {
	files := web.Assets()
	if files == nil {
		return
	}
	// Match the Web perspectives explicitly so API, probe and retired paths
	// cannot fall through to the SPA.
	for _, page := range []string{"/", "/topic-conversation", "/message", "/router", "/topic", "/issue", "/trace", "/metrics", "/watchdog"} {
		router.HandleFunc(page, func(w http.ResponseWriter, r *http.Request) {
			serveWebFile(w, r, files, "dist/index.html")
		})
	}
	router.HandleFunc("/assets/*", func(w http.ResponseWriter, r *http.Request) {
		serveWebFile(w, r, files, "dist"+r.URL.Path)
	})
}

func serveWebFile(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	if !isReadMethod(r.Method) {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Security-Policy", webCSP)
	info, err := fs.Stat(files, name)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, files, name)
}
