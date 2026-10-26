# auditor

TrackLin.es's audit service (TL-5): an append-only record of what happened in each organization, each
ticket's stages and, on Pro and Enterprise, the organization's audit log. It keeps them in **its own
database**, so the backend can't change history. Only the backend talks to it, over Railway's private
network; it has no public domain.

This is the empty, running service so far: config, health and deploys. Storing and reading events comes next
(TL-663, TL-664, TL-665).

## Routes

| Route | What |
|---|---|
| `GET /health` | `200 {"name":"auditor","status":"pass"}` when its Postgres is reachable and usable, `503` with `"fail"` otherwise. It never shows addresses. |

## Configuration

It reads its settings with [go-config](https://github.com/keloran/go-config), like the backend, so the names
are the same. A `.env` file is read when there is one; variables already set win.

| Variable | What |
|---|---|
| `RDS_URL` | the auditor's **own** Postgres (not the backend's). Or `RDS_HOSTNAME`, `RDS_PORT`, `RDS_DB`, `RDS_USERNAME`, `RDS_PASSWORD` |
| `PORT` / `HTTP_PORT` | where it listens: Railway's `PORT`, else `HTTP_PORT` (8080 in the image) |
| `BUGFIXES_AGENT_KEY`, `BUGFIXES_AGENT_SECRET`, `BUGFIXES_SERVICE` | the [Bugfixes](https://bugfix.es) agent for the service `auditor`. Without them, or with `BUGFIXES_LOCAL_ONLY=true`, errors are only logged |
| `OP_SERVICE_ACCOUNT_TOKEN` | 1Password: with it, the Bugfixes variables come from the `tracklines` vault, item `bugfixes_auditor` (`username`, `credential`, `service`) |

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
