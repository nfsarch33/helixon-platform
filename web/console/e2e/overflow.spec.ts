import { test, expect, type Page } from "@playwright/test";

// No page may scroll sideways at any common width. A run whose steps carry a multi-kilobyte argument string once
// made the run page 23,254 px wide and pushed every value off-screen, and nothing caught it.
const widths = [375, 768, 1024, 1440, 1920, 2560];
const routes = ["/console/", "/console/runs/", "/console/board/", "/console/evals/", "/console/costs/", "/console/memory/"];

async function overflow(page: Page) {
  return page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
}

// The run with the longest tool arguments among the newest runs is the hardest case; RUN_ID pins one.
async function hardestRun(page: Page): Promise<string | null> {
  if (process.env.RUN_ID) return process.env.RUN_ID;
  const res = await page.request.get("/api/v1/runs?limit=30");
  if (!res.ok()) return null;
  const runs: Array<{ id: string }> = (await res.json()).runs ?? [];
  let best: string | null = null;
  let longest = -1;
  for (const r of runs) {
    const d = await page.request.get(`/api/v1/runs/${encodeURIComponent(r.id)}`);
    if (!d.ok()) continue;
    const body = await d.json();
    const len = Math.max(0, ...(body.steps ?? []).map((s: { args?: string }) => (s.args ?? "").length));
    if (len > longest) { longest = len; best = r.id; }
  }
  return best;
}

for (const width of widths) {
  test(`no horizontal overflow at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const id = await hardestRun(page);
    const all = id ? [...routes, `/console/runs/detail/?id=${encodeURIComponent(id)}`] : routes;
    for (const route of all) {
      await page.goto(route);
      await page.waitForLoadState("networkidle");
      expect(await overflow(page), `${route} at ${width}px`).toBeLessThanOrEqual(1);
    }
  });
}

test("run detail shows every value inside the viewport", async ({ page }) => {
  const id = await hardestRun(page);
  test.skip(!id, "no runs on this agent");
  await page.setViewportSize({ width: 1920, height: 1000 });
  await page.goto(`/console/runs/detail/?id=${encodeURIComponent(id as string)}`);
  await page.waitForLoadState("networkidle");
  const outside = await page.evaluate(() =>
    [...document.querySelectorAll("dd")].filter((dd) => dd.getBoundingClientRect().right > window.innerWidth).length);
  expect(outside).toBe(0);
});
