package main

import (
	"log"

	"github.com/kunalpednekar/dumpster/internal/config"
)

func main() {
	cfg := config.Load()
	log.Printf("worker starting (db=%s:%s/%s)", cfg.DBHost, cfg.DBPort, cfg.DBName)
	// background job runner — populated in later milestones
	select {}
}
