# auditor

TrackLin.es's audit service (TL-5): an append-only record of what happened in each organization, each
ticket's stages and, on Pro and Enterprise, the organization's audit log. It keeps them in **its own
database**, so the backend can't change history. Only the backend talks to it, over Railway's private
network; it has no public domain.

So far it stores events (TL-663). Reading them back (TL-664) and retention (TL-665) come next.

## Routes

| Route | What |
|---|---|
| `GET /health` | `200 {"name":"auditor","status":"pass"}` when its Postgres is reachable and usable, `503` with `"fail"` otherwise. It never shows addresses. |
| `POST /internal/events` | the backend's events, up to 500 at a time: `{"events": [...]}` → `{"accepted": [ids], "rejected": [{"id", "reason"}]}`. Needs `Authorization: Bearer <AUDIT_SECRET>`. |

`/internal/` routes are for the backend only. Without `AUDIT_SECRET` they refuse everything (503).

### Events

Each event is who did what to what, in which organization, and when:

| Field | What |
|---|---|
| `id` | a UUID from the backend's outbox. A resend of an id already kept is accepted and changes nothing |
| `org_id` | the Clerk organization |
| `at` | when it happened (RFC 3339) |
| `actor`, `actor_kind` | a user id, or an agent or service key's name; `person`, `agent`, `service`, or `system` with no actor |
| `action` | `thing.happened`, e.g. `ticket.stage`, `api_key.created` |
| `target_type`, `target_id`, `project_id` | what it was done to (optional) |
| `data` | a JSON object up to 16 KB: details, **never secrets or ticket content** |

Rejected events are listed with a reason, and the rest of the batch is still kept. Sending a rejected one again
won't help.

The `events` table is append-only. A trigger refuses every `UPDATE`, `DELETE` and `TRUNCATE`, except deletes in a
transaction that has set `auditor.purging = 'on'`, which only the auditor's own retention and deletion does.
Migrations run on boot.

## Configuration

It reads its settings with [go-config](https://github.com/keloran/go-config), like the backend, so the names
are the same. A `.env` file is read when there is one; variables already set win.

| Variable | What |
|---|---|
| `RDS_URL` | the auditor's **own** Postgres (not the backend's). Or `RDS_HOSTNAME`, `RDS_PORT`, `RDS_DB`, `RDS_USERNAME`, `RDS_PASSWORD` |
| `PORT` / `HTTP_PORT` | where it listens: Railway's `PORT`, else `HTTP_PORT` (8080 in the image) |
| `BUGFIXES_AGENT_KEY`, `BUGFIXES_AGENT_SECRET` | the [Bugfixes](https://bugfix.es) agent; reports are sent as the service `auditor`. Without them, or with `BUGFIXES_LOCAL_ONLY=true`, errors are only logged |
| `AUDIT_SECRET` | the token the backend sends. The backend's copy is the same value |
| `OP_SERVICE_ACCOUNT_TOKEN` | 1Password: with it, secrets come from the `tracklines` vault: `bugfixes_auditor` (`username` is the agent key, `credential` the secret), and `audit_auditor` (`credential` → `AUDIT_SECRET`) |

## Railway

1. In the Tracklines project, add a **Postgres** for the auditor, and a service from this repo called **`auditor`**
   (the deploy workflow links to that name).
2. **Settings:**
   - **Build:** Dockerfile (Railway finds `Dockerfile` at the root).
   - **Networking:** no public domain. The backend reaches it on the private network (`http://auditor.railway.internal:<PORT>`).
   - **Healthcheck path:** `/health`.
   - **Restart policy:** on failure.
   - **Deploys:** only from tagged releases. Turn off GitHub auto-deploy from `main`.
3. **Variables:** `RDS_URL` as a reference to the auditor's own Postgres, and `OP_SERVICE_ACCOUNT_TOKEN`.
4. **GitHub:** the `production` environment needs the `RAILWAY_TOKEN` and `RAILWAY_PROJECT_ID` secrets, as on the other repos.

Publishing the Release Drafter draft creates a `v*` tag, and `.github/workflows/deploy-production.yml` runs
`railway up` for it.

## Development

```sh
go test ./...   # starts a throwaway Postgres in Docker (testcontainers)
go run ./cmd/auditor
```
