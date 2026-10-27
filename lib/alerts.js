// Alert thresholds and evaluation. Every metric has a warning and a critical
// level; a value at or above a level raises an alert. Defaults apply to every
// server, and each server can override any metric.
const store = require("./store");

// unit is for display; max bounds what a client may set.
const METRICS = {
  serverStopped: { label: "Server not running", unit: "bool", warn: null, crit: 1, max: 1 },
  agentOffline: { label: "Agent unreachable", unit: "min", warn: 3, crit: 10, max: 10080 },
  cpu: { label: "CPU (5 min avg)", unit: "%", warn: 80, crit: 95, max: 100 },
  load: { label: "Load per core", unit: "x", warn: 1.5, crit: 3, max: 100 },
  memory: { label: "Memory used", unit: "%", warn: 85, crit: 95, max: 100 },
  swap: { label: "Swap used", unit: "%", warn: 50, crit: 80, max: 100 },
  disk: { label: "Disk used", unit: "%", warn: 80, crit: 90, max: 100 },
  inodes: { label: "Inodes used", unit: "%", warn: 80, crit: 90, max: 100 },
  failedUnits: { label: "Failed systemd units", unit: "count", warn: 1, crit: 3, max: 1000 },
  rebootRequired: { label: "Reboot required", unit: "bool", warn: 1, crit: null, max: 1 },
  securityUpdates: { label: "Security updates", unit: "count", warn: 1, crit: 10, max: 10000 },
  updates: { label: "Updates available", unit: "count", warn: 25, crit: null, max: 10000 },
  containersDown: { label: "Containers unhealthy or stopped", unit: "count", warn: 1, crit: 3, max: 1000 },
  traffic: { label: "Included traffic used", unit: "%", warn: 80, crit: 95, max: 1000 },
};

const builtin = () =>
  Object.fromEntries(Object.entries(METRICS).map(([k, m]) => [k, { enabled: true, warn: m.warn, crit: m.crit }]));

function thresholds() {
  const saved = store.readThresholds();
  const defaults = builtin();
  for (const [k, v] of Object.entries(saved.defaults || {})) if (defaults[k]) defaults[k] = { ...defaults[k], ...v };
  return { metrics: METRICS, defaults, servers: saved.servers || {} };
}

function effective(t, key) {
  const out = {};
  for (const k of Object.keys(METRICS)) out[k] = { ...t.defaults[k], ...(t.servers[key]?.[k] || {}) };
  return out;
}

const bad = (msg) => Object.assign(new Error(msg), { status: 400 });

function cleanLevels(name, v) {
  const m = METRICS[name];
  if (!m) throw bad(`unknown metric ${name}`);
  if (typeof v !== "object" || v === null) throw bad(`${name}: expected an object`);
  const out = {};
  if ("enabled" in v) {
    if (typeof v.enabled !== "boolean") throw bad(`${name}.enabled must be true or false`);
    out.enabled = v.enabled;
  }
  for (const lvl of ["warn", "crit"]) {
    if (!(lvl in v)) continue;
    if (v[lvl] === null) out[lvl] = null;
    else if (typeof v[lvl] === "number" && Number.isFinite(v[lvl]) && v[lvl] >= 0 && v[lvl] <= m.max) out[lvl] = v[lvl];
    else throw bad(`${name}.${lvl} must be a number from 0 to ${m.max}, or null`);
  }
  return out;
}

// Validates and stores a full thresholds document from a client.
function save(body) {
  const defaults = {};
  for (const [k, v] of Object.entries(body.defaults || {})) defaults[k] = cleanLevels(k, v);
  const servers = {};
  for (const [key, metrics] of Object.entries(body.servers || {})) {
    if (!/^(hcloud|agent):[A-Za-z0-9_-]{1,64}$/.test(key)) throw bad(`bad server key ${key}`);
    const m = {};
    for (const [k, v] of Object.entries(metrics || {})) m[k] = cleanLevels(k, v);
    if (Object.keys(m).length) servers[key] = m;
  }
  const merged = { ...builtin() };
  for (const [k, v] of Object.entries(defaults)) merged[k] = { ...merged[k], ...v };
  const t = { defaults: merged, servers };
  for (const key of ["", ...Object.keys(servers)]) {
    for (const [k, v] of Object.entries(effective(t, key))) {
      if (v.warn != null && v.crit != null && v.warn > v.crit) {
        throw bad(`${key ? key + " " : ""}${k}: the warning level must not be above the critical level`);
      }
    }
  }
  store.writeThresholds({ defaults, servers });
  return thresholds();
}

const pct = (used, total) => (total > 0 ? Math.round((used / total) * 1000) / 10 : 0);

// Collects the current value of each metric for one server, with an optional
// subject (for example the mount point that is fullest).
function measure({ hetzner, agent, status, createdAt }) {
  const v = {};
  if (hetzner) {
    v.serverStopped = { value: hetzner.status === "running" ? 0 : 1, detail: hetzner.status };
    const inc = hetzner.traffic?.included;
    if (inc) v.traffic = { value: pct(hetzner.traffic.out || 0, inc) };
  }
  if (!agent) return v;
  const lastOk = status?.lastOk || null;
  if (!status?.ok) {
    v.agentOffline = { value: Math.floor((Date.now() - (lastOk || createdAt)) / 60000), detail: status?.error || "not polled yet" };
  } else {
    v.agentOffline = { value: 0 };
  }
  const s = status?.snapshot;
  if (!s) return v;
  v.cpu = { value: s.cpu5 ?? s.cpu };
  if (s.host?.cores) v.load = { value: Math.round((s.host.load[1] / s.host.cores) * 100) / 100 };
  if (s.memory?.total) v.memory = { value: pct(s.memory.total - s.memory.available, s.memory.total) };
  if (s.memory?.swapTotal) v.swap = { value: pct(s.memory.swapTotal - s.memory.swapFree, s.memory.swapTotal) };
  let disk = null;
  let inode = null;
  for (const d of s.disks || []) {
    const p = pct(d.used, d.total);
    if (!disk || p > disk.value) disk = { value: p, detail: d.mount };
    if (d.inodesTotal) {
      const ip = pct(d.inodesUsed, d.inodesTotal);
      if (!inode || ip > inode.value) inode = { value: ip, detail: d.mount };
    }
  }
  if (disk) v.disk = disk;
  if (inode) v.inodes = inode;
  v.failedUnits = { value: s.system?.failedUnits?.length || 0, detail: (s.system?.failedUnits || []).join(", ") };
  v.rebootRequired = { value: s.system?.rebootRequired ? 1 : 0 };
  if (s.updates && !s.updates.error) {
    v.securityUpdates = { value: s.updates.security };
    v.updates = { value: s.updates.total };
  }
  if (s.docker?.enabled && !s.docker.error) {
    const down = s.docker.containers.filter(
      (c) => c.health === "unhealthy" || c.state === "restarting" || c.state === "dead" || (c.state === "exited" && c.exitCode !== 0)
    );
    v.containersDown = { value: down.length, detail: down.map((c) => c.name).join(", ") };
  }
  return v;
}

function evaluate(key, inputs, t = thresholds()) {
  const levels = effective(t, key);
  const values = measure(inputs);
  const alerts = [];
  for (const [metric, { value, detail }] of Object.entries(values)) {
    const lv = levels[metric];
    if (!lv?.enabled) continue;
    let level = null;
    let threshold = null;
    if (lv.crit != null && value >= lv.crit) [level, threshold] = ["critical", lv.crit];
    else if (lv.warn != null && value >= lv.warn) [level, threshold] = ["warning", lv.warn];
    if (level) alerts.push({ metric, label: METRICS[metric].label, unit: METRICS[metric].unit, level, value, threshold, detail: detail || null });
  }
  const level = alerts.some((a) => a.level === "critical") ? "critical" : alerts.length ? "warning" : "ok";
  return { level, alerts, values };
}

module.exports = { METRICS, thresholds, save, evaluate };
