// Command lambda-stateless-jobs is the Lambda entrypoint for the two
// queue.JobType stages that are pure local Go computation with no AWS
// Batch hop: edge extraction and canonicalization. It wires
// internal/worker's EdgeHandler and CanonicalizationHandler and dispatches
// SQS events to them via internal/worker/lambda.Dispatcher — see that
// package's doc for the delivery and retry rules. Deliberately thin: no
// logic of its own beyond wiring. Deployed by terraform/modules/pipeline
// (stateless.tf).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/kunalpednekar/dumpster/internal/canonical"
	canonicalpg "github.com/kunalpednekar/dumpster/internal/canonical/pgstore"
	"github.com/kunalpednekar/dumpster/internal/config"
	"github.com/kunalpednekar/dumpster/internal/crosslink"
	crosslinkpg "github.com/kunalpednekar/dumpster/internal/crosslink/pgstore"
	"github.com/kunalpednekar/dumpster/internal/db"
	docpg "github.com/kunalpednekar/dumpster/internal/document/pgstore"
	entitypg "github.com/kunalpednekar/dumpster/internal/entity/pgstore"
	graphedgepg "github.com/kunalpednekar/dumpster/internal/graphedge/pgstore"
	statuspg "github.com/kunalpednekar/dumpster/internal/jobstatus/pgstore"
	"github.com/kunalpednekar/dumpster/internal/llm/bedrock"
	"github.com/kunalpednekar/dumpster/internal/queue"
	"github.com/kunalpednekar/dumpster/internal/rls"
	"github.com/kunalpednekar/dumpster/internal/telemetry"
	"github.com/kunalpednekar/dumpster/internal/worker"
	worklambda "github.com/kunalpednekar/dumpster/internal/worker/lambda"
)

// queueMaxReceiveCount must match both queues' redrive policy
// (maxReceiveCount in terraform/modules/pipeline/queues.tf), so the dispatcher knows
// which delivery is the last one before a message goes to its DLQ.
const queueMaxReceiveCount = 3

func main() {
	cfg := config.Load()
	logger := telemetry.NewLogger(os.Stdout, "lambda-stateless-jobs")
	ctx := context.Background()

	// Pooled, not direct: short-lived, concurrent Lambda invocations are
	// exactly the connection-pooling case internal/db.Connect's PgBouncer
	// (transaction mode) DSN exists for — the same reasoning cmd/api and
	// the other Lambdas follow.
	pool, err := db.Connect(ctx, cfg)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	txRunner := rls.New(pool)
	docs := docpg.New(txRunner)
	entities := entitypg.New(txRunner)
	edges := graphedgepg.New(txRunner)
	canonicalRepo := canonicalpg.New(txRunner)

	edgeHandler := worker.NewEdgeHandler(docs, entities, edges)

	generator, err := bedrock.New(ctx, cfg.AWSRegion, cfg.BedrockModelID)
	if err != nil {
		logger.Error("bedrock generator setup failed", "err", err)
		os.Exit(1)
	}
	aliasJudge := &canonical.LLMAliasJudge{Generator: generator}
	crosslinkRepo := crosslinkpg.New(txRunner)
	crosslinkExtractor := crosslink.NewExtractor(generator)
	canonicalizationHandler := worker.NewCanonicalizationHandler(docs, entities, canonicalRepo).
		WithAliasJudge(aliasJudge).
		WithCrossLink(crosslinkRepo, crosslinkExtractor, 0)

	dispatcher := worklambda.NewDispatcher().
		RegisterHandler(queue.JobTypeEdgeExtraction, edgeHandler).
		RegisterHandler(queue.JobTypeCanonicalization, canonicalizationHandler).
		WithStatus(statuspg.New(txRunner), queueMaxReceiveCount)

	slog.SetDefault(logger)
	lambda.Start(dispatcher.HandleSQSEvent)
}
