// Vercel serverless giriş noktası. Tüm istekler (vercel.json rewrite ile)
// buraya yönlenir ve ortak panel router'ına devredilir.
package handler

import (
	"net/http"

	"niha-panel/panel"
)

var mux = panel.Mux()

// Handler, Vercel'in çağırdığı fonksiyondur.
func Handler(w http.ResponseWriter, r *http.Request) {
	mux.ServeHTTP(w, r)
}
