# The agent

`hdash-agent` is an optional, read-only agent for Linux servers with systemd (x86_64 and ARM64). It shows what the Hetzner API can't see: memory, disks, failed services, pending reboots, available package updates, and Docker containers. It works on any Linux server, not only Hetzner.

The code is in `agent/`. It uses only Go's standard library.

## Who talks to whom

```
macOS app  --HTTPS/basic auth-->  dashboard backend  --HTTPS, pinned cert, signed requests-->  agent
                                        |
                                        +--SSH (only during install and changes, credentials you type)-->  server
```

- Clients (the macOS app, later others) only talk to the backend. They never hold an agent's key.
- The backend holds one key per agent and polls each agent every 60 seconds.
- The agent never connects out, except to Hetzner's metadata service (`169.254.169.254`) to learn its server ID.

## Install

Started from the app. The backend does the work over SSH:

1. **Host key.** `ssh-keyscan` fetches the server's host keys. You confirm a fingerprint in the app before any credentials are sent. ssh then runs with `StrictHostKeyChecking=yes` against a known_hosts file holding only that key. The key is saved, so later changes reuse it and fail if the server's key changes.
2. **Sign in.** Password, or private key with an optional passphrase. A non-root user needs sudo; the sudo password is the login password unless you give another. The credentials are written to a 0700 temp dir (a memory-only tmpfs in Docker) for that one operation and deleted afterwards. They are never stored or logged.
3. **Probe.** Reads architecture, OS, systemd, whether Docker is installed, and whether root access works. Changes nothing.
4. **Upload.** The agent binary that was built into the dashboard image is uploaded to a private temp dir on the server and its SHA-256 is checked. Nothing is downloaded from the internet on the server.
5. **Pair.** The backend generates a random 256-bit key and uploads it as a 0600 file. `hdash-agent install` (as root) reads and deletes it, then:
   - copies itself to `/usr/local/bin/hdash-agent` (root, 0755),
   - creates the system user `hdash` (no home, no login shell),
   - writes `/etc/hdash-agent/config.json` (root:hdash, 0640),
   - creates an ECDSA P-256 TLS certificate in `/var/lib/hdash-agent` (hdash, key 0600),
   - installs and starts `hdash-agent.service`,
   - prints the certificate on stdout.
6. **Pin.** The backend reads the certificate over the same SSH session and from then on trusts only that certificate for this agent. Because it arrives over SSH, there is no trust-on-first-use window.

Install options: agent port (default 7411), listen address (for example a Tailscale or private IP), the address the dashboard connects to, an allow-list of source IPs/CIDRs, and Docker monitoring (only offered when Docker is installed).

## Docker opt-in

Reading the Docker socket needs the `docker` group, and membership of that group is close to root. So Docker monitoring is off unless you turn it on:

- At install, the app offers it only when Docker is found.
- The agent keeps checking for the Docker socket. If Docker is installed later, the agent reports `docker.installed: true, enabled: false` and the app suggests turning it on.
- Turning it on or off is a separate step with SSH credentials. It sets `SupplementaryGroups=docker` in the unit and restarts the agent. The agent never changes its own rights.

The Docker client in the agent can only send GET requests. Environment variables, commands, labels and mounts of containers are not reported, since they often contain secrets.

## Updates

The agent reports available package updates without changing anything:

- Debian/Ubuntu: `apt-get -s dist-upgrade` (a simulation, works without root). Packages from a `-security` suite are counted as security updates.
- RHEL family: `dnf --cacheonly check-update` and `dnf --cacheonly updateinfo list --security`.

It never refreshes package lists. It reads what the system's own timers (`apt-daily`, `dnf-makecache`) stored, and reports when the lists were last refreshed. Checked every 30 minutes. The alert metrics are `securityUpdates` and `updates`.

## Security model

- **Read-only.** Every route is GET. There is no route that runs a command or changes anything. `install` and `uninstall` are CLI subcommands that need root and are only run by the backend over SSH.
- **One key per agent.** Each pairing has its own random key, stored in the backend encrypted with AES-256-GCM under `DATA_KEY` (bound to the agent's ID). `rotate` creates a new key and certificate; the old ones stop working right away. Re-installing on the same host replaces the old pairing.
- **The key never travels.** Each request carries a timestamp, a random nonce and `HMAC-SHA256(key, "hdash-agent-v1\n" + method + "\n" + requestURI + "\n" + timestamp + "\n" + nonce)`. The agent accepts timestamps within 60 seconds and each nonce once, so a captured request can't be replayed or moved to another path.
- **TLS 1.3 only**, with the certificate pinned by the backend (it is the only CA trusted and its SHA-256 fingerprint must match).
- **No unauthenticated surface.** Every route needs a valid signature. A wrong request gets the same `401` whatever the reason, after a delay. Five failures from one IP within a minute block that IP for 5 minutes. Optionally, only listed source addresses may connect at all.
- **Sandboxed service.** Runs as `hdash` with `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, no capabilities, a system call filter, and only `AF_UNIX/INET/INET6` sockets. `systemd-analyze security hdash-agent` rates it about 1.5 ("OK").
- **Backend.** Agent management and threshold changes require `DASHBOARD_PASSWORD`, a custom header and a JSON body (which blocks cross-site requests from a browser). Values that go into remote commands are validated and shell-quoted. The app sends SSH credentials only over HTTPS or to a backend on the same Mac.

What it does not protect against: someone who already has root on the server, or who has both the data volume and `DATA_KEY`.

## Firewall

The backend must reach the agent's port.

| Address | What to open |
|---|---|
| Public IP | Hetzner Cloud Firewall: inbound TCP 7411 with the dashboard's IP as source. With ufw: `sudo ufw allow from <dashboard-ip> to any port 7411 proto tcp` |
| Tailscale / VPN / private network | Nothing public. Set the listen address to that IP at install. |

## Protocol

All routes need the three headers `X-Hdash-Timestamp`, `X-Hdash-Nonce` (32 to 64 lowercase hex characters) and `X-Hdash-Signature` (lowercase hex HMAC).

- `GET /v1/snapshot`: the current state, collected every 60 seconds: `host` (hostname, OS, kernel, arch, Hetzner server ID, boot time, cores, load), `cpu` and `cpu5` (percent, last minute and 5-minute average), `memory`, `disks` (block-device file systems with inodes), `network` (bytes per second), `system` (`rebootRequired`, `failedUnits`), `updates` (`manager`, `total`, `security`, `packages`, `listsTime`, `checkedAt`, `error`) or `null` for an unsupported package manager, and `docker` (`null` without Docker, otherwise `installed`, `enabled`, `error`, `containers`).
- `GET /v1/history?since=<unix seconds>`: samples of `cpu` (percent), `memory`, `swap`, `disk` (used fractions, `disk` is the fullest file system) and `load` (per core). One-minute samples for the last 24 hours, five-minute averages for 7 days. Kept in memory and saved to `/var/lib/hdash-agent/history.json` every 10 minutes.

## Remove

From the app: **Manage → Uninstall agent**. Or on the server: `sudo hdash-agent uninstall`. This removes the service, binary, user, `/etc/hdash-agent` and `/var/lib/hdash-agent`. Remove the firewall rule yourself.
