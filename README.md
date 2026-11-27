# Games API — Boggle + Millionaire + Out of Line + Misclue

One Go binary serving **all games** behind one API. The repo/module name stays
`boggle-api` on purpose: the deployment, CI path and Dockerfile were built
around it, and folding more games in is not a reason to churn them. The
internal packages are what names the games honestly.

## Project structure

```
cmd/api/                       ONE binary — registers all game route trees
cmd/{seed,import,label,backfill}/   one-shot question-corpus tooling (Millionaire)
internal/app/                  everything shared: DB pool, gin engine, /health,
                               CORS, rate limits, origin allowlist
internal/boggle/               Boggle: handler, models, repository, server, service
internal/millionaire/          Millionaire: handler, models, repository, server, service
internal/outofline/            Out of Line: content, handler, models, repository, server, service
internal/misclue/              Misclue: AI turn endpoint (OpenRouter), content, handler, llm, server
internal/codes/                SHARED WORD-WORD-DD room-code generator
internal/dberr/                SHARED unique-violation classification
migrations/                    one sequence, table-prefixed per game
data/                          the WWTBAM + OpenTDB question corpora (import seeds)
```

A game is a **module**: `NewModule(db, origins, limiters)` builds its
dependencies on the shared DB pool, and `RegisterRoutes(engine)` mounts its own
route tree. Neither game dials the database, listens on a port, or owns
`/health` — which is exactly why they can share a process.

## Routes

| Game | REST | WebSocket |
|---|---|---|
| Boggle | `/api/boggle/*` | `/ws/boggle/rooms`, `/ws/boggle/rooms/:code` |
| Millionaire | `/api/millionaire/*` | `/ws/millionaire/rooms`, `/ws/millionaire/rooms/:code` |
| Out of Line | `/api/out-of-line/*` | `/ws/out-of-line/rooms/:code` |
| Misclue | `/api/misclue/ai/turn` | — (stateless AI endpoint) |
| all | `GET /health` | |

**Room codes.** Multiplayer rooms use the same `WORD-WORD-DD` vocabulary
(e.g. `BLUE-TIGER-73`) from `internal/codes`. A code is what you share; the
uuid stays the internal key. WebSocket joins are strictly code-keyed.
Millionaire's REST resolver is code-first with a uuid fallback for existing
links.

## The database

One database hosts all games, namespaced by **table prefix** — `boggle_*`,
`millionaire_*`, and `outofline_*`. This is deliberate: separating by schema or
`search_path` means one unqualified query can silently cross games, while
prefixes make that impossible by construction.

| File | What it does |
|---|---|
| `000_prefix_existing_tables.sql` | Data-preserving rename for a database that already ran a game under the old unprefixed names. Guarded; **raises** rather than guessing when the input is ambiguous. |
| `001_boggle_schema.sql` | Boggle's schema. |
| `002_millionaire_schema.sql` | Millionaire's cumulative schema. |
| `003_boggle_room_codes.sql` | Boggle's `code` column + partial unique index. |
| `004_outofline_schema.sql` | Out of Line's room/player/round/submission schema. |

Fresh database: apply `000` → `001` → `002` → `003` → `004` in order (`000` is a
no-op when there is nothing to rename). Existing database with data: run `000`
once and stop — it renames in place; **do not** then run `001`/`002`, which
DROP and recreate. For an existing database that already has Boggle/Millionaire
tables, apply `004` manually.

The devcontainer mounts `migrations/` at `/docker-entrypoint-initdb.d`, so the
schema files run automatically **on first boot only**. For an existing volume,
apply new files by hand:

```sh
psql "$DATABASE_URL" -f migrations/00X_new_thing.sql
```

## First boot — seeding the question corpus

Millionaire needs questions; Boggle does not. Two corpora are imported, both
once, both idempotent (each importer dedupes against the live corpus and
reports what it added, so re-running imports 0):

```sh
go run ./cmd/import -source wwtbam -file data/wwtbam.jsonl   # 11,450 real show questions
go run ./cmd/import -source opentdb                          #  1,900 rated OpenTDB questions
```

The **WWTBAM** corpus ships in `data/wwtbam.jsonl` (11.8k real show
questions); `strength` is the real show level 1–15.

The **OpenTDB** corpus is what makes the levels 1–3 blend real. The game
flips a coin between `WWTBAM@level` and a "casual" pool that is by
construction `source <> 'wwtbam'` — with WWTBAM as the only source that pool
is always empty and the blend silently runs at 100/0. OpenTDB fills it.

### OpenTDB corpus

**Attribution.** The questions come from [OpenTDB](https://opentdb.com),
licensed **CC BY-SA 4.0**. The per-category CSVs are taken from the
[QuartzWarrior/OTDB-Source](https://github.com/QuartzWarrior/OTDB-Source)
mirror and decoded into the vendored `data/opentdb.csv` (1,914 rows). Those
questions are shared under CC BY-SA 4.0.

Only the categories the casual blacklist allows are vendored — every
`Entertainment:*` category, `Celebrities` and `Science: Gadgets` are excluded
by `models.CasualBlacklistOpentdb*`, plus `Mythology` as a product decision
for the easy band. Kept: Animals, Art, General Knowledge, Geography, History,
Politics, Science & Nature, Science: Computers, Science: Mathematics, Sports,
Vehicles. Rebuild the CSV from a mirror checkout with:

```sh
python3 data/opentdb_build.py <mirror-dir> data/opentdb.csv
```

| File | What it is |
|---|---|
| `data/opentdb.csv` | the vendored, decoded corpus (`question,answer,category`) |
| `data/opentdb_strengths.csv` | the curated 1–15 ratings, same pub-quiz rubric as `label/rubric.md` |

`data/opentdb_strengths.csv` is what the importer reads: **a row without a
strength is invisible to the casual pool**, so importing without it (or with
it missing) buys nothing at levels 1–3.

Upstream OpenTDB rows carry a question and an answer but no options, while
`millionaire_questions.options` requires exactly four. The importer is the
adapter: it samples the three distractors from the other answers of the same
category, rejecting case-folded duplicates and containment matches ("Paris"
against "Paris, France" would leave two defensible options).

The WWTBAM difficulty ratings are shipped the same way — as data plus an
applier, because every import run mints fresh UUIDs and a local database state
is not reproducible:

| File | What it is |
|---|---|
| `data/question_strengths.csv` | the WWTBAM ratings (question → strength) |
| `migrations/1001_apply_question_strengths.sql` | a self-contained applier, keyed by question text |

```sh
psql "$DATABASE_URL" -f migrations/1001_apply_question_strengths.sql
```

Both generated files are rebuilt from the live database with
`python3 data/question_strengths_build.py`.

The OTQA corpus and the rating/label pipeline use `cmd/seed`, `cmd/label` the
same way (see `cmd/*/main.go` flags).

## Out of Line

Out of Line is a real-time multiplayer judge-and-submit party game.
Card content is embedded in the API binary under
`internal/outofline/content/decks/*.json`; adding a deck requires updating the
JSON and rebuilding the API.

Current decks: House (100 prompts / 310 answers, 15+, always on), Group Chat
(40/110), Work Mode (40/110), Bad Decisions (40/110), Deadpan Doom (40/110),
Family Gathering (40/110, all 15+), and After Hours (40/110, 18+, suggestive).

Key endpoints:

- `GET /api/out-of-line/decks` — public deck metadata
- `POST /api/out-of-line/rooms` — create room, returns host token
- `GET /api/out-of-line/rooms/:code` — public room preview
- `POST /api/out-of-line/rooms/:code/start|submit|pick|next|force|remove|rematch`
- `GET /ws/out-of-line/rooms/:code?token=...` — reconnect
- `GET /ws/out-of-line/rooms/:code?name=...&adult_confirmed=...` — join

The server sends personalized `room_state` snapshots: hands are private, judge
submissions are anonymous during judging, and reveal/finished states expose
authors and winner.

## Misclue

Misclue is a secret-word social deduction game. Party Mode is fully client-side;
Solo Mode uses a stateless AI endpoint.

- `POST /api/misclue/ai/turn` — resolve an AI character action (`clue`, `vote`, `drifter_guess`). Requires `OPENROUTER_API_KEY`.

The endpoint validates model output, enforces clue/vote rules, and retries once on model/validation failure. If both attempts fail, it returns a clear 502 error; the frontend shows a manual retry (no fake fallback clues).

## Setup

1. Open the repo in VS Code and reopen in the dev container.
2. `go mod tidy`
3. Apply `migrations/004_outofline_schema.sql` if your database volume predates
   Out of Line.
4. `go run ./cmd/api` → API on <http://localhost:8080>.
5. Frontend lives in the sibling `boggle_fe` repo: `npm run dev` → hub on
   <http://localhost:5173> (`/` picks a game).

## Environment variables

The API loads env files at startup: `.env.local` first, then `.env` (process
env vars always win). `.env` is committed with non-secret defaults; `.env.local`
is gitignored for local overrides/secrets.

To configure locally, copy `.env` to `.env.local` and set/override values there
(for example `OPENROUTER_API_KEY`). Do not put secrets in `.env`.

- `DATABASE_URL` — defaults to the devcontainer `db` service.
- `ALLOWED_ORIGINS` — comma-separated; feeds REST CORS and WebSocket origin checks.
- `PORT` — bind port (default `8080`).
- `OPENROUTER_API_KEY` — required for Misclue Solo Mode AI turns.
- `OPENROUTER_MODEL` — optional; defaults to `openrouter/free` (free models router). Pin a specific model in `.env.local` if you want more stable behavior.
- `OPENROUTER_BASE_URL` — optional; defaults to `https://openrouter.ai/api/v1`.
- `MISCLUE_AI_RATE_LIMIT_PER_MIN` — optional; stricter per-IP rate limit for `POST /api/misclue/ai/turn`. Default is the shared strict limit (300/min). If set to a positive integer <= 300, that value is used.

## Testing

```sh
go test ./...
```

Millionaire's suites run against a real Postgres: point `DATABASE_URL` at a
database whose `millionaire_*` schema is migrated (a disposable one is fine —
they create and delete their own rooms).