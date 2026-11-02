// Backend-owned acceptance of the immutable paired frontend against real MySQL.
// Credentials, browser errors and response bodies stay in the private scratch.
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

assert.equal(process.env.ASTER_BILLING_BROWSER_CI, "1");
const scratch = process.env.ASTER_BILLING_BROWSER_CASE;
const require = createRequire(path.join(process.env.PLAYWRIGHT_MODULE_DIR, "package.json"));
const { firefox, expect } = require("@playwright/test");
const credentials = JSON.parse(fs.readFileSync(path.join(scratch, "credentials.json")));
const originURL = new URL(credentials.origin);
const origin = originURL.origin;
assert.equal(originURL.protocol, "https:");
assert.equal(originURL.hostname, "localhost");
const publicDir = path.join(scratch, "public");
const state = { schema: "aster-billing-browser-progress-v1", phase: "browser-start",
  role: "none", size: "none", locale: "none", fault_hits: 0, fault_http_200: 0,
  fault_fulfilled: 0, fault_failed: 0 };
function progress(phase) {
  state.phase = phase;
  fs.writeFileSync(path.join(scratch, "progress.json"), JSON.stringify(state), { mode: 0o600 });
}
function write(name, value) {
  fs.writeFileSync(path.join(publicDir, name), JSON.stringify(value), { mode: 0o600 });
}
progress("browser-start");
const context = await firefox.launchPersistentContext(path.join(scratch, "firefox-profile"), {
  headless: true, ignoreHTTPSErrors: false, locale: "zh-CN", viewport: { width: 1440, height: 1000 },
  firefoxUserPrefs: { "security.enterprise_roots.enabled": false },
});
context.setDefaultTimeout(15_000);
const page = await context.newPage();
async function api(endpoint) {
  const result = await page.evaluate(async (endpoint) => {
    const response = await fetch(endpoint, { credentials: "same-origin", cache: "no-store" });
    return { status: response.status, value: await response.json() };
  }, endpoint);
  assert.equal(result.status, 200);
  return result.value.data;
}
async function localeMenu() {
  await page.locator("header button").filter({ has: page.locator("svg.lucide-languages") }).click();
}
async function language(locale) {
  progress("language-open");
  await localeMenu();
  progress("language-select");
  await page.getByRole("menuitem", { name: locale === "en" ? /^English/ : /^简体中文/ }).click();
  progress("language-assert");
  await expect(page.locator("html")).toHaveAttribute("lang", locale);
}
const cases = [], screenshots = [];
function passed(id) { cases.push({ id, passed: true }); }
async function screenshot(name) {
  progress("screenshot");
  await page.screenshot({ path: path.join(publicDir, name), fullPage: true });
  screenshots.push(name);
}
async function orders(role, locale) {
  const route = role === "admin" ? "/admin/orders" : "/orders";
  await page.goto(origin + route);
  const data = await api("/api/v1" + route);
  assert.equal(data.items.length, 1);
  const order = data.items[0];
  assert.equal(order.id, credentials.order.id);
  assert.equal(order.number, credentials.order.number);
  assert.deepEqual(order.total, credentials.order.total);
  assert.equal(order.status, "pending_payment");
  const formatted = await page.evaluate(({ total, locale }) => {
    const digits = String(total.amount_minor).padStart(total.decimal_places + 1, "0");
    const decimal = digits.slice(0, -total.decimal_places) + "." + digits.slice(-total.decimal_places);
    return new Intl.NumberFormat(locale === "en" ? "en-US" : "zh-CN", {
      style: "currency", currency: total.currency, currencyDisplay: "narrowSymbol",
      minimumFractionDigits: total.decimal_places, maximumFractionDigits: total.decimal_places,
    }).format(decimal);
  }, { total: credentials.order.total, locale });
  const row = page.getByRole("row").filter({ hasText: order.number });
  await expect(row).toHaveCount(1);
  await expect(row).toContainText(formatted);
  await expect(page.getByRole("alert")).toHaveCount(0);
}
try {
  const mode = process.argv[2];
  if (mode === "untrusted") {
    progress("tls-untrusted-navigation");
    let rejected = false;
    try { await page.goto(origin); }
    catch (error) { rejected = /SEC_ERROR_UNKNOWN_ISSUER|SSL_ERROR_BAD_CERT_DOMAIN|MOZILLA_PKIX_ERROR/.test(String(error)); }
    assert.equal(rejected, true, "Firefox must reject the untrusted fixture CA");
    write("tls.json", { untrusted_ca_rejected: true, private_profile_ca_trusted: false,
      ignore_https_errors: false, host_trust_modified: false });
  } else if (mode === "trusted") {
    progress("tls-trusted-navigation");
    const response = await page.goto(origin);
    assert.equal(response.status(), 200);
    const tls = JSON.parse(fs.readFileSync(path.join(publicDir, "tls.json")));
    assert.equal(tls.untrusted_ca_rejected, true);
    write("tls.json", { ...tls, private_profile_ca_trusted: true });
  } else {
    assert.equal(mode, "acceptance");
    for (const role of ["customer", "admin"]) {
      for (const size of ["desktop", "mobile"]) {
        Object.assign(state, { role, size, locale: "zh-CN" });
        await page.setViewportSize({ width: size === "desktop" ? 1440 : 375, height: 1000 });
        progress("reset-preferences");
        await context.clearCookies();
        await page.goto(origin);
        await page.evaluate(() => localStorage.clear());
        progress("login-open");
        await page.goto(origin + "/login");
        progress("login-fill");
        await page.locator('input[autocomplete="username"]').fill(credentials[role].email);
        await page.locator('input[autocomplete="current-password"]').fill(credentials[role].password);
        progress("login-submit");
        const response = page.waitForResponse(r => r.url().endsWith("/api/v1/auth/login") && r.request().method() === "POST");
        await page.locator('form button[type="submit"]').click();
        assert.equal((await response).status(), 200);
        await page.waitForURL(role === "admin" ? "**/admin" : "**/dashboard");
        progress("login-session");
        const session = await api("/api/v1/session");
        assert.equal(session.demo_mode, false);
        assert.equal(session.user.role, role);
        assert.equal(session.user.email, credentials[role].email);
        progress("login-cookie");
        const authCookies = (await context.cookies()).filter(c => c.httpOnly);
        assert.ok(authCookies.length > 0 && authCookies.every(c => c.secure && c.sameSite !== "None"));
        for (const locale of ["zh-CN", "en"]) {
          state.locale = locale;
          await language(locale);
          progress("currency-select");
          await localeMenu();
          if (role === "admin") {
            await page.getByRole("menuitem", { name: /^USD ·/ }).click();
          } else {
            // Current clients use their account currency; browsing preferences
            // cannot convert an existing order or expose a currency switch.
            await expect(page.getByRole("menuitem", { name: /^USD ·/ })).toHaveCount(0);
            await page.keyboard.press("Escape");
            assert.equal((await api("/api/v1/session")).user.currency, "CNY");
          }
          progress("currency-assert");
          await orders(role, locale);
          passed(`currency-${role}-${size}-${locale}`);
          await screenshot(`scope-${role}-${size}-${locale}.png`);
          progress("audit-navigation");
          await page.goto(origin + (role === "admin" ? "/admin/audit" : "/account/activity"));
          const events = await api(role === "admin" ? "/api/v1/admin/audit-events?limit=50" : "/api/v1/account/activity?limit=25");
          assert.ok(events.items.length > 0);
          progress("audit-assert");
          await expect(page.locator("main")).toContainText(events.items[0].action);
          if (role === "customer") {
            assert.ok(events.items.every(e => !Object.hasOwn(e, "actor_email")));
            await expect(page.locator("main")).not.toContainText(credentials.admin.email);
          }
          passed(`audit-${role}-${size}-${locale}`);
          if (role === "admin") {
            await screenshot(`audit-${size}-${locale}.png`);
            progress("read-fault-inject");
            const pattern = "**/api/v1/admin/audit-events**";
            await page.route(pattern, async route => {
              state.fault_hits++;
              try {
                assert.equal(route.request().method(), "GET");
                const real = await route.fetch();
                assert.equal(real.status(), 200);
                state.fault_http_200++;
                await route.fulfill({ status: 503, contentType: "application/json",
                  body: JSON.stringify({ error: { code: "SERVICE_UNAVAILABLE", message: "private-server-marker" } }) });
                state.fault_fulfilled++;
              } catch (error) { state.fault_failed++; throw error; }
            });
            await page.reload();
            progress("read-fault-assert");
            const alert = page.getByRole("alert");
            await expect(alert).toContainText(locale === "en" ? "The service is temporarily unavailable" : "服务暂时不可用");
            await expect(alert).not.toContainText("private-server-marker");
            progress("read-fault-no-write-warning");
            await expect(alert).not.toContainText(locale === "en" ? "Result not confirmed" : "结果未确认");
            await expect(page.locator("main tbody")).toHaveCount(0);
            assert.equal(state.fault_failed, 0);
            await page.unroute(pattern);
            progress("read-fault-retry");
            await alert.getByRole("button").click();
            progress("read-fault-recover");
            await expect(page.getByRole("alert")).toHaveCount(0);
            await expect(page.locator("main")).toContainText(events.items[0].action);
            passed(`synthetic-get-fault-${size}-${locale}`);
          }
        }
        progress("logout-open");
        await page.getByRole("button", { name: "Account", exact: true }).click();
        progress("logout-submit");
        const logout = page.waitForResponse(r => r.url().endsWith("/api/v1/auth/logout") && r.request().method() === "POST");
        await page.getByRole("menuitem", { name: "Sign out", exact: true }).click();
        assert.equal((await logout).status(), 200);
        progress("logout-session");
        assert.equal((await api("/api/v1/session")).user, null);
      }
    }
    progress("report-assert");
    assert.equal(cases.length, 20);
    assert.equal(screenshots.length, 12);
    assert.ok(state.fault_hits >= 4);
    assert.equal(state.fault_hits, state.fault_http_200);
    assert.equal(state.fault_hits, state.fault_fulfilled);
    progress("report-write");
    write("report.json", { schema: "aster-billing-browser-mysql-v1", passed: true, api_mode: "production",
      mysql_fixture: true, synthetic_get_faults: 4, business_writes: 0,
      authenticated_logins: 4, authenticated_logouts: 4, cases, screenshots: screenshots.sort() });
  }
} finally { await context.close(); }
