//go:build !observerweb

package core

import "net/http"

func serveObserver(w http.ResponseWriter, _ *http.Request, _ bool) {
	http.Error(w, "Observer assets are unbuilt; run make build-superagent", http.StatusServiceUnavailable)
}
