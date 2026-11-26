// Command orders_acceptance-evals runs the evaluator-owned orders acceptance
// suite against built candidate binaries, following Highbay's suite command
// contract (see github.com/goadesign/highbay/verify/suite): --describe prints
// the declaration, and --artifact orders=path --artifact orders-worker=path
// runs the scenarios against those binaries and prints the eval.Report.
// --live --artifact orders=path --artifact orders-worker=path starts them as
// the live copy Highbay's start_live_target runs, with the evaluator's scripted
// model, so Highbay's draft_override can change the refund agent's prompts.
package main

import (
	"context"

	"goa.design/goa-ai/eval"

	"github.com/goadesign/highbay/examples/orders-eval/acceptance"
	genevals "github.com/goadesign/highbay/examples/orders-eval/gen/evals/orders_acceptance"
	"github.com/goadesign/highbay/examples/orders-eval/harness"
	"github.com/goadesign/highbay/verify/candidate"
	"github.com/goadesign/highbay/verify/suite"
)

func main() {
	suite.Main(suite.Command{
		Environment:    harness.Environment,
		Binaries:       []string{"orders", "orders-worker"},
		MaxConcurrency: 4,
		Declare:        declare,
		Suite:          newSuite,
		Live:           live,
	})
}

// declare returns the acceptance suite's declaration for --describe; its hooks
// are never run.
func declare() (eval.Suite, error) {
	return genevals.New(&acceptance.Hooks{}, genevals.Inputs{})
}

// newSuite returns the acceptance suite running the pinned orders API and
// refund worker.
func newSuite(pinned map[string]candidate.Artifact) (eval.Suite, error) {
	env, err := harness.NewEnv(pinned["orders"], pinned["orders-worker"])
	if err != nil {
		return eval.Suite{}, err
	}
	return genevals.New(acceptance.New(env), genevals.Inputs{})
}

// live starts the pinned orders API and refund worker as a live copy whose
// prompts the holder of key may change (see acceptance.Live).
func live(ctx context.Context, pinned map[string]candidate.Artifact, key string) (suite.LiveTarget, error) {
	env, err := harness.NewEnv(pinned["orders"], pinned["orders-worker"])
	if err != nil {
		return suite.LiveTarget{}, err
	}
	return acceptance.Live(ctx, env, key)
}
