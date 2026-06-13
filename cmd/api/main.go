package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/server"
)

func main() {
	cfg := config.Load()
	router := server.NewRouter()

	addr := fmt.Sprintf(":%s", cfg.HTTPPort)
	log.Printf("starting api on %s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
