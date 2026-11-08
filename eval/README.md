# Context-engine evaluation (Milestone 0)

This directory holds the data for `forge eval`, the hidden harness that
measures Forge's context engine (design doc §13–14). Milestone 0 is
**retrieval only**: deterministic, no model calls, no network in tests.
Every later milestone has to beat the numbers committed here.

```
eval/
  README.md                 this file
  tasks/internal.jsonl      34 retrieval tasks mined from Forge's own history (committed)
  splits/verified-dev.txt   the 100 SWE-bench Verified dev-split ids (committed)
  baselines/internal.json   B0/B1/B2 results on the internal tasks (committed, checked in CI)
  baselines/verified-dev.json  B0/B1/B2 results on the Verified dev split (committed)
```

The code lives in `internal/ctxengine/` (`symbols`, `tokens`, `retrieve`,
`eval`). It imports only the standard library and never imports
`internal/tools`.

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
./forge eval retrieval --dataset verified-dev --baseline b0,b1,b2 --budget 8192 --out vdev.json
```

`forge eval retrieval` flags:

| flag | default | meaning |
|---|---|---|
| `--dataset` | `internal` | `internal`, `lite` (300), `verified` (500), `verified-dev` (100) |
| `--tasks` | | read tasks from any JSONL file instead |
| `--baseline` | `b0,b1,b2` | `b1` expands to `b1-1k,b1-4k` |
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
this directory carries the tables.

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
