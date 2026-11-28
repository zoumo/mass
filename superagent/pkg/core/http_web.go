package core

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zoumo/mass/superagent/web"
)

const (
	consolePath = "/console/"
	webCSP      = "default-src 'none'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"
)

func registerWeb(router *chi.Mux) {
	files := web.Assets()
	if files == nil {
		return
	}
	serve := func(w http.ResponseWriter, r *http.Request) {
		if !isReadMethod(r.Method) {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/console" {
			target := consolePath
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusPermanentRedirect)
			return
		}

		w.Header().Set("Content-Security-Policy", webCSP)
		name := strings.TrimPrefix(r.URL.Path, consolePath)
		page := strings.TrimSuffix(name, "/")
		switch {
		case name == "assets" || strings.HasPrefix(name, "assets/"):
			serveWebFile(w, r, files, "dist/"+name)
		case (page == "" || fs.ValidPath(page)) && path.Ext(page) == "":
			// React resolves the page path and query from the browser URL.
			serveWebFile(w, r, files, "dist/index.html")
		default:
			http.NotFound(w, r)
		}
	}
	router.HandleFunc("/", serve)
	router.HandleFunc("/console", serve)
	router.HandleFunc(consolePath+"*", serve)
}

func serveWebFile(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	info, err := fs.Stat(files, name)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, files, name)
}
