package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	qpg "github.com/kunalpednekar/dumpster/internal/queue/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	"github.com/kunalpednekar/dumpster/internal/server"
)

func main() {
	cfg := config.Load()

	pool, err := db.Connect(context.Background(), cfg)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	txRunner := rls.New(pool)

	obj := s3store.New(s3store.Config{
		Endpoint:     cfg.S3Endpoint,
		Region:       cfg.S3Region,
		Bucket:       cfg.S3Bucket,
		AccessKey:    cfg.S3AccessKey,
		SecretKey:    cfg.S3SecretKey,
		UsePathStyle: cfg.S3UsePathStyle,
	})

	deps := server.Deps{
		KBs:       kbpg.New(txRunner),
		Docs:      docpg.New(txRunner),
		Objects:   obj,
		Publisher: qpg.New(pool),
		JWTSecret: cfg.JWTSecret,
	}

	router := server.NewRouter(deps)

	addr := fmt.Sprintf(":%s", cfg.HTTPPort)
	log.Printf("starting api on %s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
