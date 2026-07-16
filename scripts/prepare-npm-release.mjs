import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

const raw = process.argv[2] || process.env.GITHUB_REF_NAME || "";
const version = raw.replace(/^v/, "");
if (!/^\d+\.\d+\.\d+(?:[-+].+)?$/.test(version)) {
  throw new Error(`invalid release version: ${raw}`);
}

const manifests = [
  "npm/lazyskills/package.json",
  "npm/platforms/darwin-arm64/package.json",
  "npm/platforms/darwin-x64/package.json",
  "npm/platforms/linux-arm64/package.json",
  "npm/platforms/linux-x64/package.json",
  "npm/platforms/win32-x64/package.json"
];

for (const relative of manifests) {
  const path = resolve(relative);
  const manifest = JSON.parse(await readFile(path, "utf8"));
  manifest.version = version;
  if (manifest.optionalDependencies) {
    for (const name of Object.keys(manifest.optionalDependencies)) manifest.optionalDependencies[name] = version;
  }
  await writeFile(path, `${JSON.stringify(manifest, null, 2)}\n`);
}

console.log(`Prepared npm packages for ${version}`);
