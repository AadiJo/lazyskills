import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = resolve(fileURLToPath(new URL("..", import.meta.url)));
const temp = await mkdtemp(join(tmpdir(), "lazyskills-npm-pack-"));
const packs = join(temp, "packs");
const platform = join(root, "npm", "platforms", "linux-x64");
const binary = join(platform, "bin", "lazyskills");

function run(command, args, cwd = root) {
  const result = spawnSync(command, args, { cwd, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] });
  if (result.status !== 0) throw new Error(`${command} ${args.join(" ")} failed:\n${result.stderr || result.stdout}`);
  return result.stdout;
}

async function pack(directory) {
  const output = run("npm", ["pack", directory, "--pack-destination", packs, "--json"]);
  const parsed = JSON.parse(output);
  return join(packs, basename(parsed[0].filename));
}

try {
  await mkdir(join(platform, "bin"), { recursive: true });
  await mkdir(packs, { recursive: true });
  run("go", ["build", "-o", binary, "./cmd/lazyskills"]);
  const platformPack = await pack(platform);
  const launcherPack = await pack(join(root, "npm", "lazyskills"));
  await writeFile(join(temp, "package.json"), '{"private":true}\n');
  run("npm", ["install", "--ignore-scripts", "--no-audit", "--no-fund", platformPack, launcherPack], temp);
  const output = run(join(temp, "node_modules", ".bin", "lazyskills"), ["version"], temp);
  if (!/^lazyskills /m.test(output)) throw new Error(`packed launcher did not execute the native binary:\n${output}`);
  const installed = JSON.parse(await readFile(join(temp, "node_modules", "@aadijo", "lazyskills", "package.json"), "utf8"));
  const expected = JSON.parse(await readFile(join(root, "npm", "lazyskills", "package.json"), "utf8")).version;
  if (installed.version !== expected) throw new Error(`packed launcher version ${installed.version} does not match ${expected}`);
  console.log("Packed npm launcher resolved and executed its native platform package.");
} finally {
  await rm(binary, { force: true });
  await rm(temp, { recursive: true, force: true });
}
