// Hetzner Cloud dashboard backend: keeps the API token server-side and
// exposes a small read-only JSON API plus the static UI. No dependencies.
const http = require("node:http");
const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const store = require("./lib/store");
const agents = require("./lib/agents");
const ssh = require("./lib/ssh");
const alerts = require("./lib/alerts");

const PORT = Number(process.env.PORT || 3000);
const PASSWORD = process.env.DASHBOARD_PASSWORD || "";
const API = "https://api.hetzner.cloud/v1";
// Storage Boxes are served from a different host than Cloud servers.
const STORAGE_API = "https://api.hetzner.com/v1";
const CACHE_MS = 30_000;
// "gross" (incl. VAT) or "net" (excl. VAT)
const BASIS = process.env.PRICE_BASIS === "net" ? "net" : "gross";
const PRICES_FILE = process.env.PRICES_FILE || "/config/prices.json";

// One project per token: HCLOUD_TOKEN (single project) and/or HCLOUD_TOKEN_<NAME> (one per project).
// HETZNER_STORAGE_TOKEN[_<NAME>] optionally overrides the token used for Storage Boxes.
const projects = [];
for (const [k, v] of Object.entries(process.env)) {
  const m = /^HCLOUD_TOKEN(?:_(.+))?$/.exec(k);
  if (!m || !v.trim()) continue;
  const suffix = m[1];
  projects.push({
    name: suffix ? suffix.toLowerCase().replace(/_/g, " ") : "default",
    token: v.trim(),
    storageToken: (process.env[`HETZNER_STORAGE_TOKEN${suffix ? "_" + suffix : ""}`] || "").trim() || v.trim(),
  });
}
if (!projects.length) {
  console.error("No token set. Define HCLOUD_TOKEN or HCLOUD_TOKEN_<PROJECT> in .env");
  process.exit(1);
}
console.log(`Projects: ${projects.map((p) => p.name).join(", ")}`);
const projectByName = (n) => projects.find((p) => p.name === n) || (projects.length === 1 ? projects[0] : null);

const cache = new Map();

async function hcloud(pathAndQuery, base, token) {
  const key = token.slice(-8) + base + pathAndQuery;
  const hit = cache.get(key);
  if (hit && Date.now() - hit.at < CACHE_MS) return hit.data;
  const res = await fetch(base + pathAndQuery, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = new Error(body?.error?.message || `Hetzner API ${res.status}`);
    err.status = res.status;
    throw err;
  }
  cache.set(key, { at: Date.now(), data: body });
  return body;
}

async function paged(endpoint, key, query, base, token) {
  const items = [];
  for (let page = 1; page; ) {
    const body = await hcloud(`/${endpoint}?${query}per_page=50&page=${page}`, base, token);
    items.push(...body[key]);
    page = body.meta?.pagination?.next_page || 0;
  }
  return items;
}

const gross = (p) => (p ? Number(p.price_monthly?.[BASIS] ?? p[BASIS]) : null);
// Location of a resource: newer API versions expose `location` directly, older ones only via `datacenter`.
const locOf = (r) => r?.location || r?.home_location || r?.datacenter?.location || null;
// Price for an exact location. Returns null when that location has no entry, instead of
// quietly using another location's price.
const priceAt = (prices, loc) => gross((prices || []).find((x) => x.location === loc));
const round = (n) => Math.round(n * 100) / 100;

// The API only exposes current list prices. Servers created before a price
// change keep their old rate, so let the user override per server or type.
function loadOverrides() {
  try {
    const o = JSON.parse(fs.readFileSync(PRICES_FILE, "utf8"));
    return { byServer: o.byServer || {}, byType: o.byType || {} };
  } catch (e) {
    if (e.code !== "ENOENT") console.error(`Ignoring ${PRICES_FILE}: ${e.message}`);
    return { byServer: {}, byType: {} };
  }
}

async function storageBoxes(p) {
  const [boxes, types] = await Promise.all([
    paged("storage_boxes", "storage_boxes", "", STORAGE_API, p.storageToken),
    // the embedded type may not carry prices, so keep the full list as a fallback
    paged("storage_box_types", "storage_box_types", "", STORAGE_API, p.storageToken).catch(() => []),
  ]);
  return boxes.map((b) => {
    const t = b.storage_box_type || {};
    const full = types.find((x) => x.id === t.id || x.name === t.name) || {};
    const prices = t.prices || full.prices;
    const loc = locOf(b)?.name;
    let cost = priceAt(prices, loc);
    // Storage Box prices are often the same everywhere; accept that when no location entry matches.
    if (cost == null && prices?.length && new Set(prices.map(gross)).size === 1) cost = gross(prices[0]);
    const capacity = t.size ?? full.size ?? null;
    const used = b.stats?.size ?? null;
    return {
      id: b.id, name: b.name, status: b.status, type: t.name || full.name, location: loc,
      server: b.server, created: b.created, labels: b.labels || {},
      capacity, used,
      data: b.stats?.size_data ?? null, snapshots: b.stats?.size_snapshots ?? null,
      cost: cost == null ? null : round(cost),
    };
  });
}

async function overview(p) {
  const cloud = (endpoint, key, query = "") => paged(endpoint, key, query, API, p.token);
  const overrides = loadOverrides();
  const extra = (p) => p.catch((e) => ({ error: e.message }));
  const [servers, volumes, primaryIps, floatingIps, snapshots, lbs, pricing] = await Promise.all([
    cloud("servers", "servers"),
    extra(cloud("volumes", "volumes")),
    extra(cloud("primary_ips", "primary_ips")),
    extra(cloud("floating_ips", "floating_ips")),
    extra(cloud("images", "images", "type=snapshot&")),
    extra(cloud("load_balancers", "load_balancers")),
    extra(hcloud("/pricing", API, p.token)),
  ]);
  const warnings = [];
  let boxes = [];
  try {
    boxes = await storageBoxes(p);
  } catch (e) {
    warnings.push(`storage boxes: ${e.message} (set HETZNER_STORAGE_TOKEN if your Cloud token isn't valid for Storage Boxes)`);
  }
  const ok = (v, name) => {
    if (Array.isArray(v)) return v;
    warnings.push(`${name}: ${v.error}`);
    return [];
  };
  const P = pricing.error ? (warnings.push(`pricing: ${pricing.error}`), {}) : pricing.pricing;
  const volPerGb = Number(P.volume?.price_per_gb_month?.[BASIS] || 0);
  const snapPerGb = Number(P.image?.price_per_gb_month?.[BASIS] || 0);
  const backupPct = Number(P.server_backup?.percentage || 20);
  const perTb = Number(P.traffic?.price_per_tb?.[BASIS] || 0);
  const fipPrice = (f) => {
    const t = (P.floating_ips || []).find((x) => x.type === f.type);
    return priceAt(t?.prices, locOf(f)?.name) ?? Number(P.floating_ip?.price_monthly?.[BASIS] || 0);
  };
  const pipPrice = (ip) => {
    const t = (P.primary_ips || []).find((x) => x.type === ip.type);
    return priceAt(t?.prices, locOf(ip)?.name) ?? 0;
  };

  const vols = ok(volumes, "volumes").map((v) => ({
    id: v.id, name: v.name, sizeGb: v.size, server: v.server, location: v.location?.name,
    cost: round(v.size * volPerGb),
  }));
  const pips = ok(primaryIps, "primary IPs").map((ip) => ({
    id: ip.id, name: ip.name, ip: ip.ip, type: ip.type, server: ip.assignee_id,
    location: locOf(ip)?.name, cost: round(pipPrice(ip)),
  }));
  const fips = ok(floatingIps, "floating IPs").map((f) => ({
    id: f.id, name: f.name || f.description, ip: f.ip, type: f.type, server: f.server,
    location: locOf(f)?.name, cost: round(fipPrice(f)),
  }));
  const snaps = ok(snapshots, "snapshots").map((i) => ({
    id: i.id, name: i.description || `snapshot ${i.id}`, sizeGb: i.image_size,
    from: i.created_from?.name, created: i.created,
    cost: round((i.image_size || 0) * snapPerGb),
  }));
  const lbList = ok(lbs, "load balancers").map((l) => ({
    id: l.id, name: l.name, type: l.load_balancer_type?.name, targets: l.targets?.length || 0,
    location: locOf(l)?.name, cost: round(priceAt(l.load_balancer_type?.prices, locOf(l)?.name) || 0),
  }));

  const rows = servers.map((s) => {
    const where = locOf(s);
    const listAt = priceAt(s.server_type?.prices, where?.name);
    if (listAt == null) warnings.push(`${s.name}: no ${s.server_type?.name} price for location "${where?.name}"; set an override in prices.json`);
    const list = listAt || 0;
    const ov = overrides.byServer[s.name] ?? overrides.byServer[s.id] ?? overrides.byType[s.server_type?.name];
    const base = ov != null ? Number(ov) : list;
    const backups = !!s.backup_window;
    const backupCost = backups ? base * backupPct / 100 : 0;
    const ipCost = pips.filter((p) => p.server === s.id).reduce((n, p) => n + p.cost, 0);
    const volCost = vols.filter((v) => v.server === s.id).reduce((n, v) => n + v.cost, 0);
    const overTb = Math.max(0, (s.outgoing_traffic || 0) - (s.included_traffic || 0)) / 1e12;
    const overage = overTb * perTb;
    return {
      id: s.id, name: s.name, status: s.status, created: s.created,
      type: s.server_type?.name, cores: s.server_type?.cores, memory: s.server_type?.memory,
      disk: s.server_type?.disk, location: where?.city, locationName: where?.name,
      country: where?.country,
      ipv4: s.public_net?.ipv4?.ip || null, ipv6: s.public_net?.ipv6?.ip || null,
      image: s.image?.description || s.image?.name || null, labels: s.labels || {},
      backups, backupWindow: s.backup_window || null,
      priceSource: ov != null ? "override" : "list", listPrice: round(list),
      traffic: { included: s.included_traffic, out: s.outgoing_traffic, in: s.ingoing_traffic },
      cost: {
        server: round(base), backups: round(backupCost), ips: round(ipCost),
        volumes: round(volCost), overage: round(overage),
        total: round(base + backupCost + ipCost + volCost + overage),
      },
    };
  });

  const sum = (a) => round(a.reduce((n, x) => n + x.cost, 0));
  const attachedVols = vols.filter((v) => v.server);
  const sumK = (k) => round(rows.reduce((n, r) => n + r.cost[k], 0));
  const unusedIps = pips.filter((p) => !p.server && p.type === "ipv4").concat(fips.filter((f) => !f.server));
  const breakdown = {
    servers: sumK("server"), backups: sumK("backups"), primaryIps: sumK("ips"),
    attachedVolumes: sumK("volumes"), overage: sumK("overage"),
    floatingIps: sum(fips), snapshots: sum(snaps), loadBalancers: sum(lbList),
    unattachedVolumes: sum(vols.filter((v) => !v.server)),
    unassignedPrimaryIps: sum(pips.filter((p) => !p.server)),
  };
  return {
    servers: rows,
    storageBoxes: {
      items: boxes,
      total: round(boxes.reduce((n, b) => n + (b.cost || 0), 0)),
    },
    resources: { volumes: vols, primaryIps: pips, floatingIps: fips, snapshots: snaps, loadBalancers: lbList },
    totals: {
      count: rows.length,
      running: rows.filter((r) => r.status === "running").length,
      backupsEnabled: rows.filter((r) => r.backups).length,
      noBackups: rows.filter((r) => !r.backups).map((r) => r.name),
      monthlyCost: round(Object.values(breakdown).reduce((n, x) => n + x, 0)),
      breakdown,
      // things you pay for that nothing is using
      idle: {
        volumes: vols.filter((v) => !v.server).length,
        ips: unusedIps.length,
      },
      currency: pricing.pricing?.currency || "EUR",
      basis: BASIS,
    },
    warnings,
  };
}

const sumObj = (list, pick) => {
  const out = {};
  for (const o of list) for (const [k, v] of Object.entries(pick(o))) out[k] = round((out[k] || 0) + v);
  return out;
};

async function allProjects() {
  const results = await Promise.allSettled(projects.map((p) => overview(p)));
  const ok = [];
  const warnings = [];
  results.forEach((r, i) => {
    if (r.status === "fulfilled") ok.push({ name: projects[i].name, data: r.value });
    else warnings.push(`${projects[i].name}: ${r.reason.message}`);
  });
  if (!ok.length) throw results[0].reason;
  const multi = projects.length > 1;
  const tag = (arr, name) => arr.map((x) => ({ ...x, project: name }));
  const resources = {};
  for (const key of ["volumes", "primaryIps", "floatingIps", "snapshots", "loadBalancers"]) {
    resources[key] = ok.flatMap((o) => tag(o.data.resources[key], o.name));
  }
  const items = ok.flatMap((o) => tag(o.data.storageBoxes.items, o.name));
  const T = ok.map((o) => o.data.totals);
  return {
    multi,
    projects: ok.map((o) => ({
      name: o.name, servers: o.data.totals.count, monthlyCost: o.data.totals.monthlyCost,
      storageBoxes: o.data.storageBoxes.items.length, storageCost: o.data.storageBoxes.total,
    })),
    servers: ok.flatMap((o) => tag(o.data.servers, o.name)),
    resources,
    storageBoxes: { items, total: round(items.reduce((n, b) => n + (b.cost || 0), 0)) },
    totals: {
      count: T.reduce((n, t) => n + t.count, 0),
      running: T.reduce((n, t) => n + t.running, 0),
      backupsEnabled: T.reduce((n, t) => n + t.backupsEnabled, 0),
      noBackups: ok.flatMap((o) => o.data.totals.noBackups.map((n) => (multi ? `${o.name}/${n}` : n))),
      monthlyCost: round(T.reduce((n, t) => n + t.monthlyCost, 0)),
      breakdown: sumObj(T, (t) => t.breakdown),
      idle: sumObj(T, (t) => t.idle),
      currency: T[0].currency,
      basis: BASIS,
    },
    warnings: [...warnings, ...ok.flatMap((o) => (multi ? o.data.warnings.map((w) => `${o.name}: ${w}`) : o.data.warnings))],
  };
}

function projectFrom(q) {
  const p = projectByName(q.get("project") || "");
  if (!p) throw Object.assign(new Error("unknown project; use ?project=" + projects.map((x) => x.name).join("|")), { status: 400 });
  return p;
}


// ---- Agents and alerts ----

const AGENT_PORT = 7411;
const bad = (msg, status = 400) => Object.assign(new Error(msg), { status });

// What clients may see of an agent. The secret, certificate and known_hosts
// stay in the backend.
function publicAgent(a, st) {
  return {
    id: a.id, name: a.name, sshHost: a.sshHost, sshPort: a.sshPort, sshUser: a.sshUser,
    hostKeyFingerprint: a.hostKeyFingerprint, agentHost: a.agentHost, agentPort: a.agentPort,
    listen: a.listen, allowFrom: a.allowFrom, docker: a.docker, fingerprint: a.fingerprint,
    version: a.version, arch: a.arch, os: a.os, hetznerId: a.hetznerId, createdAt: a.createdAt,
    status: !st ? "pending" : st.ok ? "online" : "offline",
    error: st?.ok ? null : st?.error || null,
    lastOk: st?.lastOk || null,
    snapshot: st?.snapshot || null,
  };
}

async function monitor() {
  const warnings = [];
  let data = null;
  try {
    data = await allProjects();
    warnings.push(...data.warnings);
  } catch (e) {
    warnings.push(`hetzner: ${e.message}`);
  }
  const t = alerts.thresholds();
  const list = store.listAgents();
  const byServer = new Map();
  for (const a of list) {
    const sid = agents.statusOf(a.id)?.snapshot?.host?.serverID ?? a.hetznerId;
    if (sid) byServer.set(Number(sid), a);
  }
  const linked = new Set();
  const entry = (key, h, a) => {
    const st = a ? agents.statusOf(a.id) : null;
    const ev = alerts.evaluate(key, { hetzner: h, agent: a, status: st, createdAt: a?.createdAt }, t);
    return {
      key, name: h?.name || a.name, project: h?.project || null,
      hetzner: h, agent: a ? publicAgent(a, st) : null,
      level: ev.level, alerts: ev.alerts,
    };
  };
  const servers = [];
  for (const h of data?.servers || []) {
    const a = byServer.get(h.id) || null;
    if (a) linked.add(a.id);
    servers.push(entry(`hcloud:${h.id}`, h, a));
  }
  for (const a of list) if (!linked.has(a.id)) servers.push(entry(`agent:${a.id}`, null, a));
  return { servers, warnings, time: Date.now() };
}

// One SSH operation per host at a time.
const busy = new Set();
async function exclusive(host, fn) {
  if (busy.has(host)) throw bad("another operation is already running on this server", 409);
  busy.add(host);
  try {
    return await fn();
  } finally {
    busy.delete(host);
  }
}

function sshFrom(b) {
  const c = b?.ssh || {};
  return {
    host: c.host, port: c.port, user: c.user, hostKeyFingerprint: c.hostKeyFingerprint,
    auth: c.auth, sudoPassword: c.sudoPassword,
  };
}

const firewallHint = (a) =>
  `The dashboard connects to ${a.agentHost}:${a.agentPort} (TCP). Allow that port only from the dashboard's address: ` +
  `in a Hetzner Cloud Firewall add an inbound TCP rule for port ${a.agentPort} with the dashboard's IP as source; ` +
  `with ufw run: sudo ufw allow from <dashboard-ip> to any port ${a.agentPort} proto tcp. ` +
  `Over Tailscale or a private network no public rule is needed.`;

function agentOr404(id) {
  const a = store.getAgent(String(id || ""));
  if (!a) throw bad("unknown agent", 404);
  return a;
}

const actions = {
  // Step 1: the user checks this fingerprint before sending any credentials.
  async "POST /api/agents/hostkeys"(b) {
    return { keys: await ssh.scanHostKeys(b.host, b.port) };
  },
  // Step 2: log in and look at the server without changing anything.
  async "POST /api/agents/probe"(b) {
    const conn = sshFrom(b);
    return exclusive(String(conn.host), () => ssh.withSession(conn, (s) => ssh.probeWith(s)));
  },
  // Step 3: install and pair.
  async "POST /api/agents"(b) {
    store.dataKey();
    const conn = sshFrom(b);
    const o = b.options || {};
    const agentPort = ssh.checkPort(o.agentPort, AGENT_PORT);
    const listen = ssh.checkListen(o.listen);
    const allowFrom = ssh.checkIpList(o.allowFrom);
    const agentHost = ssh.checkHost(o.agentHost || listen || conn.host);
    const hetznerId = o.hetznerId ? Number(o.hetznerId) : null;
    if (hetznerId !== null && !Number.isInteger(hetznerId)) throw bad("invalid hetznerId");
    const name = String(o.name || conn.host || "").trim().slice(0, 64);
    const docker = o.docker === true;
    const id = crypto.randomBytes(8).toString("hex");
    const secret = crypto.randomBytes(32).toString("hex");

    return exclusive(String(conn.host), () =>
      ssh.withSession(conn, async (s) => {
        const p = await ssh.probeWith(s);
        if (!p.systemd) throw bad("this server does not run systemd");
        if (!p.arch) throw bad(`unsupported architecture ${p.machine}`);
        if (!p.rootOk) throw bad(`root access failed: ${p.rootError}`, 403);
        if (docker && !p.docker) throw bad("Docker is not installed on this server");
        const dir = await ssh.upload(s, p.arch, secret);
        const res = await ssh.runInstall(s, `${dir}/hdash-agent`,
          ssh.installFlags({ secret: true, dir, port: agentPort, listen, allowFrom, docker }), dir);
        // An earlier pairing with this host now has a dead key.
        for (const old of store.listAgents()) {
          if (old.sshHost === s.host && old.sshPort === s.port) {
            store.removeAgent(old.id);
            agents.forget(old.id);
          }
        }
        const rec = store.saveAgent({
          id, name, sshHost: s.host, sshPort: s.port, sshUser: s.user, knownHosts: s.knownHosts,
          hostKeyFingerprint: conn.hostKeyFingerprint, agentHost, agentPort, listen, allowFrom,
          docker: res.docker, cert: res.cert, fingerprint: new crypto.X509Certificate(res.cert).fingerprint256,
          version: res.version, arch: p.arch, os: p.os, hetznerId, createdAt: Date.now(),
        }, secret);
        await agents.pollSoon(rec, 3);
        return { agent: publicAgent(rec, agents.statusOf(id)), firewall: firewallHint(rec) };
      })
    );
  },
  // Changes on an installed agent. Each one needs SSH credentials again.
  async "POST /api/agents/action"(b) {
    const a = agentOr404(b.id);
    const conn = {
      host: a.sshHost, port: a.sshPort, user: a.sshUser, knownHosts: a.knownHosts,
      auth: b.ssh?.auth, sudoPassword: b.ssh?.sudoPassword,
    };
    const action = String(b.action || "");
    if (!["upgrade", "docker-on", "docker-off", "rotate", "uninstall"].includes(action)) throw bad("unknown action");
    return exclusive(a.sshHost, () =>
      ssh.withSession(conn, async (s) => {
        if (action === "uninstall") {
          const r = await s.asRoot(`${ssh.REMOTE_BIN} uninstall`);
          if (r.code !== 0) throw bad(`uninstall failed: ${r.stderr.trim().slice(0, 300)}`, 502);
          store.removeAgent(a.id);
          agents.forget(a.id);
          return { removed: true };
        }
        let res;
        let secret = null;
        if (action === "docker-on" || action === "docker-off") {
          res = await ssh.runInstall(s, ssh.REMOTE_BIN, ssh.installFlags({ docker: action === "docker-on" }));
        } else {
          const p = await ssh.probeWith(s);
          if (!p.rootOk) throw bad(`root access failed: ${p.rootError}`, 403);
          if (action === "rotate") secret = crypto.randomBytes(32).toString("hex");
          const dir = await ssh.upload(s, p.arch, secret);
          res = await ssh.runInstall(s, `${dir}/hdash-agent`,
            ssh.installFlags(secret ? { secret: true, dir, rotateCert: true } : {}), dir);
        }
        const rec = store.saveAgent({
          ...a, docker: res.docker, version: res.version, cert: res.cert,
          fingerprint: new crypto.X509Certificate(res.cert).fingerprint256,
        }, secret);
        await agents.pollSoon(rec);
        return { agent: publicAgent(rec, agents.statusOf(rec.id)) };
      })
    );
  },
  // Rename, or reach the agent on another address (for example a Tailscale IP).
  async "POST /api/agents/update"(b) {
    const a = agentOr404(b.id);
    const next = { ...a };
    if (b.name !== undefined) next.name = String(b.name).trim().slice(0, 64) || a.name;
    if (b.agentHost !== undefined) next.agentHost = ssh.checkHost(b.agentHost);
    const rec = store.saveAgent(next);
    await agents.pollOne(rec);
    return { agent: publicAgent(rec, agents.statusOf(rec.id)) };
  },
  // Forget an agent without touching the server (for a server that is gone).
  async "DELETE /api/agents"(_b, q) {
    const a = agentOr404(q.get("id"));
    store.removeAgent(a.id);
    agents.forget(a.id);
    return { removed: true };
  },
  async "PUT /api/thresholds"(b) {
    return alerts.save(b);
  },
};

const routes = {
  async "/api/monitor"() {
    return monitor();
  },
  async "/api/thresholds"() {
    return alerts.thresholds();
  },
  async "/api/agents/history"(q) {
    const a = agentOr404(q.get("id"));
    const hours = Math.min(Math.max(Number(q.get("hours")) || 24, 1), 24 * 7);
    const since = Math.floor(Date.now() / 1000) - hours * 3600;
    return agents.agentGet(a, `/v1/history?since=${since}`);
  },
  async "/api/servers"() {
    return allProjects();
  },
  // Raw server object, to check whether the API exposes anything price-related for it
  async "/api/debug/server"(q) {
    const id = String(q.get("id") || "");
    if (!/^\d+$/.test(id)) throw Object.assign(new Error("bad id"), { status: 400 });
    return hcloud(`/servers/${id}`, API, projectFrom(q).token);
  },
  // Raw Storage Box API response, to see what Hetzner returns for this token
  async "/api/debug/storage-boxes"(q) {
    const p = projectFrom(q);
    const res = await fetch(`${STORAGE_API}/storage_boxes`, { headers: { Authorization: `Bearer ${p.storageToken}` } });
    const text = await res.text();
    let body;
    try { body = JSON.parse(text); } catch { body = text.slice(0, 500); }
    return { project: p.name, url: `${STORAGE_API}/storage_boxes`, status: res.status, body };
  },
  async "/api/metrics"(q) {
    const id = String(q.get("id") || "");
    if (!/^\d+$/.test(id)) throw Object.assign(new Error("bad id"), { status: 400 });
    const hours = Math.min(Math.max(Number(q.get("hours")) || 24, 1), 24 * 30);
    const end = new Date();
    const start = new Date(end - hours * 3600_000);
    // ~120 points regardless of range
    const step = Math.max(Math.ceil((hours * 3600) / 120), 10);
    const body = await hcloud(
      `/servers/${id}/metrics?type=cpu,disk,network&start=${start.toISOString()}&end=${end.toISOString()}&step=${step}`,
      API,
      projectFrom(q).token
    );
    const series = {};
    for (const [name, v] of Object.entries(body.metrics.time_series)) {
      series[name] = v.values.map(([t, val]) => [t * 1000, Number(val)]);
    }
    return { start, end, step, series };
  },
};

function authorized(req) {
  if (!PASSWORD) return true;
  const m = /^Basic (.+)$/.exec(req.headers.authorization || "");
  if (!m) return false;
  const given = Buffer.from(m[1], "base64").toString().split(":").slice(1).join(":");
  const a = crypto.createHash("sha256").update(given).digest();
  const b = crypto.createHash("sha256").update(PASSWORD).digest();
  return crypto.timingSafeEqual(a, b);
}

const MAX_BODY = 512 * 1024;
function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (c) => {
      size += c.length;
      if (size > MAX_BODY) {
        reject(bad("request body too large", 413));
        req.destroy();
      } else chunks.push(c);
    });
    req.on("end", () => {
      if (!chunks.length) return resolve({});
      try {
        const v = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        resolve(v && typeof v === "object" ? v : {});
      } catch {
        reject(bad("invalid JSON"));
      }
    });
    req.on("error", reject);
  });
}

// Changes need a password to be configured, and a custom header plus a JSON
// body, which a browser will not send cross-site without a CORS preflight
// (which this server never approves).
function adminCheck(req) {
  if (!PASSWORD) throw bad("set DASHBOARD_PASSWORD to enable agent management and threshold changes", 403);
  if (req.headers["x-hdash-client"] !== "1" || !/^application\/json\b/.test(req.headers["content-type"] || "")) {
    throw bad("missing X-Hdash-Client header or JSON content type", 403);
  }
}

const MIME = { ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".css": "text/css" };

http
  .createServer(async (req, res) => {
    if (req.url === "/healthz") return void res.end("ok");
    if (!authorized(req)) {
      res.writeHead(401, { "WWW-Authenticate": 'Basic realm="Hetzner dashboard"' });
      return void res.end("Unauthorized");
    }
    const url = new URL(req.url, "http://x");
    const action = req.method !== "GET" && req.method !== "HEAD" ? actions[`${req.method} ${url.pathname}`] : null;
    const handler = req.method === "GET" || req.method === "HEAD" ? routes[url.pathname] : action && ((q) => {
      adminCheck(req);
      return readBody(req).then((b) => action(b, q));
    });
    if (!handler && req.method !== "GET" && req.method !== "HEAD") {
      res.writeHead(url.pathname.startsWith("/api/") ? 404 : 405, { "Content-Type": "application/json" });
      return void res.end(JSON.stringify({ error: "not found" }));
    }
    if (handler) {
      try {
        const data = await handler(url.searchParams);
        res.writeHead(200, { "Content-Type": "application/json", "Cache-Control": "no-store" });
        res.end(JSON.stringify(data));
      } catch (e) {
        res.writeHead(e.status || 502, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: e.message }));
      }
      return;
    }
    const file = path.join(__dirname, "public", url.pathname === "/" ? "index.html" : url.pathname);
    if (!file.startsWith(path.join(__dirname, "public")) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) {
      res.writeHead(404);
      return void res.end("Not found");
    }
    res.writeHead(200, { "Content-Type": MIME[path.extname(file)] || "application/octet-stream" });
    fs.createReadStream(file).pipe(res);
  })
  .listen(PORT, () => console.log(`Dashboard listening on :${PORT}`));

agents.startPolling();
