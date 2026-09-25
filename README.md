# eva-similar

Go toolkit for semantic search across **Eva** tasks. It indexes the title,
description, result and comments of every task into **Qdrant** via a local
embedding server (Ollama or text-embeddings-inference), then links newly
created tasks to their semantically closest existing tasks.

```
Eva (JSON-RPC 2.2) ──► indexer ──► embeddings (Ollama/TEI) ──► Qdrant
Eva webhook ────────► listener ──► embed + vector search  ──► link tasks in Eva
```

## Components

| Binary          | Role                                                                |
|-----------------|---------------------------------------------------------------------|
| `cmd/indexer`   | Continuously reads new tasks (+ comments) from Eva and upserts them |
| `cmd/listener`  | HTTP webhook server: on a new task, finds similar ones and links them |
| `cmd/linker`    | Polling listener (no webhook/NAT needed): scans new tasks, finds + links |
| `cmd/dedup`     | One-shot: finds and (optionally) deletes duplicate points in Qdrant |

## Prerequisites

- Go 1.22+
- Docker (for Qdrant / Ollama)

## Quick start

```bash
cp .env.example .env
# edit .env: set EVA_RPC_URL, EVA_API_TOKEN, WEBHOOK_TOKEN, thresholds.
# The root .env is auto-loaded at startup; real shell env vars win over it.

docker compose up -d            # qdrant only (ollama runs on the host)

# pull the embedding model into your host ollama
ollama pull nomic-embed-text

# 1) backfill all tasks
go run ./cmd/indexer

# 2) run the listener
go run ./cmd/listener

# point Eva's webhook at http://<host>:8480/webhook/task
```

Manual triggers (useful while wiring webhooks):
```bash
curl http://localhost:8480/index/<task_id>     # index a single task
curl http://localhost:8480/link/<task_id>      # index + find + link similar (relations)
curl http://localhost:8480/comment/<task_id>   # index + find + post «Автолинкер» comment (no relations)
curl http://localhost:8480/healthz
```

No webhook? Use the poller instead (outbound-only, works behind NAT/VPN):
```bash
LINKER_WATERMARK_FILE=watermark.json go run ./cmd/linker
```
It scans `CmfTask.list` every `LINKER_POLL_INTERVAL_SECONDS` for tasks created
after the persisted watermark and processes each — same matching logic as the
webhook path, minus the inbound endpoint. The first run starts from
`LINKER_INITIAL_LOOKBACK_HOURS` and saves a watermark so restarts only reprocess
genuinely new tasks.

By default the linker does **not** create Eva relations: it posts an
«Автолинкер» comment on the task listing its similar tasks (`LINKER_LINK_MODE=comment`).
To restore relation creation in the linker set `LINKER_LINK_MODE=link`; the
listener's `/link` and `/webhook/task` always create relations regardless.

Deduplication (remove duplicate points — same `eva_id` + `chunk_index`):
```bash
go run ./cmd/dedup                      # dry run, scans every Qdrant collection
go run ./cmd/dedup -collections eva_tasks
go run ./cmd/dedup -collections a,b      # a single collection or a comma-separated list
go run ./cmd/dedup -apply                # actually delete the duplicates
```

## How it works

1. `indexer` continuously pages through Eva tasks via
   `POST <EVA_RPC_URL>/?m=CmfTask.list` (kwargs: `filter`, `fields`, `slice`, `order_by`),
   compares task IDs with Qdrant and skips tasks that are already indexed,
   fetches new tasks' comments via `CmfComment.list` (filter
   `parent == CmfTask:<id>`), chunks the text (`name + text + result +
   comments`) semantically, embeds each chunk and upserts the points into the
   Qdrant collection `eva_tasks` (point id = hash of task id + chunk index,
   payload keeps `eva_id`/`code`/`chunk_index`).
   After a complete scan it waits one minute and checks Eva again, so newly
   created tasks are picked up without restarting the process.
2. `listener` accepts `POST /webhook/task`. It extracts the task id from the
   payload (`LISTENER_TASK_ID_PATH`, dotted path, e.g. `task.id`), indexes the
   task, then runs a Qdrant cosine search with every chunk of the new task for
   the closest tasks above `LISTENER_SCORE_THRESHOLD` (best score per task).
   It links each result in Eva via `CmfRelationOption.create` with kwargs
   `{out_link, in_link, relation_type}` and records the link in the chunk
   payloads to avoid duplicates. With `LINKS_DRY_RUN=true` (default) it only
   logs.

## Eva API wiring

The client speaks the JSON-RPC 2.2 dialect from the official OpenAPI spec
`oas_evateam_v1_9_22.json`: `POST {EVA_RPC_URL}/?m=<Model>.<method>` with a
body of `{"jsonrpc":"2.2","method":...,"callid":"<uuid>","kwargs":{...}}`.
`kwargs` carries `filter` (array of `[field, op, value]` triples),
`fields`, `slice` (the half-open range `[start, end)`), `order_by` and `include_archived`.
Everything instance-specific is config:

| Variable | Default | Notes |
|---|---|---|
| `EVA_RPC_URL` | `https://eva.staff.rfn.ru/api` | Base endpoint; client appends `/?m=...` |
| `EVA_API_TOKEN` | — | **Recommended.** Sent as `Authorization: Bearer <token>` |
| `EVA_APITOKEN_HEADER` / `_SCHEME` | `Authorization` / `Bearer` | Header/scheme for the token |
| `EVA_AUTH_HEADER` | — | Custom static `Name: Value` header; lower priority than API token |
| `EVA_LOGIN` / `EVA_PASSWORD` | — | SSO login via `EVA_AUTH_LOGIN_URL` (`/auth/signin`); only used when no token is set |
| `EVA_TASK_LIST_METHOD` | `CmfTask.list` | kwargs `{filter, slice, include_archived}` |
| `EVA_TASK_LIST_FILTER` | — | JSON filter triples applied to every page, e.g. `[["status","!=","closed"]]` |
| `EVA_TASK_GET_METHOD` | `CmfTask.get` | kwargs `{filter: [[EVA_TASK_GET_FILTER_FIELD,"==",id]]}` |
| `EVA_TASK_COMMENTS_METHOD` | `CmfComment.list` | kwargs `{filter: [[parent,"==",CmfTask:<id>]]}` |
| `EVA_TASK_COMMENT_CREATE_METHOD` | `CmfComment.create` | kwargs `{parent, text}`; posts the «Автолинкер» comment in `LINKER_LINK_MODE=comment` |
| `EVA_TASK_LINK_METHOD` | `CmfRelationOption.create` | kwargs `{out_link, in_link, relation_type}` |
| `EVA_LINK_RELATION_TYPE` | — | Full id from `CmfRelationType.list`, e.g. `CmfRelationType:...` (system.link = «Взаимная»). Plain codes like `related` are rejected |
| `EVA_TASK_ID_FIELD` / `_CODE_FIELD` | `id` / `code` | Task uuid / short code; code is used as `out_link`/`in_link` |
| `EVA_TASK_TITLE_FIELD` / `_DESC_FIELD` / `_RESULT_FIELD` | `name` / `text` / `result` | Dotted paths of the text fields (Eva: `name`=заголовок, `text`=HTML-описание) |
| `EVA_TASK_COMMENT_FIELD` / `_PARENT_PREFIX` | `text` / `CmfTask:` | Comment text field and task reference prefix |
| `EVA_TASK_PAYLOAD_FIELDS` | `number,project_id,status` | Extra fields copied into the vector payload |

The exact model/method names and field layout depend on your Eva version; the
parameters above let you adapt without code changes. If the RPC `result` is
wrapped or returned as a bare list, the client handles both.

**Auth priority:** `EVA_API_TOKEN` (Bearer) → `EVA_AUTH_HEADER` → login/password.
The startup log prints `auth=` so you can verify which scheme was picked.

> **rfn SSO:** `eva.staff.rfn.ru` sits behind a custom «Авторизация» proxy that
> 302-redirects every call to `/auth/signin` — the API token may not pass
> through it. To work around, either whitelist `/api/` for the Bearer token on
> the proxy, or set `EVA_LOGIN`/`EVA_PASSWORD`/`EVA_AUTH_LOGIN_URL` so the
> client POSTs the SSO form (plaintext password is the SSO's documented mode
> for external systems) and reuses the session cookie.

**Webhook auth:** set `WEBHOOK_TOKEN` and Eva must send it as
`Authorization: Bearer <token>` (or `X-Webhook-Token` / `?token=`). The
`/webhook/task`, `/index/<id>` and `/link/<id>` endpoints return `401`
otherwise.

## Embedding & chunking

`EMBED_PROVIDER=ollama` (default) uses
`POST <OLLAMA_URL>/api/embed` (`{"model": ..., "input": [...]}`).
`EMBED_PROVIDER=tei` uses `POST <TEI_URL>/embed`
(`{"inputs": ["..."]}`). Set `EMBED_DIM` to match your model.

With `EMBED_CHUNKING=semantic` (default) every task is split into semantic
chunks before being embedded (`internal/similar/chunks.go`):

- The HTML sections (description, result) are parsed for structure: headings
  (`# `), list items (`- `) and paragraphs become separate blocks; tiny
  markers keep the structure readable for the embedding model.
- Small blocks are merged greedily up to `EMBED_CHUNK_CHARS` (default 2000
  runes); a single oversized block is cut at sentence boundaries, with
  `EMBED_CHUNK_OVERLAP` runes of overlap for window-split sentences.
- The task title is its own chunk, comments are grouped per chunk.
- Each chunk is embedded separately and stored as its own Qdrant point
  (point id = hash(task_id + chunk index); payload keeps `eva_id`,
  `chunk_index`, `chunk_count`). Similarity search queries with every chunk
  of the new task and aggregates the best score per candidate task.

Set `EMBED_CHUNKING=none` to fall back to the legacy behaviour: one
aggregated text (`EMBED_MAX_CHARS` cap), one vector, one point per task.
Switching modes on an already-populated collection requires
`INDEXER_RECREATE=true` because the point ids differ.

## Notes / TODOs

- The webhook payload schema and the SSO/token auth interplay are the two
  instance-specific unknowns — see `internal/eva/client.go` (`Auth`) and
  `.env.example` for the knobs.
- `LISTENER_SCORE_THRESHOLD` tuning: start `0.75` with `LINKS_DRY_RUN=true`,
  inspect the logs, then lower/raise and flip dry-run when satisfied.

## Спек EVA API
https://redocly.github.io/redoc/?url=https://docs.evateam.ru/files/obj/CmfDocument/CmfDocument%3Ad22/CmfDocument%3Ad22c35d4-581b-11f0-bfd0-00161e12a413/oas_evateam_v1_9_22.json
