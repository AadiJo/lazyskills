import test from "node:test";
import assert from "node:assert/strict";
import { platformDescriptor } from "../lib/launcher.js";

test("maps every release platform", () => {
  assert.equal(platformDescriptor("darwin", "arm64").packageName, "@aadijo/lazyskills-darwin-arm64");
  assert.equal(platformDescriptor("darwin", "x64").archive, "lazyskills_Darwin_x86_64.tar.gz");
  assert.equal(platformDescriptor("linux", "arm64").binary, "lazyskills");
  assert.equal(platformDescriptor("linux", "x64").packageName, "@aadijo/lazyskills-linux-x64");
  assert.equal(platformDescriptor("win32", "x64").binary, "lazyskills.exe");
});

test("rejects unsupported platforms clearly", () => {
  assert.throws(() => platformDescriptor("freebsd", "x64"), /unsupported platform freebsd\/x64/);
});
