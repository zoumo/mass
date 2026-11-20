//go:build !observerweb

package core

import "net/http"

func serveObserver(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "Observer assets are unbuilt; run make build-superagent", http.StatusServiceUnavailable)
}
