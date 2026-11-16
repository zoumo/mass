package sa

import (
	"crypto/rand"
	"encoding/base64"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// NewObserverHandler exposes only loopback, same-origin GET/HEAD observation.
// authority must come from the actual bound listener, including its actual port.
func NewObserverHandler(service *WorkerService, authority string) (http.Handler, error) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "" {
		return nil, invalidConfig("observer requires an actual loopback listener address")
	}
	assets, assetErr := observerAssets()
	var files http.Handler
	if assetErr == nil {
		files = http.FileServer(http.FS(assets))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		if !observerSameOrigin(r, host, port) {
			http.Error(w, "observer requires a loopback, same-origin request", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "observer is read-only", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/observer" {
			http.Redirect(w, r, "/observer/", http.StatusTemporaryRedirect)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/observer/api/") {
			resource := strings.TrimPrefix(r.URL.Path, "/observer/api/")
			if strings.HasPrefix(resource, "sessions/") || strings.HasPrefix(resource, "runtimes/") {
				resource = strings.TrimPrefix(r.URL.EscapedPath(), "/observer/api/")
			}
			if strings.HasPrefix(resource, "sessions/") && strings.HasSuffix(resource, "/events") {
				service.observer.streamSession(w, r, resource)
				return
			}
			result, err := service.observer.Query(r.Context(), resource, r.URL.Query())
			if err != nil {
				writeManagementError(w, managementStatus(err), err, nil)
				return
			}
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/observer/") {
			http.NotFound(w, r)
			return
		}
		if assetErr != nil {
			http.Error(w, "Observer assets are not built. Run make observer-build in mass-sa, then use that binary.", http.StatusServiceUnavailable)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/observer/")
		if name == "" || name == "." || name == "/observer" {
			name = "index.html"
		}
		if _, err := fs.Stat(assets, name); err != nil {
			// SPA fallback is limited to known page routes, never missing assets/API.
			switch name {
			case "triggers", "routers", "tickets", "resources":
				name = "index.html"
			default:
				http.NotFound(w, r)
				return
			}
		}
		copy := r.Clone(r.Context())
		urlCopy := *r.URL
		copy.URL = &urlCopy
		if name == "index.html" {
			index, err := fs.ReadFile(assets, name)
			if err != nil {
				http.Error(w, "Observer page is unavailable", http.StatusInternalServerError)
				return
			}
			var random [24]byte
			if _, err := rand.Read(random[:]); err != nil {
				http.Error(w, "Observer page is unavailable", http.StatusInternalServerError)
				return
			}
			nonce := base64.RawStdEncoding.EncodeToString(random[:])
			// PrimeVue uses its CSP nonce for trusted component styles. Scripts
			// and style attributes retain the original same-origin restrictions.
			w.Header().Set("Content-Security-Policy", strings.Replace(w.Header().Get("Content-Security-Policy"), "style-src 'self'", "style-src 'self' 'nonce-"+nonce+"'", 1))
			page := strings.Replace(string(index), "<head>", `<head><meta name="observer-style-nonce" content="`+nonce+`">`, 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "index.html", time.Time{}, strings.NewReader(page))
			return
		} else {
			copy.URL.Path = "/" + name
		}
		files.ServeHTTP(w, copy)
	}), nil
}

func observerSameOrigin(r *http.Request, boundHost, boundPort string) bool {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != boundPort {
		return false
	}
	// localhost is an allowed alias; other numeric loopback addresses must equal
	// the listener's address. No DNS resolution or forwarding headers are trusted.
	if host != "localhost" && host != boundHost {
		return false
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(peer) == nil || !net.ParseIP(peer).IsLoopback() {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "http" || parsed.Host != r.Host || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return false
		}
	}
	return true
}
