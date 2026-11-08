// Package ctxengine is the home of Forge's context engine: dynamic,
// ranked, budgeted retrieval rebuilt on every turn of the agent loop.
//
// Milestone 0 ships only the measuring stick. The subpackages are:
//
//   - symbols:  enclosing-symbol extraction (Go via go/ast, Python via an
//     indentation scanner) and identifier tokenization.
//   - tokens:   the deterministic token estimator used for budgets.
//   - retrieve: the Retriever interface and the B0/B1/B2 baselines.
//   - eval:     task schema, gold extraction, dataset loaders, metrics
//     and the `forge eval` harness.
//
// Dependency rule: cli -> query -> ctxengine -> {api, core}. Nothing in
// ctxengine may import internal/tools; tool activity reaches the engine
// as plain data.
package ctxengine
