import { chromium } from "playwright-core";
import { mkdir } from "node:fs/promises";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../..", import.meta.url));
const output = resolve(root, "docs/screenshots");
const baseURL = process.env.LAZYSKILLS_SCREENSHOT_URL || "http://127.0.0.1:5173";
const executablePath = process.env.BROWSER_EXECUTABLE || (process.platform === "win32"
  ? "C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe"
  : undefined);

if (!executablePath) throw new Error("set BROWSER_EXECUTABLE to a Chromium-based browser");
await mkdir(output, { recursive: true });

const browser = await chromium.launch({ executablePath, headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 });
page.on("pageerror", (error) => console.error("page error", error));
await page.goto(baseURL, { waitUntil: "domcontentloaded" });
await page.getByText(/shown \/ .* total/).waitFor();
await page.locator(".skill-open").first().waitFor();
const [renderedSkills, apiSkills] = await Promise.all([
  page.locator(".skill-open").count(),
  page.evaluate(async () => (await (await fetch("/api/scan")).json()).result.skills.length)
]);
if (renderedSkills !== apiSkills) throw new Error(`rendered ${renderedSkills} of ${apiSkills} scanned skills`);
await page.screenshot({ path: resolve(output, "web-overview.png") });

const firstSkill = page.locator(".skill-open").first();
const firstSkillName = (await firstSkill.locator(".name").textContent()).trim();
await firstSkill.click();
await page.locator(".detail h1").filter({ hasText: firstSkillName }).waitFor({ timeout: 3000 });
await page.getByRole("button", { name: /actions c/ }).click();
await page.getByRole("button", { name: /Reinstall \/ update/ }).click();
await page.getByText("exact command").waitFor();
const modalState = await page.evaluate(() => ({
  backgroundInert: document.querySelector(".app-shell").inert,
  focusedInDialog: Boolean(document.activeElement?.closest("[role=dialog]"))
}));
if (!modalState.backgroundInert || !modalState.focusedInDialog) throw new Error(`dialog focus state failed: ${JSON.stringify(modalState)}`);
await page.screenshot({ path: resolve(output, "web-action-preview.png") });
await page.getByRole("button", { name: "cancel" }).click();
if (await page.evaluate(() => document.querySelector(".app-shell").inert)) throw new Error("background remained inert after dialog closed");

await page.getByRole("button", { name: "agents", exact: true }).click();
await page.locator(".matrix").waitFor();
await page.screenshot({ path: resolve(output, "web-visibility.png") });

await page.setViewportSize({ width: 390, height: 844 });
await page.locator(".detail.mobile-open").waitFor();
await page.screenshot({ path: resolve(output, "web-mobile-detail.png") });

await browser.close();
