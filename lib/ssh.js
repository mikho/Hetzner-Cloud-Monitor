// Installs, updates and removes agents over SSH with the system OpenSSH client.
//
// - The host key must be one the user confirmed by fingerprint. ssh runs with
//   StrictHostKeyChecking=yes against a one-off known_hosts file.
// - Passwords, private keys and passphrases are written to a 0700 temp dir
//   (tmpfs in Docker) only for the duration of one operation, then removed.
//   They are never logged or stored.
// - Every value put into a remote command is validated first and then
//   shell-quoted.
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const crypto = require("node:crypto");

const ASKPASS = path.join(__dirname, "askpass.sh");
const AGENT_BIN_DIR = process.env.AGENT_BIN_DIR || path.join(__dirname, "..", "agent-bin");
const REMOTE_BIN = "/usr/local/bin/hdash-agent";
const RESULT_PREFIX = "HDASH-RESULT ";

const bad = (msg) => Object.assign(new Error(msg), { status: 400 });

const HOST_RE = /^(?=.{1,253}$)[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$/;
const IPV6_RE = /^[0-9A-Fa-f:.]{2,45}$/;
const USER_RE = /^[a-z_][a-z0-9_.-]{0,31}$/;
const IP_OR_CIDR_RE = /^[0-9A-Fa-f:.]{2,45}(\/\d{1,3})?$/;

function checkHost(h) {
  h = String(h || "").trim();
  if (h.startsWith("-") || !(HOST_RE.test(h) || (h.includes(":") && IPV6_RE.test(h)))) throw bad("invalid host");
  return h;
}
function checkPort(p, fallback) {
  const n = p === undefined || p === null || p === "" ? fallback : Number(p);
  if (!Number.isInteger(n) || n < 1 || n > 65535) throw bad("invalid port");
  return n;
}
function checkUser(u) {
  u = String(u || "").trim();
  if (!USER_RE.test(u)) throw bad("invalid SSH user");
  return u;
}
function checkIpList(list) {
  const arr = Array.isArray(list) ? list : String(list || "").split(",");
  const out = arr.map((s) => String(s).trim()).filter(Boolean);
  for (const s of out) if (!IP_OR_CIDR_RE.test(s)) throw bad(`invalid IP or CIDR: ${s}`);
  return out;
}
function checkListen(l) {
  l = String(l || "").trim();
  if (l && !/^[0-9A-Fa-f:.]{2,45}$/.test(l)) throw bad("listen must be an IP address or empty");
  return l;
}

const q = (s) => `'${String(s).replace(/'/g, `'\\''`)}'`;

// SHA256 fingerprint in OpenSSH's format, from a known_hosts line.
function fingerprintOf(line) {
  const blob = line.trim().split(/\s+/)[2];
  return "SHA256:" + crypto.createHash("sha256").update(Buffer.from(blob, "base64")).digest("base64").replace(/=+$/, "");
}

function spawnCollect(cmd, args, { input, env, timeout = 120_000 } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { env: { PATH: process.env.PATH, ...env }, stdio: ["pipe", "pipe", "pipe"] });
    const out = [];
    const err = [];
    const timer = setTimeout(() => child.kill("SIGKILL"), timeout);
    child.stdout.on("data", (c) => out.push(c));
    child.stderr.on("data", (c) => err.push(c));
    child.on("error", (e) => {
      clearTimeout(timer);
      reject(e);
    });
    child.on("close", (code, signal) => {
      clearTimeout(timer);
      resolve({ code: signal ? -1 : code, stdout: Buffer.concat(out).toString("utf8"), stderr: Buffer.concat(err).toString("utf8") });
    });
    child.stdin.on("error", () => {});
    child.stdin.end(input ?? "");
  });
}

async function scanHostKeys(host, port) {
  host = checkHost(host);
  port = checkPort(port, 22);
  const r = await spawnCollect("ssh-keyscan", ["-T", "8", "-p", String(port), "-t", "ed25519,ecdsa,rsa", "--", host], { timeout: 20_000 });
  const keys = r.stdout
    .split("\n")
    .filter((l) => l && !l.startsWith("#") && l.trim().split(/\s+/).length >= 3)
    .map((line) => ({ type: line.trim().split(/\s+/)[1], fingerprint: fingerprintOf(line), line: line.trim() }));
  if (!keys.length) throw Object.assign(new Error(`no SSH host key from ${host}:${port}`), { status: 502 });
  return keys;
}

// Opens a scoped session: writes credentials to a private temp dir, runs fn,
// and always removes the dir afterwards.
async function withSession(conn, fn) {
  const host = checkHost(conn.host);
  const port = checkPort(conn.port, 22);
  const user = checkUser(conn.user);
  const auth = conn.auth || {};
  if (auth.type !== "password" && auth.type !== "key") throw bad("auth.type must be password or key");

  let knownHosts = conn.knownHosts;
  if (!knownHosts) {
    const fp = String(conn.hostKeyFingerprint || "");
    if (!fp.startsWith("SHA256:")) throw bad("confirm the server's host key fingerprint first");
    const matching = (await scanHostKeys(host, port)).filter((k) => k.fingerprint === fp);
    if (!matching.length) throw Object.assign(new Error("the server's host key does not match the fingerprint you confirmed"), { status: 409 });
    knownHosts = matching.map((k) => k.line).join("\n");
  }

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "hdash-ssh-"));
  try {
    fs.chmodSync(dir, 0o700);
    const file = (name, content) => {
      const p = path.join(dir, name);
      fs.writeFileSync(p, content, { mode: 0o600 });
      return p;
    };
    file("known_hosts", knownHosts + "\n");
    const args = [
      "-F", "/dev/null",
      "-o", "StrictHostKeyChecking=yes",
      "-o", `UserKnownHostsFile=${path.join(dir, "known_hosts")}`,
      "-o", "GlobalKnownHostsFile=/dev/null",
      "-o", "UpdateHostKeys=no",
      "-o", "ConnectTimeout=10",
      "-o", "ServerAliveInterval=15",
      "-o", "NumberOfPasswordPrompts=1",
      "-o", "LogLevel=ERROR",
      "-o", "ControlMaster=no",
      "-o", "ControlPath=none",
      "-o", "ForwardAgent=no",
      "-o", "ForwardX11=no",
      "-o", "ClearAllForwardings=yes",
      "-o", "IdentityAgent=none",
      "-o", "PermitLocalCommand=no",
      "-p", String(port),
      "-l", user,
    ];
    const env = { HOME: dir, SSH_ASKPASS: ASKPASS, SSH_ASKPASS_REQUIRE: "force", DISPLAY: "none" };
    if (auth.type === "password") {
      if (!auth.password) throw bad("password is required");
      env.HDASH_SECRET_FILE = file("secret", auth.password);
      args.push("-o", "PubkeyAuthentication=no", "-o", "PreferredAuthentications=keyboard-interactive,password");
    } else {
      if (!auth.privateKey || !/-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(auth.privateKey)) throw bad("a private key in PEM/OpenSSH format is required");
      const keyPath = file("id", auth.privateKey.replace(/\r\n/g, "\n").trim() + "\n");
      env.HDASH_SECRET_FILE = file("secret", auth.passphrase || "");
      args.push("-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no",
        "-o", "PreferredAuthentications=publickey", "-o", "IdentitiesOnly=yes", "-i", keyPath);
    }
    // For sudo: an explicit sudo password, otherwise the login password.
    const sudoPassword = conn.sudoPassword || (auth.type === "password" ? auth.password : "");
    const isRoot = user === "root";

    const sh = async (command, opts = {}) => {
      const r = await spawnCollect("ssh", [...args, "--", host, command], { env, ...opts });
      if (r.code === 255) throw Object.assign(new Error(`SSH: ${r.stderr.trim() || "connection failed"}`), { status: 502 });
      return r;
    };
    const asRoot = (command, opts = {}) => {
      if (isRoot) return sh(command, opts);
      if (sudoPassword) return sh(`sudo -S -p '' -- sh -c ${q(command)}`, { ...opts, input: sudoPassword + "\n" });
      return sh(`sudo -n -- sh -c ${q(command)}`, opts);
    };
    return await fn({ sh, asRoot, host, port, user, knownHosts });
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

const ARCH = { x86_64: "amd64", amd64: "amd64", aarch64: "arm64", arm64: "arm64" };

async function probeWith(s) {
  const r = await s.sh(
    "uname -m; echo ---; (. /etc/os-release 2>/dev/null; echo \"$PRETTY_NAME\"); echo ---; " +
      "[ -d /run/systemd/system ] && echo yes || echo no; echo ---; " +
      "{ [ -S /run/docker.sock ] || [ -S /var/run/docker.sock ]; } && echo yes || echo no; echo ---; " +
      `[ -x ${REMOTE_BIN} ] && ${REMOTE_BIN} version || echo none`
  );
  if (r.code !== 0) throw Object.assign(new Error(`probe failed: ${r.stderr.trim()}`), { status: 502 });
  const [arch, osName, systemd, docker, installed] = r.stdout.split("---").map((x) => x.trim());
  const root = await s.asRoot("id -u");
  return {
    arch: ARCH[arch] || null,
    machine: arch,
    os: osName,
    systemd: systemd === "yes",
    docker: docker === "yes",
    installedVersion: installed === "none" ? null : installed,
    rootOk: root.code === 0 && root.stdout.trim() === "0",
    rootError: root.code === 0 ? null : (root.stderr.trim() || "sudo failed").slice(0, 300),
  };
}

function binaryFor(arch) {
  const p = path.join(AGENT_BIN_DIR, `hdash-agent-linux-${arch}`);
  if (!fs.existsSync(p)) throw Object.assign(new Error(`agent binary for ${arch} is missing (${p})`), { status: 500 });
  const data = fs.readFileSync(p);
  return { data, sha256: crypto.createHash("sha256").update(data).digest("hex") };
}

// Uploads the agent binary (and optionally the key) to a private temp dir on
// the server and checks the checksum. Returns the remote dir.
async function upload(s, arch, secretHex) {
  const bin = binaryFor(arch);
  const r = await s.sh(
    'umask 077 && d=$(mktemp -d /tmp/hdash.XXXXXXXX) && cat > "$d/hdash-agent" && chmod 700 "$d/hdash-agent" && sha256sum "$d/hdash-agent" && echo "DIR=$d"',
    { input: bin.data }
  );
  const dir = /DIR=(\/tmp\/hdash\.[A-Za-z0-9]{8})\s*$/.exec(r.stdout)?.[1];
  if (r.code !== 0 || !dir) throw Object.assign(new Error(`upload failed: ${r.stderr.trim()}`), { status: 502 });
  if (!r.stdout.startsWith(bin.sha256)) {
    await s.sh(`rm -rf ${q(dir)}`);
    throw Object.assign(new Error("uploaded agent checksum does not match"), { status: 502 });
  }
  if (secretHex) {
    const w = await s.sh(`umask 077 && cat > ${q(dir + "/secret")}`, { input: secretHex });
    if (w.code !== 0) throw Object.assign(new Error(`upload failed: ${w.stderr.trim()}`), { status: 502 });
  }
  return dir;
}

function parseResult(r) {
  const line = r.stdout.split("\n").find((l) => l.startsWith(RESULT_PREFIX));
  if (r.code !== 0 || !line) {
    const why = (r.stderr || r.stdout).trim().split("\n").slice(-3).join(" ").slice(0, 500);
    throw Object.assign(new Error(`agent install failed: ${why || "exit " + r.code}`), { status: 502 });
  }
  return JSON.parse(line.slice(RESULT_PREFIX.length));
}

// Runs the uploaded or installed binary's `install` as root.
async function runInstall(s, bin, flags, cleanupDir) {
  const cmd = `${q(bin)} install ${flags.join(" ")}; rc=$?; ${cleanupDir ? `rm -rf ${q(cleanupDir)}; ` : ""}exit $rc`;
  const r = await s.asRoot(cmd);
  if (cleanupDir) await s.sh(`rm -rf ${q(cleanupDir)}`).catch(() => {});
  return parseResult(r);
}

function installFlags(o) {
  const f = [];
  if (o.secret) f.push(`--secret-file ${q(o.dir + "/secret")}`);
  if (o.port !== undefined) f.push(`--port ${o.port}`);
  if (o.listen !== undefined) f.push(`--listen ${q(o.listen)}`);
  if (o.allowFrom !== undefined) f.push(`--allow-from ${q(o.allowFrom.join(","))}`);
  if (o.docker !== undefined) f.push(`--docker=${o.docker ? "true" : "false"}`);
  if (o.rotateCert) f.push("--rotate-cert");
  return f;
}

module.exports = {
  checkHost, checkPort, checkIpList, checkListen, q,
  scanHostKeys, withSession, probeWith, upload, runInstall, installFlags, REMOTE_BIN,
};
