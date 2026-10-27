// Persistent state in DATA_DIR: paired agents and alert thresholds.
// Agent secrets are encrypted with AES-256-GCM under DATA_KEY, so a copy of
// the data volume alone does not let anyone talk to the agents.
const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");

const DATA_DIR = process.env.DATA_DIR || "/data";
const AGENTS_FILE = path.join(DATA_DIR, "agents.json");
const THRESHOLDS_FILE = path.join(DATA_DIR, "thresholds.json");

function dataKey() {
  const hex = (process.env.DATA_KEY || "").trim();
  if (!/^[0-9a-fA-F]{64}$/.test(hex)) {
    throw Object.assign(new Error("DATA_KEY is not set. Generate one with `openssl rand -hex 32` and add it to .env"), { status: 503 });
  }
  return Buffer.from(hex, "hex");
}

function encrypt(plain, aad) {
  const iv = crypto.randomBytes(12);
  const c = crypto.createCipheriv("aes-256-gcm", dataKey(), iv);
  c.setAAD(Buffer.from(aad));
  const data = Buffer.concat([c.update(plain, "utf8"), c.final()]);
  return { iv: iv.toString("base64"), tag: c.getAuthTag().toString("base64"), data: data.toString("base64") };
}

function decrypt(box, aad) {
  const d = crypto.createDecipheriv("aes-256-gcm", dataKey(), Buffer.from(box.iv, "base64"));
  d.setAAD(Buffer.from(aad));
  d.setAuthTag(Buffer.from(box.tag, "base64"));
  return Buffer.concat([d.update(Buffer.from(box.data, "base64")), d.final()]).toString("utf8");
}

function readJSON(file, fallback) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch (e) {
    if (e.code !== "ENOENT") console.error(`Ignoring ${file}: ${e.message}`);
    return fallback;
  }
}

function writeJSON(file, value) {
  fs.mkdirSync(DATA_DIR, { recursive: true, mode: 0o700 });
  const tmp = `${file}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(value, null, 2), { mode: 0o600 });
  fs.renameSync(tmp, file);
}

let agents = readJSON(AGENTS_FILE, { agents: [] }).agents;

const listAgents = () => agents;
const getAgent = (id) => agents.find((a) => a.id === id) || null;

function saveAgent(record, secret) {
  const rec = { ...record };
  if (secret) rec.secret = encrypt(secret, rec.id);
  agents = agents.filter((a) => a.id !== rec.id).concat(rec);
  writeJSON(AGENTS_FILE, { agents });
  return rec;
}

function removeAgent(id) {
  agents = agents.filter((a) => a.id !== id);
  writeJSON(AGENTS_FILE, { agents });
}

const agentSecret = (a) => Buffer.from(decrypt(a.secret, a.id), "hex");

const readThresholds = () => readJSON(THRESHOLDS_FILE, { defaults: {}, servers: {} });
const writeThresholds = (t) => writeJSON(THRESHOLDS_FILE, t);

module.exports = { dataKey, listAgents, getAgent, saveAgent, removeAgent, agentSecret, readThresholds, writeThresholds };
