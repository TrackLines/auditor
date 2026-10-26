# auditor

TrackLin.es's audit service (TL-5): an append-only record of what happened in each organization, each
ticket's stages and, on Pro and Enterprise, the organization's audit log. It keeps them in **its own
database**, so the backend can't change history. Only the backend talks to it, over Railway's private
network; it has no public domain.

It stores events (TL-663), reads them back (TL-664), and deletes what's purged (TL-665). Plan retention
(TL-676) comes next.

## Routes

| Route | What |
|---|---|
| `GET /health` | `200 {"name":"auditor","status":"pass"}` when its Postgres is reachable and usable, `503` with `"fail"` otherwise. It never shows addresses. |
| `POST /internal/events` | the backend's events, up to 500 at a time: `{"events": [...]}` → `{"accepted": [ids], "rejected": [{"id", "reason"}]}`. Needs `Authorization: Bearer <AUDIT_SECRET>`. |

| `GET /internal/orgs/{org}/tickets/{ticket}/history` | that ticket's `ticket.stage` events, oldest first, in exactly the shape of the backend's `GET /api/tickets/{id}/history` |
| `GET /internal/orgs/{org}/events` | the org log, newest first: `?actor=&action=&target_type=&target_id=&project_id=&from=&to=&since=&cursor=&limit=` → `{"events": [...], "next_cursor": "…" or null}` |

`/internal/` routes are for the backend only, which checks who's asking and their plan first.

**The org log:**
- `action` is exact, or a prefix when it ends in a dot (`api_key.`). Ticket stages are left out unless `action` asks for them.
- `from`, `to` and `since` are RFC 3339. `since` is how far back the org's plan keeps events, so expired events never show, even before they're purged.
- `limit` is 1 to 200 (default 50). Pass `next_cursor` as `cursor` for the next, older page. Without `AUDIT_SECRET` they refuse everything (503).

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

### Purges

The privacy policy's promises are kept as the backend's purge events arrive:

- **`ticket.purged`:** that ticket's stages are deleted. Org log entries about it stay, since they hold only ids.
- **`org.purged`:** every event of the organization is deleted, that one included.

Both are remembered in `purged` (ids only), so events for them that arrive afterwards, from a late batch or a
resend, are accepted but not kept. Each purge logs how many events it deleted.

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
