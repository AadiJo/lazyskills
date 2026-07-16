import { createWriteStream } from "node:fs";
import { createHash } from "node:crypto";
import { chmod, mkdir, readFile, rename, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";

const raw = process.argv[2] || process.env.GITHUB_REF_NAME || "";
const tag = raw.startsWith("v") ? raw : `v${raw}`;
if (!/^v\d+\.\d+\.\d+(?:[-+].+)?$/.test(tag)) throw new Error(`invalid release tag: ${raw}`);

const packages = [
  ["darwin-arm64", "lazyskills_Darwin_arm64.tar.gz", "lazyskills"],
  ["darwin-x64", "lazyskills_Darwin_x86_64.tar.gz", "lazyskills"],
  ["linux-arm64", "lazyskills_Linux_arm64.tar.gz", "lazyskills"],
  ["linux-x64", "lazyskills_Linux_x86_64.tar.gz", "lazyskills"],
  ["win32-x64", "lazyskills_Windows_x86_64.zip", "lazyskills.exe"]
];

async function download(url, destination) {
  const response = await fetch(url, { redirect: "follow", headers: { Authorization: `Bearer ${process.env.GITHUB_TOKEN}`, "User-Agent": "lazyskills-release" } });
  if (!response.ok || !response.body) throw new Error(`download failed (${response.status}): ${url}`);
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

const releaseRoot = resolve(".npm-release");
await rm(releaseRoot, { recursive: true, force: true });
await mkdir(releaseRoot, { recursive: true });
const checksumsPath = join(releaseRoot, "checksums.txt");
await download(`https://github.com/AadiJo/lazyskills/releases/download/${tag}/checksums.txt`, checksumsPath);
const checksums = await readFile(checksumsPath, "utf8");

for (const [name, archiveName, binaryName] of packages) {
  const work = resolve(".npm-release", name);
  await rm(work, { recursive: true, force: true });
  await mkdir(work, { recursive: true });
  const archive = join(work, archiveName);
  await download(`https://github.com/AadiJo/lazyskills/releases/download/${tag}/${archiveName}`, archive);
	const expected = expectedChecksum(checksums, archiveName);
	const actual = await sha256(archive);
	if (actual !== expected) throw new Error(`checksum mismatch for ${archiveName}`);
  const command = archiveName.endsWith(".zip") ? "unzip" : "tar";
  const args = archiveName.endsWith(".zip") ? ["-q", archive, "-d", work] : ["-xzf", archive, "-C", work];
  const extracted = spawnSync(command, args, { stdio: "inherit" });
  if (extracted.status !== 0) throw new Error(`failed to extract ${archiveName}`);
  const destination = resolve("npm/platforms", name, "bin", binaryName);
  await rm(destination, { force: true });
  await rename(join(work, binaryName), destination);
  if (!binaryName.endsWith(".exe")) await chmod(destination, 0o755);
}

console.log(`Fetched native binaries for ${tag}`);
