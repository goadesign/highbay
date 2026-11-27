import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { ensureBinary, resolvePlatform } from "./bin/highbay.js";

const scripts = fileURLToPath(new URL("../../scripts/", import.meta.url));
const version = "1.2.3";
const official = `https://github.com/goadesign/highbay/releases/download/v${version}`;
const targets = ["darwin_arm64", "darwin_amd64", "linux_arm64", "linux_amd64"];

function scratch(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "highbay-cli-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}

function bundle(dir, target = resolvePlatform()) {
  const binary = Buffer.from([
    "#!/bin/sh",
    "printf 'args=%s no_deps=%s no_start=%s\\n' \"$*\" \"$HIGHBAY_NO_DEPS\" \"$HIGHBAY_NO_START\"",
    "printf 'install_dir=%s\\n' \"$HIGHBAY_INSTALL_DIR\"",
    "if [ \"$1\" = \"companions\" ]; then",
    "  printf 'launcher=%s\\ncompanion_dir=%s\\n' \"$HIGHBAY_NPM_LAUNCHER\" \"$HIGHBAY_COMPANION_BIN_DIR\"",
    "  command -v chorus",
    "fi",
    "",
  ].join("\n"));
  const archive = `highbay_${version}_${target}.tar.gz`;
  fs.writeFileSync(path.join(dir, "highbay"), binary, { mode: 0o755 });
  execFileSync("tar", ["-czf", archive, "highbay"], { cwd: dir, stdio: "pipe" });
  const bytes = fs.readFileSync(path.join(dir, archive));
  const hash = createHash("sha256").update(bytes).digest("hex");
  return { archive, binary, bytes, hash, line: `${hash}  ${archive}\n` };
}

function release(fixture, urls = []) {
  return async (url) => {
    urls.push(url);
    if (url.endsWith("/checksums.txt")) return new Response(fixture.line);
    if (url.endsWith(`/${fixture.archive}`)) return new Response(fixture.bytes);
    return new Response(null, { status: 404 });
  };
}

function shell(script, args, env) {
  return spawnSync("sh", [path.join(scripts, script), ...args], {
    env: { ...process.env, ...env },
    encoding: "utf8",
  });
}

test("the npm launcher verifies artifacts and isolates each release source", async (t) => {
  const dir = scratch(t);
  const fixture = bundle(dir);
  const cacheRoot = path.join(dir, "cache");
  const urls = [];
  const opts = { version, cacheRoot, fetchImpl: release(fixture, urls) };
  const binary = await ensureBinary(opts);
  assert.deepEqual(fs.readFileSync(binary), fixture.binary);
  assert.ok(fs.statSync(binary).mode & 0o111);
  assert.deepEqual(urls, [`${official}/checksums.txt`, `${official}/${fixture.archive}`]);
  assert.equal(await ensureBinary(opts), binary);
  assert.equal(urls.length, 2);
  const mirror = "https://mirror.invalid/highbay";
  const mirrored = await ensureBinary({ ...opts, baseURL: mirror });
  assert.notEqual(mirrored, binary);
  assert.deepEqual(urls.slice(2), [`${mirror}/checksums.txt`, `${mirror}/${fixture.archive}`]);
});

test("the npm launcher rejects duplicate checksums, corrupted archives, and unsafe redirects", async (t) => {
  const dir = scratch(t);
  const fixture = bundle(dir);
  const cacheRoot = path.join(dir, "cache");
  await assert.rejects(ensureBinary({
    version, cacheRoot, fetchImpl: release({ ...fixture, line: fixture.line.repeat(2) }),
  }), /Expected one SHA-256 checksum/);
  await assert.rejects(ensureBinary({
    version, cacheRoot, fetchImpl: release({ ...fixture, bytes: Buffer.from("corrupted") }),
  }), /SHA-256 verification failed/);
  assert.ok(!fs.readdirSync(cacheRoot, { recursive: true }).includes("highbay"));
  const urls = [];
  await assert.rejects(ensureBinary({
    version, cacheRoot,
    fetchImpl: async (url) => {
      urls.push(url);
      return new Response(null, { status: 302, headers: { location: "http://mirror.invalid/checksums.txt" } });
    },
  }), /redirect to an HTTPS URL/);
  assert.deepEqual(urls, [`${official}/checksums.txt`]);
  assert.deepEqual(targets.map((target) => {
    const [platform, arch] = target.split("_");
    return resolvePlatform(platform, arch === "amd64" ? "x64" : arch);
  }), targets);
  assert.throws(() => resolvePlatform("win32", "x64"), /no release/);
});

test("the packaged executable and npx pass only the resolved companion launchers", async (t) => {
  const dir = scratch(t);
  const cacheRoot = path.join(dir, "cache");
  await ensureBinary({ version, cacheRoot, fetchImpl: release(bundle(dir)) });

  const modules = path.join(dir, "node_modules");
  const packageDir = path.join(modules, "@goadesign/highbay");
  const binDir = path.join(modules, ".bin");
  fs.mkdirSync(binDir, { recursive: true });
  const manifest = JSON.parse(fs.readFileSync(new URL("./package.json", import.meta.url), "utf8"));
  fs.mkdirSync(path.join(packageDir, "bin"), { recursive: true });
  fs.writeFileSync(path.join(packageDir, "package.json"), JSON.stringify({ ...manifest, version }));
  fs.copyFileSync(new URL("./bin/highbay.js", import.meta.url), path.join(packageDir, "bin/highbay.js"));
  fs.chmodSync(path.join(packageDir, manifest.bin.highbay), 0o755);
  for (const [name, aliases] of [
    ["@goadesign/chorus", { chorus: "bin/chorus.js" }],
  ]) {
    // A nested package store, with no .bin entries for the dependencies.
    const depDir = path.join(modules, ".pnpm", name.replace("/", "+"), "node_modules", name);
    fs.mkdirSync(path.join(depDir, "bin"), { recursive: true });
    const visible = path.join(modules, name);
    fs.mkdirSync(path.dirname(visible), { recursive: true });
    fs.symlinkSync(depDir, visible);
    fs.writeFileSync(path.join(depDir, "package.json"), JSON.stringify({ bin: aliases }));
    for (const file of Object.values(aliases)) {
      fs.writeFileSync(path.join(depDir, file), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
    }
  }
  const env = {
    ...process.env,
    HIGHBAY_NPM_CACHE_DIR: cacheRoot,
    HIGHBAY_RELEASE_BASE_URL: official,
    NO_UPDATE_NOTIFIER: "1",
    npm_config_update_notifier: "false",
    npm_config_cache: path.join(dir, "npm-cache"),
  };
  function checkPaths(output) {
    const command = fs.realpathSync(path.join(packageDir, manifest.bin.highbay));
    assert.ok(output.includes(`launcher=${command}\n`));
    const result = output.split("\n").find((line) => line.startsWith("companion_dir="));
    assert.ok(result);
    const companions = result.slice("companion_dir=".length);
    assert.ok(companions.startsWith(path.join(cacheRoot, "companions") + path.sep));
    assert.deepEqual(fs.readdirSync(companions).sort(), ["chorus"]);
    assert.ok(output.includes(path.join(companions, "chorus")));
  }
  for (const [alias, launcher] of Object.entries(manifest.bin)) {
    const executable = path.join(binDir, alias);
    fs.symlinkSync(path.join(packageDir, launcher), executable);
    const out = execFileSync(executable, ["companions", "from npm"], { env, encoding: "utf8" });
    assert.match(out, /^args=companions from npm /);
    checkPaths(out);
  }
  const fromNpx = execFileSync("npx", ["--offline", "@goadesign/highbay", "companions", "from npx"], {
    cwd: dir, env, encoding: "utf8",
  });
  assert.match(fromNpx, /^args=companions from npx /);
  checkPaths(fromNpx);
});

test("the MCP listing matches the npm executable and opens stdio", () => {
  const read = (name) => JSON.parse(fs.readFileSync(new URL(name, import.meta.url), "utf8"));
  const pkg = read("package.json");
  const server = read("../../server.json");
  const [entry] = server.packages;
  assert.equal(server.packages.length, 1);
  assert.deepEqual(pkg.bin, { highbay: "bin/highbay.js" });
  assert.equal(server.name, pkg.mcpName);
  assert.equal(entry.identifier, pkg.name);
  assert.equal(server.version, pkg.version);
  assert.equal(entry.version, pkg.version);
  assert.equal(entry.runtimeHint, "npx");
  assert.equal(entry.transport.type, "stdio");
  assert.deepEqual(entry.packageArguments, [{ type: "positional", value: "stdio" }]);
});

test("the shell installer verifies highbay, preserves it on failure, and passes setup flags", (t) => {
  const dir = scratch(t);
  const fixture = bundle(dir);
  fs.writeFileSync(path.join(dir, "checksums.txt"), fixture.line);
  fs.writeFileSync(path.join(dir, "curl"), [
    "#!/bin/sh",
    "while [ \"$#\" -gt 1 ]; do",
    "  if [ \"$1\" = --output ]; then out=$2; shift; fi",
    "  shift",
    "done",
    "case $1 in",
    `  */latest) printf '%s' 'https://github.com/goadesign/highbay/releases/tag/v${version}' ;;`,
    "  *) cp \"$HIGHBAY_FIXTURE/$(basename \"$1\")\" \"$out\" ;;",
    "esac",
    "",
  ].join("\n"), { mode: 0o755 });
  const bin = path.join(dir, "install bin");
  const env = {
    PATH: `${dir}${path.delimiter}${process.env.PATH}`,
    HIGHBAY_FIXTURE: dir,
    HIGHBAY_INSTALL_DIR: bin,
    HIGHBAY_VERSION: "",
    HIGHBAY_NO_DEPS: "1",
    HIGHBAY_NO_START: "1",
  };
  const result = shell("install.sh", [], env);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /args=up --yes no_deps=1 no_start=1/);
  assert.ok(result.stdout.includes(`install_dir=${bin}\n`));
  const highbay = path.join(bin, "highbay");
  assert.deepEqual(fs.readFileSync(highbay), fixture.binary);
  fs.writeFileSync(path.join(dir, fixture.archive), "corrupted download");
  const bad = shell("install.sh", [], { ...env, HIGHBAY_VERSION: `v${version}` });
  assert.equal(bad.status, 1);
  assert.match(bad.stderr, /SHA-256 verification failed/);
  assert.deepEqual(fs.readFileSync(highbay), fixture.binary);
  const multiline = shell("install.sh", [], { ...env, HIGHBAY_VERSION: `${version}\nextra` });
  assert.equal(multiline.status, 1);
  assert.match(multiline.stderr, /expected a stable Highbay release version/);
});

test("the formula gates output on one valid checksum per supported platform", (t) => {
  const dir = scratch(t);
  const checksums = path.join(dir, "checksums.txt");
  const sums = targets.map((target, index) => `${String(index).repeat(64)}  highbay_${version}_${target}.tar.gz\n`);
  fs.writeFileSync(checksums, sums.join(""));
  const result = shell("homebrew-formula.sh", [version, checksums]);
  assert.equal(result.status, 0, result.stderr);
  for (const target of targets) assert.ok(result.stdout.includes(`${official}/highbay_${version}_${target}.tar.gz`));
  for (const dep of ["git", "go", "goadesign/tap/chorus"]) {
    assert.ok(result.stdout.includes(`depends_on "${dep}"`));
  }
  for (const lines of [sums.slice(1), [...sums, sums[0]], ["bad" + sums[0], ...sums.slice(1)]]) {
    fs.writeFileSync(checksums, lines.join(""));
    const invalid = shell("homebrew-formula.sh", [version, checksums]);
    assert.equal(invalid.status, 1);
    assert.equal(invalid.stdout, "");
  }
  const multiline = shell("homebrew-formula.sh", [`${version}\nextra`, checksums]);
  assert.equal(multiline.status, 1);
  assert.equal(multiline.stdout, "");
});
