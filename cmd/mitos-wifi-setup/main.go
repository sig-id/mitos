// Standalone static sender: no Studio, token, password or transcription API.
package main

import (
	"flag"
	"log"
	"mitos/internal/wifisetup"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8747", "Static sender address")
	cert := flag.String("cert", "", "TLS certificate PEM")
	key := flag.String("key", "", "TLS private key PEM")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("/wifi-setup/", wifisetup.ServeHTTP)
	server := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096}
	log.Printf("Wi-Fi sender at %s/wifi-setup/", *addr)
	if *cert != "" || *key != "" {
		log.Fatal(server.ListenAndServeTLS(*cert, *key))
	} else {
		log.Fatal(server.ListenAndServe())
	}
}
