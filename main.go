// Yerel geliştirme sunucusu. Vercel'de bu dosya kullanılmaz;
// orada api/index.go devreye girer.
package main

import (
	"log"
	"net/http"
	"os"

	"niha-panel/panel"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("Panel çalışıyor:  http://localhost%s", addr)
	if err := http.ListenAndServe(addr, panel.Mux()); err != nil {
		log.Fatal(err)
	}
}
