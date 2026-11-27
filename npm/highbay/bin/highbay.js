#!/usr/bin/env node
// Run the verified Highbay binary and make its packaged companions available.
import { spawn, execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import fs from "node:fs";
import { open } from "node:fs/promises";
import { createRequire } from "node:module";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const { version: packageVersion } = require("../package.json");
const launcher = fs.realpathSync(fileURLToPath(import.meta.url));
const defaultCacheRoot = path.resolve(
  process.env.HIGHBAY_NPM_CACHE_DIR || path.join(os.homedir(), ".cache", "highbay", "npm"),
);
const releases = "https://github.com/goadesign/highbay/releases/download";
const maxMetadataBytes = 1024 * 1024;
const maxArchiveBytes = 256 * 1024 * 1024;

export function resolvePlatform(platform = process.platform, arch = process.arch) {
  const osNames = { darwin: "darwin", linux: "linux" };
  const archNames = { arm64: "arm64", x64: "amd64" };
  const goOS = osNames[platform], goArch = archNames[arch];
  if (!goOS || !goArch) {
    throw new Error(`Highbay has no release for ${platform}/${arch}.`);
  }
  return `${goOS}_${goArch}`;
}

export function parseChecksum(checksums, archive) {
  const entries = checksums.split(/\r?\n/).map((line) => line.trim().split(/\s+/))
    .filter((fields) => fields.length === 2 && fields[1].replace(/^\*/, "") === archive);
  if (entries.length !== 1 || !/^[0-9a-f]{64}$/i.test(entries[0][0])) {
    throw new Error(`Expected one SHA-256 checksum for ${archive}.`);
  }
  return entries[0][0].toLowerCase();
}

// Put only the declared companions ahead of the caller's PATH. Their package
// manifests let Node resolve npm and pnpm layouts without consulting .bin.
function companionBinDir(cacheRoot) {
  const companions = [["@goadesign/chorus", "chorus"]].map(([name, command]) => {
    const manifest = require.resolve(`${name}/package.json`);
    const { bin } = JSON.parse(fs.readFileSync(manifest, "utf8"));
    const target = fs.realpathSync(path.resolve(path.dirname(manifest), bin[command]));
    return [command, target];
  });
  const identity = createHash("sha256").update(JSON.stringify(companions)).digest("hex");
  const binDir = path.join(cacheRoot, "companions", identity);
  fs.mkdirSync(binDir, { recursive: true });
  for (const [command, target] of companions) {
    const alias = path.join(binDir, command);
    try {
      fs.symlinkSync(target, alias);
    } catch (err) {
      if (err.code !== "EEXIST" || fs.readlinkSync(alias) !== target) throw err;
    }
  }
  return binDir;
}

// GitHub redirects asset requests to its release CDN. Check the target
// before following it and share one deadline across the download.
async function releaseResponse(url, fetchImpl) {
  const signal = AbortSignal.timeout(120_000);
  for (let redirects = 0; redirects < 10; redirects++) {
    const response = await fetchImpl(url, { redirect: "manual", signal });
    if (![301, 302, 303, 307, 308].includes(response.status)) return response;
    const location = response.headers.get("location");
    await response.body?.cancel();
    if (!location) throw new Error("The release download redirected without a location.");
    const target = new URL(location, url);
    if (target.protocol !== "https:" || target.username || target.password) {
      throw new Error("The release download must redirect to an HTTPS URL without credentials.");
    }
    url = target.href;
  }
  throw new Error("The release download redirected too many times.");
}

// Enforce limits even when the server does not send Content-Length.
async function download(url, limit, fetchImpl, consume) {
  const response = await releaseResponse(url, fetchImpl);
  if (!response.ok || !response.body) {
    await response.body?.cancel();
    throw new Error(`GET ${url} returned HTTP ${response.status}.`);
  }
  let size = 0;
  for await (const chunk of response.body) {
    size += chunk.byteLength;
    if (size > limit) {
      throw new Error(`The response from ${url} exceeded the download limit.`);
    }
    await consume(chunk);
  }
}

export async function ensureBinary({
  version = packageVersion,
  cacheRoot = defaultCacheRoot,
  baseURL = process.env.HIGHBAY_RELEASE_BASE_URL || `${releases}/v${version}`,
  platform = process.platform,
  arch = process.arch,
  fetchImpl = fetch,
} = {}) {
  if (!/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(version)) {
    throw new Error("Expected a stable Highbay archive version.");
  }
  const target = resolvePlatform(platform, arch);
  const source = new URL(baseURL);
  if (source.protocol !== "https:" || source.username || source.password ||
      source.search || source.hash) {
    throw new Error("The Highbay release base must be an HTTPS URL without credentials, a query, or a fragment.");
  }
  const base = source.href.replace(/\/+$/, "");
  // A version downloaded from a mirror cannot be reused by an official run.
  const sourceKey = createHash("sha256").update(base).digest("hex");
  const dir = path.join(cacheRoot, sourceKey, version, target);
  const binary = path.join(dir, "highbay");
  if (fs.existsSync(binary)) return binary;

  const archive = `highbay_${version}_${target}.tar.gz`;
  const decoder = new TextDecoder();
  let checksums = "";
  await download(`${base}/checksums.txt`, maxMetadataBytes, fetchImpl, (chunk) => {
    checksums += decoder.decode(chunk, { stream: true });
  });
  const want = parseChecksum(checksums + decoder.decode(), archive);

  fs.mkdirSync(dir, { recursive: true });
  const stage = fs.mkdtempSync(path.join(dir, ".download-"));
  try {
    const downloaded = path.join(stage, archive);
    const file = await open(downloaded, "wx");
    const hash = createHash("sha256");
    try {
      await download(`${base}/${archive}`, maxArchiveBytes, fetchImpl, async (chunk) => {
        hash.update(chunk);
        await file.writeFile(chunk);
      });
    } finally {
      await file.close();
    }
    if (hash.digest("hex") !== want) {
      throw new Error(`SHA-256 verification failed for ${archive}.`);
    }
    execFileSync("tar", ["-xzf", downloaded, "-C", stage, "highbay"], { stdio: "pipe" });
    const unpacked = path.join(stage, "highbay");
    if (!fs.lstatSync(unpacked).isFile()) {
      throw new Error("The verified release archive did not contain a regular highbay file.");
    }
    fs.chmodSync(unpacked, 0o755);
    fs.renameSync(unpacked, binary);
    return binary;
  } finally {
    fs.rmSync(stage, { recursive: true, force: true });
  }
}

async function run() {
  const binary = await ensureBinary();
  const binDir = companionBinDir(defaultCacheRoot);
  const child = spawn(binary, process.argv.slice(2), {
    stdio: "inherit",
    env: {
      ...process.env,
      PATH: [binDir, process.env.PATH].filter(Boolean).join(path.delimiter),
      HIGHBAY_NPM_LAUNCHER: launcher,
      HIGHBAY_COMPANION_BIN_DIR: binDir,
    },
  });
  for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
    process.on(signal, () => child.kill(signal));
  }
  child.once("error", (err) => {
    process.stderr.write(`highbay: ${err.message}\n`);
    process.exitCode = 1;
  });
  child.once("exit", (code, signal) => {
    if (signal) {
      process.removeAllListeners(signal);
      process.kill(process.pid, signal);
    } else {
      process.exitCode = code ?? 1;
    }
  });
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === launcher) {
  run().catch((err) => {
    process.stderr.write(`highbay: ${err.message}\n`);
    process.exitCode = 1;
  });
}
