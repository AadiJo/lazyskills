import { createHash } from "node:crypto";
import { createWriteStream, existsSync } from "node:fs";
import { chmod, mkdir, readFile, rename, rm } from "node:fs/promises";
import { homedir, tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";
import { spawn, spawnSync } from "node:child_process";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";

const require = createRequire(import.meta.url);
const repo = "AadiJo/lazyskills";

const platforms = new Map([
  ["darwin-arm64", { packageName: "@aadijo/lazyskills-darwin-arm64", archive: "lazyskills_Darwin_arm64.tar.gz", binary: "lazyskills" }],
  ["darwin-x64", { packageName: "@aadijo/lazyskills-darwin-x64", archive: "lazyskills_Darwin_x86_64.tar.gz", binary: "lazyskills" }],
  ["linux-arm64", { packageName: "@aadijo/lazyskills-linux-arm64", archive: "lazyskills_Linux_arm64.tar.gz", binary: "lazyskills" }],
  ["linux-x64", { packageName: "@aadijo/lazyskills-linux-x64", archive: "lazyskills_Linux_x86_64.tar.gz", binary: "lazyskills" }],
  ["win32-x64", { packageName: "@aadijo/lazyskills-win32-x64", archive: "lazyskills_Windows_x86_64.zip", binary: "lazyskills.exe" }]
]);

export function platformDescriptor(platform = process.platform, arch = process.arch) {
  const descriptor = platforms.get(`${platform}-${arch}`);
  if (!descriptor) {
    throw new Error(`unsupported platform ${platform}/${arch}; install a native release from https://github.com/${repo}/releases`);
  }
  return descriptor;
}

export function installedBinary(descriptor) {
  try {
    const manifest = require.resolve(`${descriptor.packageName}/package.json`);
    const candidate = join(dirname(manifest), "bin", descriptor.binary);
    return existsSync(candidate) ? candidate : "";
  } catch {
    return "";
  }
}

async function launcherVersion() {
  const manifestPath = fileURLToPath(new URL("../package.json", import.meta.url));
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  if (!/^\d+\.\d+\.\d+(?:[-+].+)?$/.test(manifest.version) || manifest.version === "0.0.0-development") {
    throw new Error("platform package is unavailable and this development launcher has no matching GitHub release");
  }
  return manifest.version;
}

function cacheRoot() {
  if (process.env.XDG_CACHE_HOME) return join(process.env.XDG_CACHE_HOME, "lazyskills", "npm");
  if (process.platform === "win32" && process.env.LOCALAPPDATA) return join(process.env.LOCALAPPDATA, "lazyskills", "npm");
  return join(homedir(), ".cache", "lazyskills", "npm");
}

async function download(url, destination) {
  const response = await fetch(url, { redirect: "follow", headers: { "User-Agent": "lazyskills-npm-launcher" } });
  if (!response.ok || !response.body) throw new Error(`download failed (${response.status}) for ${url}`);
  await mkdir(dirname(destination), { recursive: true });
  await pipeline(Readable.fromWeb(response.body), createWriteStream(destination));
}

async function sha256(path) {
  const hash = createHash("sha256");
  hash.update(await readFile(path));
  return hash.digest("hex");
}

function expectedChecksum(contents, archive) {
  for (const line of contents.split(/\r?\n/)) {
    const match = line.trim().match(/^([a-f0-9]{64})\s+\*?(.+)$/i);
    if (match && match[2] === archive) return match[1].toLowerCase();
  }
  throw new Error(`release checksums do not contain ${archive}`);
}

async function extractArchive(archive, descriptor, destination) {
  const work = join(tmpdir(), `lazyskills-${process.pid}-${Date.now()}`);
  await mkdir(work, { recursive: true });
  try {
    let command;
    let args;
    if (archive.endsWith(".zip")) {
      command = "powershell.exe";
      args = ["-NoProfile", "-NonInteractive", "-Command", "Expand-Archive -LiteralPath $args[0] -DestinationPath $args[1] -Force", archive, work];
    } else {
      command = "tar";
      args = ["-xzf", archive, "-C", work];
    }
    const extracted = spawnSync(command, args, { stdio: "pipe", encoding: "utf8" });
    if (extracted.status !== 0) throw new Error(`could not extract ${basename(archive)}: ${(extracted.stderr || extracted.stdout).trim()}`);
    const source = join(work, descriptor.binary);
    if (!existsSync(source)) throw new Error(`release archive does not contain ${descriptor.binary}`);
    await mkdir(dirname(destination), { recursive: true });
    await rename(source, destination);
    if (process.platform !== "win32") await chmod(destination, 0o755);
  } finally {
    await rm(work, { recursive: true, force: true });
  }
}

export async function fallbackBinary(descriptor) {
  const version = await launcherVersion();
  const destination = join(cacheRoot(), version, descriptor.binary);
  if (existsSync(destination)) return destination;
  const tag = version.startsWith("v") ? version : `v${version}`;
  const base = `https://github.com/${repo}/releases/download/${tag}`;
  const archive = `${destination}.${descriptor.archive.endsWith(".zip") ? "zip" : "tar.gz"}`;
  const checksums = `${destination}.checksums.txt`;
  await download(`${base}/${descriptor.archive}`, archive);
  await download(`${base}/checksums.txt`, checksums);
  const expected = expectedChecksum(await readFile(checksums, "utf8"), descriptor.archive);
  const actual = await sha256(archive);
  if (actual !== expected) {
    await rm(archive, { force: true });
    throw new Error(`checksum mismatch for ${descriptor.archive}`);
  }
  await extractArchive(archive, descriptor, destination);
  await rm(archive, { force: true });
  await rm(checksums, { force: true });
  return destination;
}

export async function resolveBinary() {
  const descriptor = platformDescriptor();
  return installedBinary(descriptor) || fallbackBinary(descriptor);
}

export async function run(args) {
  const binary = await resolveBinary();
  const child = spawn(binary, args, { stdio: "inherit", windowsHide: false });
  child.on("error", (error) => {
    console.error(`lazyskills: failed to start native binary: ${error.message}`);
    process.exitCode = 1;
  });
  child.on("exit", (code, signal) => {
    if (signal) process.kill(process.pid, signal);
    else process.exitCode = code ?? 1;
  });
}
