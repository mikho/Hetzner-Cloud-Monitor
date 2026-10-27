// Talks to paired agents over HTTPS. The agent's certificate was received over
// SSH during install and is the only certificate trusted for that agent. Every
// request is signed with the agent's own key; the key never leaves this process.
const https = require("node:https");
const crypto = require("node:crypto");
const store = require("./store");

const POLL_MS = 60_000;
const MAX_BODY = 8 << 20;

// Latest poll result per agent id: { at, ok, snapshot, error, lastOk }
const state = new Map();

function sign(secret, method, uri) {
  const ts = String(Math.floor(Date.now() / 1000));
  const nonce = crypto.randomBytes(16).toString("hex");
  const sig = crypto.createHmac("sha256", secret).update(`hdash-agent-v1\n${method}\n${uri}\n${ts}\n${nonce}`).digest("hex");
  return { "X-Hdash-Timestamp": ts, "X-Hdash-Nonce": nonce, "X-Hdash-Signature": sig };
}

function agentGet(a, uri) {
  return new Promise((resolve, reject) => {
    const req = https.request(
      {
        host: a.agentHost,
        port: a.agentPort,
        path: uri,
        method: "GET",
        headers: { Accept: "application/json", ...sign(store.agentSecret(a), "GET", uri) },
        // Trust exactly this agent's self-signed certificate, nothing else.
        ca: a.cert,
        servername: "hdash-agent",
        checkServerIdentity: (_host, cert) =>
          cert.fingerprint256 === a.fingerprint ? undefined : new Error("agent certificate does not match the pinned certificate"),
        minVersion: "TLSv1.3",
        agent: false,
        timeout: 15_000,
      },
      (res) => {
        const chunks = [];
        let size = 0;
        res.on("data", (c) => {
          size += c.length;
          if (size > MAX_BODY) req.destroy(new Error("agent response too large"));
          else chunks.push(c);
        });
        res.on("end", () => {
          let body;
          try {
            body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
          } catch {
            return reject(new Error(`agent returned invalid JSON (HTTP ${res.statusCode})`));
          }
          if (res.statusCode !== 200) return reject(Object.assign(new Error(`agent: ${body.error || res.statusCode}`), { status: 502 }));
          resolve(body);
        });
      }
    );
    req.on("timeout", () => req.destroy(new Error("agent did not answer in time")));
    req.on("error", reject);
    req.end();
  });
}

async function pollOne(a) {
  const prev = state.get(a.id) || {};
  try {
    const snapshot = await agentGet(a, "/v1/snapshot");
    state.set(a.id, { at: Date.now(), ok: true, snapshot, error: null, lastOk: Date.now() });
  } catch (e) {
    state.set(a.id, { ...prev, at: Date.now(), ok: false, error: e.message });
  }
}

// After an install or restart the agent needs a few seconds before it answers.
async function pollSoon(a, tries = 6) {
  for (let i = 0; i < tries; i++) {
    await pollOne(a);
    if (state.get(a.id)?.ok) return;
    await new Promise((r) => setTimeout(r, 1500));
  }
}

const pollAll = () => Promise.allSettled(store.listAgents().map(pollOne));

function startPolling() {
  pollAll();
  setInterval(pollAll, POLL_MS).unref();
}

const statusOf = (id) => state.get(id) || null;
const forget = (id) => state.delete(id);

module.exports = { agentGet, pollOne, pollSoon, startPolling, statusOf, forget };
