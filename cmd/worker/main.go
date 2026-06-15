package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	qpg "github.com/kunalpednekar/dumpster/internal/queue/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	"github.com/kunalpednekar/dumpster/internal/worker"
)

func main() {
	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	q := qpg.New(pool)
	docs := docpg.New(rls.New(pool))
	h := worker.NewDocumentHandler(docs)

	w := worker.New(q, h, worker.Config{})
	log.Printf("worker: starting")
	if err := w.Run(ctx); err != nil {
		log.Printf("worker: stopped: %v", err)
	}
}
