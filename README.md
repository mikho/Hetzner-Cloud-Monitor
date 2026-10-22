# Hetzner Cloud Dashboard

A small, self-hosted dashboard for your Hetzner Cloud project, run in Docker.

- Server list with status, type, location, IPs and traffic use
- Whether **backups** are enabled on each server
- **Monthly cost** per server and in total, including backups, primary IPs, volumes, floating IPs, snapshots, load balancers and traffic overage
- Idle resources you are paying for (unattached volumes and IPs)
- **Storage Boxes** in their own box: status, type, location, space used and monthly cost (not added to the main total)
- CPU, network and disk charts per server (1h / 24h / 7d / 30d)

The Hetzner API token stays on the server side. The browser never sees it, and the app only reads from the API.

## Requirements

- Docker with Compose (Docker Desktop, OrbStack, or Docker Engine + the compose plugin)
- A Hetzner Cloud API token. Create a **Read-only** one in the Cloud Console under *your project → Security → API tokens*.

A token covers one project. To monitor several projects, add one token per project (see below). All projects are shown together, with a Project column.

## Install

```bash
# 1. Configure
cp .env.example .env
#    edit .env and set HCLOUD_TOKEN

cp prices.example.json prices.json     # optional, see "Price overrides"; use {} for none

# 2. Build and start
docker compose up -d --build

# 3. Open
open http://localhost:3000             # or just visit it in a browser
```

`prices.json` must exist before the first start. If it doesn't, Docker creates a directory with that name and the mount fails. An empty `{}` is fine.

## Configuration

Set these in `.env`:

| Variable | Default | Description |
|---|---|---|
| `HCLOUD_TOKEN` | one of these | Token for a single project (read-only is enough). Labelled `default`. |
| `HCLOUD_TOKEN_<NAME>` | one of these | One token per project, for example `HCLOUD_TOKEN_PRODUCTION`. `<NAME>` becomes the project label (lowercased, `_` shown as a space). You can add as many as you like. |
| `DASHBOARD_PASSWORD` | empty | If set, the dashboard asks for HTTP basic auth. Any username, this password. |
| `PRICE_BASIS` | `gross` | `gross` shows prices including VAT, `net` shows prices excluding VAT. |
| `HETZNER_STORAGE_TOKEN[_<NAME>]` | the project's cloud token | Optional. Storage Boxes are read from `api.hetzner.com`. Set this only if a project's Cloud token is rejected there. Use the same `<NAME>` as the project's token. |
| `PORT` | `3000` | Port inside the container. If you change it, update the port mapping in `docker-compose.yml` too. |

After changing `.env`, apply it with `docker compose up -d`.

## Multiple projects

Put one token per project in `.env`:

```
HCLOUD_TOKEN_PRODUCTION=xxxxxxxx
HCLOUD_TOKEN_STAGING=yyyyyyyy
HCLOUD_TOKEN_CLIENT_ACME=zzzzzzzz
```

Then run `docker compose up -d`. With more than one project the table gets a Project column, and the totals, cost breakdown and resource lists add everything together. If one project fails (for example an expired token), the others still load and a warning names the failing project. Project names must be unique, and each token needs only read access.

Debug routes and price overrides work per project: add `&project=production` to `/api/debug/...` URLs (not needed with a single project). Overrides in `prices.json` match by server name across all projects.

## Price overrides

The Hetzner API only returns the **current list price** for each server type. Servers created before a price change keep their old rate, so the dashboard may show a higher price than you actually pay. Servers still using a list price get a yellow `list` tag in the table.

To use the real price, edit `prices.json`:

```json
{
  "byType":   { "cpx21": 8.49 },
  "byServer": { "web-1": 7.00 }
}
```

- `byServer` takes a server name or ID and wins over `byType`.
- `byType` applies to every server of that type.
- Prices are per month and used exactly as written. Use the same basis as `PRICE_BASIS` (with VAT for `gross`, without for `net`).
- Changes are picked up on the next refresh. No rebuild or restart is needed.
- Backup cost is calculated from the overridden price.

Prices are matched on each server's own location. If a server type has no price for that location, a warning appears on the page and you can set an override for it.

## What the Docker files do

**`Dockerfile`**
```dockerfile
FROM node:22-alpine          # small Node image; the app has no npm dependencies
WORKDIR /app
COPY server.js ./            # backend
COPY public ./public         # web UI
ENV NODE_ENV=production PORT=3000
USER node                    # runs as a non-root user
EXPOSE 3000
HEALTHCHECK ... /healthz     # Docker marks the container healthy via GET /healthz
CMD ["node", "server.js"]
```

**`docker-compose.yml`**
```yaml
services:
  dashboard:
    build: .                          # build the image from the Dockerfile
    restart: unless-stopped           # come back after reboots and crashes
    env_file: .env                    # token and settings, read from your machine
    ports:
      - "127.0.0.1:3000:3000"         # only reachable from this machine
    volumes:
      - ./prices.json:/config/prices.json:ro   # price overrides, read-only
    read_only: true                   # container filesystem is read-only
    cap_drop: [ALL]                   # no extra Linux capabilities
    security_opt: ["no-new-privileges:true"]
```

**`.dockerignore`** keeps `.env`, `prices.json`, `.git` and this README out of the image, so your token never ends up inside it. The token is passed at runtime through `env_file`.

## Everyday commands

```bash
docker compose logs -f          # follow logs
docker compose ps               # status and health
docker compose up -d --build    # rebuild after changing code
docker compose restart          # restart without rebuilding
docker compose down             # stop and remove the container
```

Without Compose:
```bash
docker build -t hetzner-dashboard .
docker run -d --name hetzner-dashboard --env-file .env \
  -v "$PWD/prices.json:/config/prices.json:ro" \
  -p 127.0.0.1:3000:3000 hetzner-dashboard
```

## Running on a server / exposing it

The port is bound to `127.0.0.1` on purpose. To reach the dashboard from elsewhere:

- Put a reverse proxy with HTTPS (Caddy, nginx, Traefik) in front of `localhost:3000`, **and** set `DASHBOARD_PASSWORD`. Basic auth over plain HTTP sends the password unencrypted.
- Or reach it over a VPN or SSH tunnel: `ssh -L 3000:127.0.0.1:3000 your-server`.

Avoid changing the mapping to `"3000:3000"` on an internet-facing machine without HTTPS and a password.

Never commit `.env`. It contains your API token.

## API

All routes are read-only and use the same password if one is set.

| Route | Description |
|---|---|
| `GET /api/servers` | Servers, costs, other resources and warnings. |
| `GET /api/metrics?id=<id>&project=<name>&hours=<n>` | CPU, disk and network time series for one server (1 to 720 hours). |
| `GET /api/debug/server?id=<id>` | Raw Hetzner API object for one server. Useful for checking prices and locations. |
| `GET /api/debug/storage-boxes` | Raw Storage Box API response and HTTP status for your token. Useful when no boxes show up. |
| `GET /healthz` | Health check (no auth). |

Responses from the Hetzner API are cached for 30 seconds.

## Troubleshooting

- **`No token set`**: `.env` is missing or has no `HCLOUD_TOKEN` / `HCLOUD_TOKEN_<NAME>` line with a value. Check with `docker compose logs`.
- **Prices look wrong**: see "Price overrides". Also check `/api/debug/server?id=<id>` for the server's `server_type.prices` and location.
- **A route returns plain-text "Not found"**: the container is probably running an old image. Rebuild with `docker compose build --no-cache && docker compose up -d`.
- **Mount error about `prices.json`**: create the file first (`echo '{}' > prices.json`). If Docker already made a directory with that name, delete it and recreate it as a file.
- **Network chart looks 8x too high or low**: the chart assumes the API reports bytes per second. See the `* 8` in the Network chart in `public/index.html`.
- **"storage boxes: ..." warning**: the Storage Box API rejected the token or the request. Create a token that has access to Storage Boxes and set `HETZNER_STORAGE_TOKEN`. The rest of the dashboard keeps working. If you have no Storage Boxes, the section is simply hidden.
- **Warnings about a resource type (volumes, snapshots, ...)**: that lookup failed. The rest of the dashboard still works. Check the token's permissions.

## Not included

Storage Boxes are listed separately and are not part of the main total. Costs do not cover everything on a Hetzner invoice, such as other products outside Cloud (for example Robot dedicated servers or domains) or credits and discounts. Treat the total as an estimate and compare it with your invoice.
