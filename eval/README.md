# Context-engine evaluation (Milestones 0–1)

This directory holds the data for `forge eval`, the hidden harness that
measures Forge's context engine (design doc §13–14). Milestone 0 is
**retrieval only**: deterministic, no model calls, no network in tests.
Every later milestone has to beat the numbers committed here.

```
eval/
  README.md                 this file
  tasks/internal.jsonl      34 retrieval tasks mined from Forge's own history (committed)
  splits/verified-dev.txt   the 100 SWE-bench Verified dev-split ids (committed)
  baselines/internal.json   B0/B1/B2/M1 results on the internal tasks (committed, checked in CI)
  baselines/verified-dev.json  B0/B1/B2/M1 results on the Verified dev split (committed)
```

The code lives in `internal/ctxengine/` (`symbols`, `tokens`, `index`,
`retrieve`, `engine`, `eval`). Its only third-party dependency is the
pure-Go SQLite driver `modernc.org/sqlite` (used by `index`), and it never
imports `internal/tools`.

## Quick start

```sh
go build -o forge ./cmd/forge

# Internal Forge tasks against this checkout (what CI runs).
# Some base commits live on squash-merged PR branches, so fetch PR heads once:
git fetch origin '+refs/pull/*/head:refs/remotes/origin/pr/*'
./forge eval retrieval --dataset internal --repo ArpitK24/forge=. --no-fetch --strict \
  --out results.json

# SWE-bench: download the datasets once (Hugging Face datasets-server, ~10 s)
./forge eval fetch-swebench --dataset all
# then score the 100-task Verified dev split (clones ~10 repos into the cache on first use)
./forge eval retrieval --dataset verified-dev --baseline b0,b1,b2,m1 --budget 8192 --out vdev.json
```

`forge eval retrieval` flags:

| flag | default | meaning |
|---|---|---|
| `--dataset` | `internal` | `internal`, `lite` (300), `verified` (500), `verified-dev` (100) |
| `--tasks` | | read tasks from any JSONL file instead |
| `--baseline` | `b0,b1,b2,m1` | `b1` expands to `b1-1k,b1-4k` |
| `--budget` | `8192` | pack token budget |
| `--out` | stdout | JSON report |
| `--cache-dir` | `<user cache>/forge/eval` | datasets and bare repo clones |
| `--repo owner/name=path` | | use a local clone instead of the cache (repeatable) |
| `--no-fetch` | off | never clone or fetch |
| `--strict` | off | non-zero exit if any task is skipped |
| `--jobs` | min(CPUs, 4) | parallel tasks (does not affect output) |
| `--bootstrap`, `--seed` | `2000`, `20261108` | paired bootstrap resamples and seed |
| `--include-tests` | off | count test files as gold (internal tasks) |
| `--limit`, `--quiet` | | first N tasks by id; no progress lines |

Other subcommands: `fetch-swebench`, `split` (regenerates the dev id
list), `mine` (regenerates `tasks/internal.jsonl`).

## Datasets and splits

| dataset | tasks | split | source |
|---|---|---|---|
| SWE-bench Verified | 500 | 100 `dev` / 400 `heldout` | `princeton-nlp/SWE-bench_Verified` |
| SWE-bench Lite | 300 | `dev` if the id is in the Verified dev list, else `heldout` | `princeton-nlp/SWE-bench_Lite` |
| Forge internal | 34 | `internal` | this repo's git history |

**SWE-bench** rows come from `https://datasets-server.huggingface.co/rows`
(`config=default`, `split=test`, 100 rows per page) and are cached as
JSONL under `<cache>/datasets/`. They are never committed. We keep
`instance_id, repo, base_commit, patch, problem_statement, created_at,
version`, and drop `hints_text`, `FAIL_TO_PASS` and `PASS_TO_PASS`.

**Dev split.** 100 Verified tasks stratified by repo: each repo gets a
largest-remainder quota proportional to its size (ties by repo name), and
its ids are shuffled by a SplitMix64 stream (seed `20261108`). Repos with
fewer than ~1% of tasks (flask, seaborn) round to zero. The id list is
committed, so the split never moves even if the dataset is re-uploaded;
`forge eval split` regenerates it. Everything else in Verified is held
out for milestone gates; do not tune on it.

**Internal tasks** are mined by `forge eval mine` from every commit on
`main` and on PR heads (`refs/pull/*/head`) up to PR #17; later PRs
(including the eval harness itself) are not mined, so the set stays
fixed. To regenerate it:

```sh
git fetch --no-tags origin '+refs/pull/*/head:refs/remotes/origin/pr/*'
forge eval mine --revs "origin/main $(printf 'refs/remotes/origin/pr/%d ' $(seq 1 17))"
```

A commit qualifies if it has
one parent, modifies at least one existing non-test `.go` file, touches a
`_test.go` file, has at most 10 gold files and creates at most 10 new Go
files. Duplicates are removed by `git patch-id --stable` and by identical
subject line, and a `(#N)` squash merge is dropped when PR #N's own
commits qualify (the originals are smaller, sharper tasks). That yields
34 tasks rather than the ~50 the design doc hoped for: of the 92
non-merge commits reachable from `main` and PR heads, only 47 change both
Go code and Go tests, and the size caps and de-duplication remove 13 of
those (repo-wide gofmt/lint sweeps, the initial imports, and squash
merges that repeat their PR's commits).

* `gold_files`: modified or deleted non-test **Go** files. CHANGELOG,
  README, CI and manifest edits ride along with fixes but are not context
  a fix needs.
* `gold_patch`: the diff restricted to the gold files. The full change is
  `git diff base_commit fix_commit`.
* `problem_statement`: the commit message with the conventional-commit
  prefix (`fix(tui):`), PR numbers and trailers removed, and every literal
  mention of a touched file (full path, base name, or a multi-segment
  directory such as `internal/plugins`) replaced by `<file>`. Function and type names are kept, as an issue reporter would
  name them. This makes the internal set **easier** than SWE-bench
  (commit messages are written by the fixer); compare baselines on it,
  not absolute numbers against SWE-bench.

## Task schema (JSONL, one object per line)

| field | meaning |
|---|---|
| `id` | SWE-bench `instance_id`, or `forge-<sha12>` |
| `repo` | `owner/name` on GitHub |
| `base_commit` | commit the retriever sees |
| `problem_statement` | the query |
| `gold_patch` | unified diff of the fix |
| `gold_files` | base-side files the fix changes (tests excluded, see below) |
| `gold_functions` | `path::Symbol` ids enclosing the changed hunks at `base_commit`; resolved on the fly when absent |
| `split` | `dev`, `heldout` or `internal` |
| `source` | `swe-bench-lite`, `swe-bench-verified`, `forge-git` |
| `added_files`, `fix_commit`, `created_at` | optional |

## Gold extraction

`ParseUnifiedDiff` consumes hunks by their line counts and records, per
file, the base-side lines removed and the base-side positions where lines
were inserted. Then:

* **Gold files** are the base-side paths. Files a patch *creates* are
  listed in `added_files` and not scored: no retriever can find them at
  the base commit. Test files are dropped for mined tasks (`_test.go`,
  `test_*.py`, `*_test.py`, `conftest.py`, `tests/`, `testdata/`);
  `--include-tests` keeps them. SWE-bench needs no filter because its
  `patch` already excludes the `test_patch`.
* **Gold functions** map every removed line, and every insertion that
  falls strictly inside a symbol, to the innermost enclosing symbol at the
  base commit. Go uses `go/ast` (functions, `Recv.Method`, types). Python
  uses a cgo-free scanner that tracks triple-quoted strings, brackets and
  backslash continuations, closes blocks on dedent, includes decorators,
  qualifies methods as `Class.method`, and folds functions nested in
  functions into their parent. Module-level edits (imports, constants)
  and code appended after a function's last line have no symbol. Tasks
  without gold functions are excluded from function recall (`n` in the
  report says how many count).

## Baselines

| name | what it is |
|---|---|
| `b0` | No engine: today's Forge. Empty ranking and pack, so every retrieval metric is 0 by definition. It is recorded so later milestones pair against it. |
| `b1-1k`, `b1-4k` | Aider-style repo map in pure Go. Definition tags from `symbols`, reference tags = identifier occurrences; an edge R→D for each identifier D defines and R references, weighted `mul·√refs` (×10 mentioned in the issue, ×10 long compound names, ×0.1 `_private`, ×0.1 defined in >5 files). PageRank (d=0.85) personalized toward files whose path, base name or stem the issue mentions, or that define a mentioned identifier. Files are ranked by PageRank; definitions by the rank their in-edges carry. The map lists top definitions' first lines under their file headers up to 1k or 4k tokens. Go and Python files only. |
| `b2` | Full-file grep. BM25 (k1=1.2, b=0.75) over query terms; documents are path terms + content terms; identifiers are split on snake_case and camelCase (acronyms kept, compound kept as an extra term), stopwords dropped. Symbols are ranked by a second BM25 over symbol bodies. Whole files are packed greedily in rank order, skipping any that would overflow the budget. |

| `m1` | Milestone 1 retriever (`retrieve/m1.go`), the ranker behind `--context-engine`. The corpus is loaded into an in-memory copy of the real index (`index` package: SQLite/FTS5, the same code that maintains `.forge/index/index.db`). Chunks are symbols (spans from `symbols`, so ids match the gold sets; Go signatures from `go/parser`), each code file's top-level "header", and 60-line windows of files without an extractor. FTS5 `bm25()` over three columns (path ×1, symbol name ×2, body ×1) of identifier-split terms; files score a decayed sum of their 3 best chunks (1, 0.5, 0.25); files the session read or edited, Grep paths/patterns and `file:line` references in tool output add an activity boost (decay 0.7 per turn; recorded trajectories are not in the eval yet, so it is 0 here). The pack takes chunk bodies in score order from the top 10 files while they fit and score at least 0.6 of the best chunk (a body over 25% of the budget is shown as its signature), then up to 5 signature lines per file for further matches. `gold_in_pack` counts a file only when at least one body from it is packed; signature-only files do not count. |

All baselines index the same corpus: files with a source, build or prose
extension, ≤1 MiB, not binary, outside `vendor/` and `node_modules/`.
Snapshots are read straight from a bare clone (`git ls-tree` +
`git cat-file --batch`) rather than a worktree per task; it is the same
tree, costs no disk per task and cannot get dirty.

**Tokens** are estimated as `ceil(bytes/4)` (`tokens.Name = "bytes/4"`).
Pure-Go cl100k/o200k ports either download merge tables at runtime or
vendor megabytes of tables; budgets only need a stable, monotone
estimate. Calibration against real usage is an M3 item.

## Metrics

Per task and baseline (`baselines[].tasks[]`), and aggregated with 95%
percentile bootstrap CIs (`baselines[].aggregate`):

* `file_recall@{1,3,5,10}`: |gold files in the top k| / |gold files|.
* `func_recall@{5,10}`: the same over gold symbol ids and the ranked symbol list.
* `mrr`: 1 / rank of the first gold file, 0 if none.
* `gold_in_pack`: |gold files present in the pack| / |gold files|. For B2
  that is the file's full content; for B1 it means the file appears in the
  map (signatures only), so B1's number is not comparable to B2's.
* `pack_tokens`: estimated tokens of the pack.

`paired[]` holds B − A for every baseline pair and metric, resampling the
same task indices for both sides (paired bootstrap, SplitMix64 with the
report's seed, so the stream never changes with a Go upgrade).

**Determinism.** Reports have no timestamps, durations or hostnames.
Inputs are sorted, BM25 sums visit terms in sorted order, scores are
quantized to 1e-9 before ranking (so fused multiply-add on arm64 cannot
reorder ties), ties break on path or symbol id, and reported numbers are
rounded to 6 decimals. Tests run the harness three times (different job
counts and input order) and require identical bytes; CI runs the internal
set twice and fails on any difference.

## Baseline results (budget 8192, bytes/4 tokens)

See `baselines/*.json` for CIs and per-task rankings; the PR that added
this directory carries the B0/B1/B2 tables.

## Milestone 1 results

Exit criteria (design doc §14): file recall@5 at or above B2 with 30%
fewer pack tokens; single-file reindex under 50 ms p95; cold index of the
Forge repo under 5 s.

verified-dev (100 tasks; func recall over the 95 with gold functions):

| | file R@1 | R@3 | R@5 | R@10 | func R@5 | func R@10 | MRR | gold-in-pack | pack tokens |
|---|---|---|---|---|---|---|---|---|---|
| b1-4k | 0.030 | 0.085 | 0.150 | 0.242 | 0.011 | 0.011 | 0.107 | 0.685† | 4090 |
| b2 | 0.145 | 0.318 | 0.373 | 0.506 | 0.308 | 0.359 | 0.292 | 0.155 | 8188 |
| **m1** | **0.240** | **0.408** | **0.431** | **0.576** | **0.342** | **0.428** | **0.385** | **0.483** | **5118** |
| m1 − b2 (95% paired CI) | +0.095 [0.020, 0.175] | +0.090 [0.020, 0.160] | +0.058 [−0.018, 0.135] | +0.070 [0.005, 0.135] | +0.034 [−0.019, 0.089] | +0.069 [0.018, 0.128] | +0.093 [0.038, 0.155] | +0.328 [0.235, 0.422] | −3070 [−3665, −2480] |

internal (34 tasks):

| | file R@1 | R@3 | R@5 | R@10 | func R@5 | func R@10 | MRR | gold-in-pack | pack tokens |
|---|---|---|---|---|---|---|---|---|---|
| b1-4k | 0.112 | 0.204 | 0.391 | 0.624 | 0.203 | 0.225 | 0.372 | 0.936† | 4060 |
| b2 | 0.293 | 0.594 | 0.681 | 0.846 | 0.435 | 0.552 | 0.634 | 0.563 | 8156 |
| **m1** | 0.231 | **0.636** | **0.715** | 0.838 | **0.438** | **0.575** | 0.630 | **0.736** | **4096** |
| m1 − b2 (95% paired CI) | −0.062 [−0.154, 0.011] | +0.042 [−0.056, 0.136] | +0.034 [−0.005, 0.083] | −0.009 [−0.048, 0.028] | +0.003 [−0.018, 0.023] | +0.024 [−0.017, 0.069] | −0.004 [−0.074, 0.065] | +0.173 [0.045, 0.307] | −4060 [−4795, −3257] |

† B1's map lists signatures only, so its gold-in-pack is not comparable.

* **Recall@5 vs B2:** met on both sets (0.431 vs 0.373 on dev, 0.715 vs
  0.681 internal). The paired CIs for R@5 include 0, so "at or above" is
  shown, "significantly above" is not; R@1, R@3, R@10, MRR and func R@10
  are significantly better on dev.
* **30% fewer pack tokens:** met: 5118 vs 8188 on dev (−37.5%), 4096 vs
  8156 internal (−49.8%), while gold-in-pack roughly triples on dev.
* **Index speed** (`go test -run TestIndexPerf -v ./internal/ctxengine/index`,
  set `FORGE_INDEX_PERF=1` to enforce): met. On a 2-vCPU arm64 sandbox,
  cold index of this repo (211 files, 2,341 chunks) takes 0.28–0.31 s
  (target 5 s), and single-file reindex over 200 edits of the 40 largest
  Go files is p50 ≈3 ms, p95 10.5–15.6 ms (target 50 ms) across three
  runs. The reindex timing covers hash, parse and the SQLite transaction
  (the file is read from memory, so disk stat/read is excluded). There is
  no fsnotify watcher yet: the engine syncs once per session and then
  reindexes files the agent edits (`Refresh`) before the next turn; edits
  made outside Forge mid-session are picked up at the next session.

**What was tuned, and on what.** Ranking parameters (column weights,
file aggregation) were set before looking at results and kept after a
small sweep on the internal set showed only noise-level differences.
The one parameter chosen with the dev split in view is the packer's
score cutoff: 0.3 → 0.5 → 0.6 of the best chunk gave 7875 → 6505 → 5118
pack tokens on dev with gold-in-pack 0.513 → 0.493 → 0.483; 0.6 is the
committed value. Recall metrics do not depend on it. Nothing was tuned
on held-out Verified tasks.

**Not yet measured:** the activity signal (no recorded trajectories in
the eval sets; it is unit-tested only) and end-to-end solve rate.

## Leakage notes

* Hidden tests: SWE-bench's FAIL_TO_PASS/PASS_TO_PASS lists and test patch
  are never shown to a retriever, and `hints_text` is dropped. A future
  "repro-only" mode (M4) may run a reproduction the agent writes itself,
  never the hidden tests.
* Internal tasks leak more than SWE-bench (commit messages are written by
  the person who made the fix and often name functions). File names and package directories are
  masked; treat internal numbers as relative.
* Contamination: SWE-bench repos and fixes are public and likely in model
  training data. That matters for end-to-end solve rate, not for these
  retrieval baselines, which have no learned component.
* Never tune ranking weights on `heldout` tasks. Use `dev` and `internal`.

## End-to-end solve rate (not in M0)

TODO (needs Docker capacity and a model budget decision): run the official
SWE-bench docker harness on the 100 dev tasks with `forge -p` in headless
mode, B0 first, 3 runs per configuration, two fixed models (one cheap, one
strong), recording solve rate, $ cost (`core.CostTracker`), turns,
latency and cache-read share; McNemar for solve rate, paired bootstrap for
the rest. Shape: `forge eval e2e --dataset verified-dev --model ... --runs 3`
writing predictions JSONL for `swebench.harness.run_evaluation`.
