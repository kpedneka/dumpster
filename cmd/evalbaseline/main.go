// cmd/evalbaseline ingests the validation corpus (validation/) into a fresh
// benchmark knowledge base, using the same document-creation and
// job-publishing steps a real upload goes through -- see
// internal/evalcorpus's package doc.
//
// It deliberately does not touch entity extraction, embedding, or AWS
// Batch: run this once to publish the corpus onto the queue, then run your
// normal worker process (with its real AWS Batch credentials already
// configured) until every document reaches "indexed" status, exactly as
// for a real user upload. Only after that has the graphrecall/intrusion
// scoring step got anything real to measure.
//
// There is no separate "account" system in this app -- every domain
// row's tenant is a session ID (internal/session), and sessions are
// ephemeral by design (6h idle timeout, 24h hard cap regardless of
// activity -- see internal/account.Sweep). This tool accepts that: it
// mints a brand new session for every run rather than reusing one, so a
// benchmark KB is naturally disposable. If a run's session expires
// before you finish scoring, just run this again -- re-ingesting the
// corpus is cheap enough that a real persistence mechanism isn't worth
// the added code and maintenance for what this tool is for.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kunalpednekar/dumpster/internal/auth"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	"github.com/kunalpednekar/dumpster/internal/evalcorpus"
	kbpg "github.com/kunalpednekar/dumpster/internal/kb/pgstore"
	"github.com/kunalpednekar/dumpster/internal/objectstore/s3store"
	qpg "github.com/kunalpednekar/dumpster/internal/queue/pgstore"
	"github.com/kunalpednekar/dumpster/internal/rls"
	sessionpg "github.com/kunalpednekar/dumpster/internal/session/pgstore"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
)

func main() {
	kbName := flag.String("kb-name", "validation-benchmark", "name for the benchmark knowledge base")
	corpusDir := flag.String("corpus-dir", "validation", "path to the validation corpus directory")
	flag.Parse()

	logger := telemetry.NewLogger(os.Stdout, "evalbaseline")

	ctx := context.Background()
	cfg := config.Load()

	pool, err := db.ConnectDirect(ctx, cfg)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	sess, err := sessionpg.New(pool).Create(ctx)
	if err != nil {
		logger.Error("create session failed", "err", err)
		os.Exit(1)
	}
	userID := sess.ID
	logger.Info("minted a fresh session for this benchmark run", "session_id", userID)

	// Every RLS-protected query reads the tenant identity from the
	// context, not from a repository method's explicit userID parameter --
	// same reason cmd/evalscore wraps its context this way.
	ctx = auth.WithUserID(ctx, userID)

	txRunner := rls.New(pool)
	kbs := kbpg.New(txRunner)
	docs := docpg.New(txRunner)
	q := qpg.New(pool)
	obj, err := s3store.New(ctx, s3store.Config{
		Endpoint:     cfg.S3Endpoint,
		Region:       cfg.S3Region,
		Bucket:       cfg.S3Bucket,
		AccessKey:    cfg.S3AccessKey,
		SecretKey:    cfg.S3SecretKey,
		UsePathStyle: cfg.S3UsePathStyle,
	})
	if err != nil {
		logger.Error("object store setup failed", "err", err)
		os.Exit(1)
	}

	files, err := loadCorpusFiles(*corpusDir)
	if err != nil {
		logger.Error("loading corpus files failed", "err", err)
		os.Exit(1)
	}
	logger.Info("loaded corpus files", "count", len(files))

	kb, err := kbs.Create(ctx, userID, *kbName)
	if err != nil {
		logger.Error("create kb failed", "err", err)
		os.Exit(1)
	}
	logger.Info("created benchmark kb", "kb_id", kb.ID, "name", kb.Name)

	ing := &evalcorpus.Ingester{Objects: obj, Documents: docs, Publisher: q}
	results, err := ing.IngestAll(ctx, userID, kb.ID, files)
	if err != nil {
		logger.Error("ingest failed partway through", "err", err, "kb_id", kb.ID)
		os.Exit(1)
	}

	mapping := make(map[string]string, len(results))
	for relPath, doc := range results {
		mapping[relPath] = doc.ID.String()
	}
	out, err := json.MarshalIndent(map[string]any{
		"session_id": userID.String(),
		"kb_id":      kb.ID.String(),
		"documents":  mapping,
	}, "", "  ")
	if err != nil {
		logger.Error("marshal result failed", "err", err)
		os.Exit(1)
	}
	fmt.Println(string(out))

	logger.Info("ingest complete -- documents are queued, not yet processed",
		"session_id", userID,
		"kb_id", kb.ID,
		"next_step", "run your worker until every document's status is 'indexed', then run cmd/evalscore with this same session_id and kb_id",
		"expires", "this session is swept after 6h idle or 24h total, whichever comes first -- finish scoring within that window or just re-run this tool",
	)
}

// loadCorpusFiles reads every document path listed in
// <corpusDir>/ground_truth.json's topics[].documents, so the file list this
// tool ingests can never drift from the ground truth the scoring step
// reads later.
func loadCorpusFiles(corpusDir string) (map[string][]byte, error) {
	gtPath := filepath.Join(corpusDir, "ground_truth.json")
	raw, err := os.ReadFile(gtPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", gtPath, err)
	}

	var gt struct {
		Topics []struct {
			Documents []string `json:"documents"`
		} `json:"topics"`
	}
	if err := json.Unmarshal(raw, &gt); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", gtPath, err)
	}

	files := make(map[string][]byte)
	for _, topic := range gt.Topics {
		for _, relPath := range topic.Documents {
			content, err := os.ReadFile(filepath.Join(corpusDir, relPath))
			if err != nil {
				return nil, fmt.Errorf("reading corpus file %s: %w", relPath, err)
			}
			files[relPath] = content
		}
	}
	return files, nil
}
