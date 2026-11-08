// The real Instructions page saves body text and markers together. Isolated
// API fixtures; both engines, every language and desktop/narrow widths.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_MARKER_ASSETS || path.join(__dirname, "../assets"));
const original = { begin: "<!-- magpie:begin · written by magpie from its Library -->", end: "<!-- magpie:end -->" };
const translations = {
  en: ["Instruction block markers", "Restore default markers"],
  zh: ["指令块边界标记", "恢复默认标记"],
  "zh-TW": ["指令區塊邊界標記", "還原預設標記"],
  ja: ["指示ブロックの境界マーカー", "既定のマーカーに戻す"],
  de: ["Begrenzungen des Anweisungsblocks", "Standardmarkierungen wiederherstellen"],
};

function fixture() {
  return {
    dir: "/home/test/.config/magpie/library", backups: "/home/test/.config/magpie/backups", home: "/home/test",
    agents: [{ id: "codex", name: "Codex", icon: "", instructions: "/home/test/.codex/AGENTS.md" }],
    instructions: {
      shared: "Original rules.", markers: { ...original },
      sets: [{ id: "default", name: "", active: true, text: "Original rules." }],
      agents: [{ agent: "codex", name: "Codex", icon: "", path: "/home/test/.codex/AGENTS.md", on: true, extra: "" }],
    },
    foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
  };
}

async function serve(route, lang, lib, sent) {
  const url = new URL(route.request().url());
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
  if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
  if (url.pathname === "/api/library") return route.fulfill({ json: lib });
  if (url.pathname === "/api/library/instructions/save") {
    const body = route.request().postDataJSON();
    sent.push(body);
    if (body.markers) lib.instructions.markers = body.markers;
    if (body.texts?.default !== undefined) lib.instructions.shared = lib.instructions.sets[0].text = body.texts.default;
    if (body.extra?.codex !== undefined) lib.instructions.agents[0].extra = body.extra.codex;
    return route.fulfill({ json: { part: "instructions", instructions: lib.instructions, result: { changed: ["codex"] }, problems: [] } });
  }
  if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
  if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
  if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
  if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  return route.fulfill({ body: await fs.readFile(file), contentType });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}：正文和边界标记一起保存并恢复默认`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [lang, words] of Object.entries(translations)) {
      for (const width of [980, 420]) {
        await t.test(`${lang} ${width}px`, async () => {
          const ctx = await browser.newContext({ viewport: { width, height: 860 }, reducedMotion: "reduce" });
          try {
            await ctx.addInitScript(() => localStorage.setItem("magpie.libTab", "instructions"));
            const page = await ctx.newPage();
            page.setDefaultTimeout(7000);
            page.setDefaultNavigationTimeout(60000);
            const errors = [];
            page.on("pageerror", (e) => errors.push(e.message));
            const lib = fixture(), sent = [];
            await page.route("**/*", (route) => serve(route, lang, lib, sent));
            await page.goto("http://magpie.test/");
            await page.locator('button[data-view="library"]').click();
            const markers = page.locator("#view-library .lib-instruction-markers");
            await markers.waitFor();
            assert.equal(await markers.locator("b").textContent(), words[0]);
            const begin = markers.locator('[data-lib="marker:begin"]');
            const end = markers.locator('[data-lib="marker:end"]');
            assert.equal(await begin.inputValue(), original.begin);
            await page.locator("#view-library .lib-set").first().click();
            await page.locator('[data-lib="set:default"]').fill("Updated body rules.");
            await begin.fill("<!-- team:begin -->");
            await end.fill("<!-- team:end -->");
            await end.press("Control+s");
            await page.waitForFunction(() => document.querySelector("#view-library .lib-savebar")?.hidden);
            assert.deepEqual(sent[0].markers, { begin: "<!-- team:begin -->", end: "<!-- team:end -->" });
            assert.deepEqual(sent[0].texts, { default: "Updated body rules." });
            await page.reload();
            await page.locator('button[data-view="library"]').click();
            await markers.waitFor();
            assert.equal(await begin.inputValue(), "<!-- team:begin -->");
            assert.equal(await end.inputValue(), "<!-- team:end -->");
            const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1);
            assert.equal(overflow, false, "marker editor overflows a narrow window");
            await markers.getByRole("button", { name: words[1], exact: true }).click();
            await page.locator("#view-library .lib-savebar button.primary").click();
            await page.waitForFunction(() => document.querySelector("#view-library .lib-savebar")?.hidden);
            assert.deepEqual(sent.at(-1).markers, original);
            assert.deepEqual(errors, []);
          } finally {
            await ctx.close();
          }
        });
      }
    }
  });
}
