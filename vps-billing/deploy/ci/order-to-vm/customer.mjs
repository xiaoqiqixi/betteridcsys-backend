// Customer and staff mutations use the pinned frontend in a real Firefox.
// The pipe carries private fixture controls and independent libvirt assertions.
import { createInterface } from 'node:readline';
import { pathToFileURL } from 'node:url';
import { resolve } from 'node:path';
import assert from 'node:assert/strict';

const { firefox, expect } = await import(pathToFileURL(resolve('artifacts/e2e-browser/node_modules/@playwright/test/index.mjs')));
const lines = createInterface({ input: process.stdin });
const pending = new Map();
let sequence = 0;
lines.on('line', (line) => {
  const response = JSON.parse(line);
  const p = pending.get(response.id);
  if (!p) return;
  pending.delete(response.id);
  response.error ? p.reject(new Error(response.error)) : p.resolve(response.value);
});
function fixture(method, args = {}) {
  const id = ++sequence;
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    process.stdout.write(JSON.stringify({ id, method, args }) + '\n');
  });
}
const contexts = [];
let setup;
async function browser(name) {
  const context = await firefox.launchPersistentContext(setup.profiles[name], {
    headless: true, locale: 'en-US', timezoneId: 'UTC', viewport: { width: 1360, height: 960 },
  });
  context.setDefaultTimeout(30000);
  contexts.push(context);
  return [context, await context.newPage()];
}
async function data(context, path) {
  const result = await context.request.get(setup.origin + '/api/v1' + path);
  assert.equal(result.status(), 200, `GET ${path} failed`);
  return (await result.json()).data;
}
async function mutation(page, suffix, action) {
  const waiting = page.waitForResponse(r => new URL(r.url()).pathname.endsWith(suffix) && r.request().method() !== 'GET');
  await action();
  const response = await waiting;
  assert.ok(response.status() >= 200 && response.status() < 300, `${suffix}: HTTP ${response.status()}`);
  return (await response.json()).data;
}
async function login(page, email, password) {
  await page.goto(setup.origin + '/login');
  await page.getByRole('button', { name: /^(语言|Language)$/ }).click();
  await page.getByRole('menuitem', { name: /^English/ }).click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await page.getByLabel('Email', { exact: true }).fill(email);
  await page.getByLabel('Password', { exact: true }).fill(password);
  await mutation(page, '/auth/login', () => page.getByRole('button', { name: 'Sign in', exact: true }).click());
}
async function menu(page, name) {
  await page.getByRole('button', { name: 'Actions', exact: true }).click();
  await page.getByRole('menuitem', { name, exact: true }).click();
}
async function servicePage(page, id) {
  await page.goto(setup.origin + '/services/' + id);
  await expect(page.getByRole('button', { name: 'Actions', exact: true })).toBeVisible();
}
async function power(page, service, action, expected) {
  await servicePage(page, service.id);
  const result = await mutation(page, `/services/${service.id}/power`, async () => {
    if (action === 'Start') await page.getByRole('button', { name: action, exact: true }).click();
    else {
      await page.getByRole('button', { name: 'Power', exact: true }).click();
      await page.getByRole('menuitem', { name: action, exact: true }).click();
      await page.getByRole('dialog').getByRole('button', { name: action, exact: true }).click();
    }
  });
  await fixture('state', { id: service.id, state: 'active', power: expected, task: result.id, step: action.toLowerCase().replaceAll(' ', '-') });
}
async function placeOrder(page, name, password) {
  await page.goto(setup.origin + '/store');
  await page.locator(`a[href^="/store/${setup.product}"]`).first().click();
  await expect(page.getByRole('heading', { name: 'E2E Small', exact: true })).toBeVisible();
  await page.getByRole('combobox').click();
  await page.getByRole('option', { name: /24\.04/ }).click();
  await page.getByLabel(/^Hostname\b/).fill(name);
  await page.getByLabel('Root password', { exact: true }).fill(password);
  await page.getByRole('button', { name: 'Continue to checkout', exact: true }).click();
  const result = await mutation(page, '/orders', () => page.getByRole('button', { name: 'Place order', exact: true }).click());
  assert.equal(result.invoice.status, 'unpaid');
  await fixture('step', { id: 'checkout-' + name, order: result.order.id, invoice: result.invoice.id });
  return result;
}
async function payManual(page, invoice) {
  await page.goto(setup.origin + '/admin/invoices/' + invoice);
  await page.getByRole('button', { name: 'Record payment', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Reference', { exact: true }).fill('acceptance-' + invoice);
  await mutation(page, `/invoices/${invoice}/payments`, () => dialog.getByRole('button', { name: 'Confirm', exact: true }).click());
}

try {
  setup = await fixture('setup');
  const [staffContext, staff] = await browser('staff');
  const [customerContext, customer] = await browser('customer');
  await login(staff, 'staff@e2e.invalid', setup.staffPassword);
  const staffSession = await data(staffContext, '/session');
  assert.equal(staffSession.user.role, 'admin');
  assert.ok(staffSession.user.permissions.includes('invoices.write'), 'staff cannot record payments');
  await customer.goto(setup.origin + '/register');
  await customer.getByLabel('Full name', { exact: true }).fill('Acceptance Customer');
  await customer.getByLabel('Email', { exact: true }).fill('customer@e2e.invalid');
  await customer.getByLabel('Password', { exact: true }).fill(setup.customerPassword);
  await customer.getByRole('combobox', { name: 'Country / region', exact: true }).click();
  await customer.getByRole('option', { name: 'United States', exact: true }).click();
  await customer.getByRole('checkbox').check();
  await mutation(customer, '/auth/register', () => customer.getByRole('button', { name: 'Create account', exact: true }).click());
  await customer.goto(await fixture('mail', { address: 'customer@e2e.invalid' }));
  await expect(customer.getByRole('heading', { name: 'Email verified', exact: true })).toBeVisible();
  await customer.getByRole('link', { name: 'Continue', exact: true }).click();
  await customer.getByRole('button', { name: 'Account', exact: true }).click();
  await customer.getByRole('menuitem', { name: 'Sign out', exact: true }).click();
  await login(customer, 'customer@e2e.invalid', setup.customerPassword);
  const session = await data(customerContext, '/session');
  assert.ok(session.user);
  await fixture('registered', { id: session.user.id });

  const manual = await placeOrder(customer, 'manual-e2e', setup.rootPassword);
  await payManual(staff, manual.invoice.id);
  assert.equal((await data(customerContext, '/invoices/' + manual.invoice.id)).status, 'paid');
  const service = await fixture('provisioned', { order: manual.order.id, step: 'manual-payment-provisioning', password: setup.rootPassword });
  await servicePage(customer, service.id);
  await expect(customer.getByText(service.ip, { exact: true }).first()).toBeVisible();

  const gateway = await placeOrder(customer, 'gateway-e2e', setup.secondPassword);
  await customer.goto(setup.origin + '/invoices/' + gateway.invoice.id);
  // The fixture's reserved checkout host is synthetic. Observe the real plugin
  // checkout response and abort only that external navigation, never stack APIs.
  await customer.route('https://checkout.example.test/**', route => route.abort('blockedbyclient'));
  const checkout = await mutation(customer, `/invoices/${gateway.invoice.id}/pay`, () => customer.getByRole('button', { name: /^Pay / }).click());
  assert.equal(checkout.status, 'redirect');
  assert.equal(new URL(checkout.checkout_url).hostname, 'checkout.example.test');
  await fixture('gateway', { invoice: gateway.invoice.id });
  const second = await fixture('provisioned', { order: gateway.order.id, step: 'fixture-payment-provisioning', password: setup.secondPassword });
  assert.notEqual(service.ip, second.ip);

  await power(customer, service, 'Shut down', 'stopped');
  await power(customer, service, 'Start', 'running');
  await power(customer, service, 'Reboot', 'running');
  await servicePage(customer, service.id);
  await menu(customer, 'Reset root password');
  await customer.getByRole('dialog').getByLabel('Root password', { exact: true }).fill(setup.resetPassword);
  let task = await mutation(customer, `/services/${service.id}/reset-password`, () => customer.getByRole('dialog').getByRole('button', { name: 'Confirm', exact: true }).click());
  await fixture('state', { id: service.id, state: 'active', power: 'running', task: task.id, step: 'reset-root-password', password: setup.resetPassword });

  await servicePage(customer, service.id);
  await menu(customer, 'Reinstall OS');
  let dialog = customer.getByRole('dialog');
  await dialog.getByRole('combobox').click();
  await customer.getByRole('option', { name: /22\.04/ }).click();
  await dialog.getByLabel('Root password', { exact: true }).fill(setup.reinstallPassword);
  await dialog.getByPlaceholder('manual-e2e', { exact: true }).fill('manual-e2e');
  task = await mutation(customer, `/services/${service.id}/reinstall`, () => dialog.getByRole('button', { name: 'Erase and reinstall', exact: true }).click());
  await fixture('state', { id: service.id, state: 'active', power: 'running', task: task.id, step: 'reinstall-second-image', image: 'ubuntu-22', password: setup.reinstallPassword });

  await power(customer, service, 'Shut down', 'stopped');
  await servicePage(customer, service.id);
  await customer.getByRole('tab', { name: 'Snapshots', exact: true }).click();
  await customer.getByPlaceholder('Snapshot name', { exact: true }).fill('acceptance-snapshot');
  task = await mutation(customer, `/services/${service.id}/snapshots`, () => customer.getByRole('button', { name: 'Take snapshot', exact: true }).click());
  await fixture('state', { id: service.id, state: 'active', power: 'stopped', task: task.id, step: 'stopped-snapshot' });
  await customer.reload();
  await customer.getByRole('tab', { name: 'Snapshots', exact: true }).click();
  const snapshots = await data(customerContext, `/services/${service.id}/snapshots`);
  assert.equal(snapshots.items.length, 1);
  await customer.getByRole('button', { name: 'Restore', exact: true }).click();
  dialog = customer.getByRole('dialog');
  await dialog.getByRole('textbox').fill('manual-e2e');
  task = await mutation(customer, `/snapshots/${snapshots.items[0].id}/restore`, () => dialog.getByRole('button', { name: 'Restore', exact: true }).click());
  await fixture('state', { id: service.id, state: 'active', power: 'stopped', task: task.id, step: 'restore-snapshot' });
  await customer.reload();
  await customer.getByRole('tab', { name: 'Snapshots', exact: true }).click();
  await customer.getByRole('button', { name: 'Delete', exact: true }).click();
  task = await mutation(customer, `/snapshots/${snapshots.items[0].id}`, () => customer.getByRole('dialog').getByRole('button', { name: 'Delete', exact: true }).click());
  await fixture('state', { id: service.id, state: 'active', power: 'stopped', task: task.id, step: 'delete-snapshot' });
  await power(customer, service, 'Start', 'running');

  await servicePage(customer, service.id);
  let consoleTimeout;
  const handshake = new Promise((resolve, reject) => {
    consoleTimeout = setTimeout(() => reject(new Error('node VNC handshake deadline exceeded')), 30000);
    customer.once('popup', popup => popup.once('websocket', socket => {
      if (new URL(socket.url()).protocol !== 'wss:') return reject(new Error('console websocket lacks TLS'));
      socket.once('framereceived', frame => {
        if (!Buffer.from(frame.payload).toString('ascii').startsWith('RFB ')) return reject(new Error('node VNC banner absent'));
        resolve(popup);
      });
    }));
  });
  await customer.getByRole('button', { name: 'Console', exact: true }).click();
  const popup = await handshake;
  clearTimeout(consoleTimeout);
  await popup.close();
  await fixture('state', { id: service.id, state: 'active', power: 'running', step: 'console-vnc-handshake' });

  await power(customer, service, 'Shut down', 'stopped');
  await servicePage(customer, service.id);
  await menu(customer, 'Change plan');
  await customer.getByRole('dialog').getByRole('radio', { name: /E2E Large/ }).click();
  const upgrade = await mutation(customer, `/services/${service.id}/upgrade`, () => customer.getByRole('dialog').getByRole('button', { name: 'Confirm change', exact: true }).click());
  assert.equal(upgrade.status, 'invoice');
  await payManual(staff, upgrade.invoice.id);
  await fixture('state', { id: service.id, state: 'active', power: 'stopped', step: 'plan-resize', cpu: 2, memory: 768, disk: 6 });
  await power(customer, service, 'Start', 'running');

  const renewal = await fixture('overdue', { id: service.id });
  await servicePage(customer, service.id);
  await expect(customer.getByText('Service suspended', { exact: true })).toBeVisible();
  const csrf = (await data(customerContext, '/session')).csrf_token;
  const denied = await customerContext.request.post(setup.origin + `/api/v1/services/${service.id}/power`, {
    headers: { Origin: setup.origin, 'X-CSRF-Token': csrf, 'Idempotency-Key': crypto.randomUUID() }, data: { action: 'start' },
  });
  assert.equal(denied.status(), 409, 'suspended start was not blocked');
  await fixture('state', { id: service.id, state: 'suspended', power: 'stopped', step: 'overdue-start-blocked' });
  await payManual(staff, renewal.id);
  await fixture('state', { id: service.id, state: 'active', power: 'running', step: 'paid-auto-unsuspend' });

  for (const item of [service, second]) {
    await servicePage(customer, item.id);
    await menu(customer, 'Cancel service');
    dialog = customer.getByRole('dialog');
    await dialog.getByRole('radio', { name: /^Immediately/ }).click();
    await dialog.getByLabel('Reason', { exact: true }).fill('Acceptance complete');
    await mutation(customer, `/services/${item.id}/cancellation`, () => dialog.getByRole('button', { name: 'Request cancellation', exact: true }).click());
    await fixture('terminate', { id: item.id });
  }
  await fixture('finish');
} catch (error) {
  let message = String(error.stack || error);
  for (const context of contexts) for (const page of context.pages()) {
    if (page.isClosed()) continue;
    const url = new URL(page.url());
    const buttons = await page.getByRole('button').allTextContents().catch(() => []);
    message += `\nPage ${url.origin}${url.pathname}; language=${await page.locator('html').getAttribute('lang').catch(() => '')}; buttons=${JSON.stringify(buttons).slice(0, 1200)}`;
  }
  if (setup) for (const [key, value] of Object.entries(setup)) if (key.endsWith('Password')) message = message.replaceAll(value, '<redacted>');
  await fixture('failed', { message }).catch(() => {});
  process.exitCode = 1;
} finally {
  await Promise.allSettled(contexts.map(context => context.close()));
  lines.close();
}
