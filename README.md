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
   `POST <EVA_RPC_URL>/?m=CmfTask.list` (kwargs: `filter`, `slice`, `order_by`),
   fetches each task's comments via `CmfComment.list` (filter
   `parent == CmfTask:<id>`), builds the embedding text
   `name + text + result + comments`, embeds it in batches and upserts the
   vector with metadata into the Qdrant collection `eva_tasks` (point id =
   hash of the Eva task id, payload keeps `eva_id`/`code`).
2. `listener` accepts `POST /webhook/task`. It extracts the task id from the
   payload (`LISTENER_TASK_ID_PATH`, dotted path, e.g. `task.id`), indexes the
   task, then runs a Qdrant cosine search for the closest tasks above
   `LISTENER_SCORE_THRESHOLD`. It links each result in Eva via
   `CmfRelationOption.create` with kwargs
   `{out_link, in_link, relation_type}` and records the link in the point
   payload to avoid duplicates. With `LINKS_DRY_RUN=true` (default) it only
   logs.

## Eva API wiring

The client speaks the JSON-RPC 2.2 dialect from the official OpenAPI spec
`oas_evateam_v1_9_22.json`: `POST {EVA_RPC_URL}/?m=<Model>.<method>` with a
body of `{"jsonrpc":"2.2","method":...,"callid":"<uuid>","kwargs":{...}}`.
`kwargs` carries `filter` (array of `[field, op, value]` triples),
`fields`, `slice` (`[offset, limit]`), `order_by` and `include_archived`.
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
| `EVA_TASK_LINK_METHOD` | `CmfRelationOption.create` | kwargs `{out_link, in_link, relation_type}` |
| `EVA_LINK_RELATION_TYPE` | `related` | Relation type (instance-specific: `blocks`, `parent`, ...) |
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

## Embedding

`EMBED_PROVIDER=ollama` (default) uses
`POST <OLLAMA_URL>/api/embed` (`{"model": ..., "input": [...]}`).
`EMBED_PROVIDER=tei` uses `POST <TEI_URL>/embed`
(`{"inputs": ["..."]}`). Set `EMBED_DIM` to match your model.

## Notes / TODOs

- The webhook payload schema and the SSO/token auth interplay are the two
  instance-specific unknowns — see `internal/eva/client.go` (`Auth`) and
  `.env.example` for the knobs.
- `LISTENER_SCORE_THRESHOLD` tuning: start `0.75` with `LINKS_DRY_RUN=true`,
  inspect the logs, then lower/raise and flip dry-run when satisfied.

## Спек EVA API
https://redocly.github.io/redoc/?url=https://docs.evateam.ru/files/obj/CmfDocument/CmfDocument%3Ad22/CmfDocument%3Ad22c35d4-581b-11f0-bfd0-00161e12a413/oas_evateam_v1_9_22.json