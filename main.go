package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	addr := flag.String("addr", ":3000", "listen address")
	dataDir := flag.String("data", "data", "data directory for the SQLite database")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("cannot create data dir: %v", err)
	}
	db, err := openDB(filepath.Join(*dataDir, "roost.db"))
	if err != nil {
		log.Fatalf("cannot open database: %v", err)
	}
	defer db.Close()

	hub := newHub()
	s, err := newServer(db, hub)
	if err != nil {
		log.Fatalf("cannot create server: %v", err)
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	log.Printf("roost listening on %s", *addr)
	if err := http.ListenAndServe(*addr, gate(mux)); err != nil {
		log.Fatal(err)
	}
}

func gate(next http.Handler) http.Handler {
	return rateLimit(sameSiteCheck(next))
}
