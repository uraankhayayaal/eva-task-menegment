# eva-similar

Go toolkit for semantic search across **Eva** tasks. It indexes the title,
description, result and comments of every task into **Qdrant** via a local
embedding server (Ollama or text-embeddings-inference), then links newly
created tasks to their semantically closest existing tasks.

```
Eva (JSON-RPC 2.0) ──► indexer ──► embeddings (Ollama/TEI) ──► Qdrant
Eva webhook ────────► listener ──► embed + vector search  ──► link tasks in Eva
```

## Components

| Binary          | Role                                                                |
|-----------------|---------------------------------------------------------------------|
| `cmd/indexer`   | Batch reads all tasks (+ comments) from Eva, embeds and upserts them |
| `cmd/listener`  | HTTP webhook server: on a new task, finds similar ones and links them |

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

# point Eva's webhook at http://<host>:8080/webhook/task
```

Manual triggers (useful while wiring webhooks):
```bash
curl http://localhost:8080/index/<task_id>   # index a single task
curl http://localhost:8080/link/<task_id>    # index + find + link similar
curl http://localhost:8080/healthz
```

## How it works

1. `indexer` pages through Eva tasks via
   `POST <EVA_RPC_URL>?m=CmfTask.list`, fetches each task's comments, builds
   the embedding text `title + description + result + comments`, embeds it in
   batches and upserts the vector with metadata into the Qdrant collection
   `eva_tasks` (point id = hash of the Eva task id, payload keeps `eva_id`).
2. `listener` accepts `POST /webhook/task`. It extracts the task id from the
   payload (`LISTENER_TASK_ID_PATH`, dotted path, e.g. `task.id`), indexes the
   task, then runs a Qdrant cosine search for the closest tasks above
   `LISTENER_SCORE_THRESHOLD`. It links each result in Eva via
   `EVA_TASK_LINK_METHOD` (default `CmfTask.save_links`; override if your
   instance differs) and records the link in the point payload to avoid
   duplicates. With `LINKS_DRY_RUN=true` (default) it only logs.

## Eva API wiring

The client speaks the JSON-RPC 2.0 dialect used by Eva's web app:
`POST <api_url>?m=<Model>.<method>` with a body carrying
`model`/`method`/`args`/`kwargs`/`filter`. Everything instance-specific is
config:

| Variable | Default | Notes |
|---|---|---|
| `EVA_RPC_URL` | `http://localhost:8080/api` | Full endpoint of your Eva instance |
| `EVA_API_TOKEN` | — | **Recommended.** Sent as `Authorization: Bearer <token>` |
| `EVA_AUTH_HEADER` | — | Custom static `Name: Value` header; lower priority than API token |
| `EVA_LOGIN` / `EVA_PASSWORD` | — | Password login via `EVA_AUTH_LOGIN_URL`; only used when no token is set |
| `EVA_TASK_LIST_METHOD` | `CmfTask.list` | Positional args `[filter, offset, limit]` |
| `EVA_TASK_GET_METHOD` | `CmfTask.get` | Arg: task id |
| `EVA_TASK_COMMENTS_METHOD` | `CmfTaskComment.list` | Args `[task_id, filter]` |
| `EVA_TASK_LINK_METHOD` | `CmfTask.save_links` | Called with `[[{from,to,type}]]` — override to match your API |
| `EVA_TASK_ID_FIELD` | `id` | Dotted path of the id in the task object |
| `EVA_TASK_TITLE_FIELD` / `_DESC_FIELD` / `_RESULT_FIELD` | `title` / `description` / `result` | Dotted paths of the text fields |
| `EVA_TASK_COMMENT_FIELD` | `text` | Comment text field |
| `EVA_TASK_PAYLOAD_FIELDS` | `number,project_id,status` | Extra fields copied into the vector payload |

The exact model/method names and field layout depend on your Eva version; the
parameters above let you adapt without code changes. If the RPC `result` is
wrapped (`{"result": {...}}`) or returned as a bare list, the client handles
both.

**Auth priority:** `EVA_API_TOKEN` (Bearer) → `EVA_AUTH_HEADER` → login/password.
The startup log prints `auth=` so you can verify which scheme was picked.

**Webhook auth:** set `WEBHOOK_TOKEN` and Eva must send it as
`Authorization: Bearer <token>` (or `X-Webhook-Token` / `?token=`). The
`/webhook/task`, `/index/<id>` and `/link/<id>` endpoints return `401`
otherwise.

## Embedding

`EMBED_PROVIDER=ollama` (default) uses
`POST <OLLAMA_URL>/api/embed` (`{"model": ..., "input": [...]}`).
`EMBED_PROVIDER=tei` uses `POST <TEI_URL>/embed`
(`{"inputs": ["..."]}`). Set `EMBED_DIM` to match your model.

## Notes / TODOs

- The webhook payload schema and the "link tasks" RPC method are the two
  unknowns that depend on your Eva instance — see `internal/eva/task.go`
  (`LinkTasks`) and `.env.example` for the knobs.
- `LISTENER_PATH`/score tuning: start `LISTENER_SCORE_THRESHOLD=0.75` with
  `LINKS_DRY_RUN=true`, inspect the logs, then lower/raise and flip dry-run
  when satisfied.

## Спек EVA API
https://redocly.github.io/redoc/?url=https://docs.evateam.ru/files/obj/CmfDocument/CmfDocument%3Ad22/CmfDocument%3Ad22c35d4-581b-11f0-bfd0-00161e12a413/oas_evateam_v1_9_22.json