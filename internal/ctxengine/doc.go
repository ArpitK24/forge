// Package ctxengine is the home of Forge's context engine: dynamic,
// ranked, budgeted retrieval rebuilt on every turn of the agent loop.
//
// The subpackages are:
//
//   - symbols:  enclosing-symbol extraction (Go via go/ast, Python via an
//     indentation scanner) and identifier tokenization.
//   - tokens:   the deterministic token estimator used for budgets.
//   - index:    the persistent SQLite/FTS5 index (Milestone 1).
//   - retrieve: the Retriever interface, the B0/B1/B2 baselines and the
//     Milestone 1 retriever m1.
//   - engine:   the per-session query.ContextProvider (opt-in).
//   - eval:     task schema, gold extraction, dataset loaders, metrics
//     and the `forge eval` harness.
//
// Dependency rule: cli -> query -> ctxengine -> {api, core}. Nothing in
// ctxengine may import internal/tools; tool activity reaches the engine
// as plain data.
package ctxengine
