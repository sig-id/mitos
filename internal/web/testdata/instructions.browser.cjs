// Run with an installed Playwright: PLAYWRIGHT_MODULE=/path/to/playwright node --test this-file
const {test, before, after} = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const {chromium} = require(process.env.PLAYWRIGHT_MODULE || '../../../tools/terminal-eval/node_modules/playwright');
const asset = name => path.join(__dirname, '../static', name);
let browser;
before(async () => {
 browser = await chromium.launch({headless:true, ...(process.env.CHROMIUM_BIN ? {executablePath:process.env.CHROMIUM_BIN} : {})});
});
after(async () => { await browser?.close(); });
async function fixture(t, options = {}) {
 const page = await browser.newPage(options);
 const errors = [];
 page.on('pageerror', error => errors.push(error.message));
 t.after(async () => { await page.close(); assert.deepEqual(errors, []); });
 await page.setContent('<meta name="viewport" content="width=device-width,initial-scale=1"><form><input aria-label="Name"><mitos-instructions label="Time limit">Includes **waiting time** and `retries`.</mitos-instructions><button type="submit">Save</button></form>');
 await page.addStyleTag({path:asset('operating.css')});
 await page.addStyleTag({path:asset('instructions.css')});
 await page.addScriptTag({path:asset('analysis-format.js')});
 await page.addScriptTag({path:asset('instructions.js')});
 await page.evaluate(() => {
  window.submits = 0;
  document.querySelector('form').onsubmit = event => { event.preventDefault(); window.submits++; };
 });
 return page;
}
const isOpen = page => page.locator('.instructions-popup').first().evaluate(node => node.matches(':popover-open'));

test('short Markdown is light DOM help, available on focus and dismissible with Escape', async t => {
 const page = await fixture(t);
 assert.equal(await page.locator('mitos-instructions').evaluate(node => node.shadowRoot), null);
 assert.equal(await page.locator('.instructions-popup strong').last().textContent(), 'waiting time');
 assert.equal(await isOpen(page), false);
 await page.getByRole('button', {name:'Time limit help'}).focus();
 assert.equal(await isOpen(page), true);
 assert.equal(await page.getByRole('tooltip').count(), 1);
 await page.keyboard.press('Escape');
 assert.equal(await isOpen(page), false);
 assert.equal(await page.getByRole('button', {name:'Time limit help'}).evaluate(node => node === document.activeElement), true);
});

test('hover help remains available under the pointer and closes on departure', async t => {
 const page = await fixture(t);
 await page.getByRole('button', {name:'Time limit help'}).hover();
 assert.equal(await isOpen(page), true);
 await page.getByRole('tooltip').hover();
 await page.waitForTimeout(150);
 assert.equal(await isOpen(page), true);
 await page.mouse.move(800, 500);
 await page.waitForFunction(() => !document.querySelector('.instructions-popup').matches(':popover-open'));
});

test('long instructions open on click, support Markdown links, and never submit a form', async t => {
 const page = await fixture(t);
 await page.evaluate(() => {
  document.querySelector('mitos-instructions').value = '1. Review the **goal**.\n2. Open [Background jobs](/services).\n\nChanges apply to future work. Check the saved instructions before starting another run.';
 });
 const trigger = page.getByRole('button', {name:'Time limit help'});
 await trigger.focus();
 assert.equal(await isOpen(page), false);
 await trigger.click();
 assert.equal(await page.getByRole('region', {name:'Time limit'}).isVisible(), true);
 assert.equal(await page.getByRole('link', {name:'Background jobs'}).getAttribute('href'), '/services');
 await page.getByRole('button', {name:'Close instructions'}).click();
 assert.equal(await isOpen(page), false);
 assert.equal(await trigger.evaluate(node => node === document.activeElement), true);
 assert.equal(await page.evaluate(() => window.submits), 0);
});

test('Markdown treats HTML and executable URLs as text', async t => {
 const page = await fixture(t);
 await page.evaluate(() => {
  document.querySelector('mitos-instructions').value = '<img src=x onerror=alert(1)> [bad](javascript:alert) [remote](//example.com) [slash](/\\example.com)';
 });
 assert.equal(await page.locator('.instructions-popup img,.instructions-popup a').count(), 0);
 assert.match(await page.locator('.instructions-popup').textContent(), /<img/);
});

test('dynamic instances, updates, reconnects and light dismissal work', async t => {
 const page = await fixture(t);
 await page.evaluate(() => {
  const help = MitosInstructions.create('Additional guidance.', 'Dynamic', 'popover');
  document.body.append(help);
  help.value = 'Updated **guidance**.';
  help.remove();
  document.body.append(help);
 });
 await page.getByRole('button', {name:'Dynamic help'}).click();
 assert.equal(await page.getByRole('region', {name:'Dynamic'}).isVisible(), true);
 await page.getByRole('textbox', {name:'Name'}).click();
 assert.equal(await page.getByRole('region', {name:'Dynamic'}).isVisible(), false);
 await page.getByRole('button', {name:'Dynamic help'}).click();
 await page.evaluate(() => document.querySelector('mitos-instructions[label=Dynamic]').remove());
 assert.equal(await page.locator('.instructions-popup:popover-open').count(), 0);
});

test('touch help stays within a narrow viewport and works inside a modal', async t => {
 const page = await fixture(t, {viewport:{width:360,height:640}, hasTouch:true, isMobile:true});
 await page.evaluate(() => {
  const dialog = document.createElement('dialog');
  const help = MitosInstructions.create('Read the instructions and check the result before continuing. '.repeat(15), 'Modal instructions');
  dialog.append(help); document.body.append(dialog); dialog.showModal();
 });
 await page.getByRole('button', {name:'Modal instructions help'}).tap();
 const popup = page.getByRole('region', {name:'Modal instructions'});
 const rect = await popup.boundingBox();
 assert.ok(rect.x >= 0 && rect.y >= 0 && rect.x + rect.width <= 360 && rect.y + rect.height <= 640);
 await page.getByRole('button', {name:'Close instructions'}).tap();
 assert.equal(await popup.isVisible(), false);
 assert.equal(await page.locator('dialog').evaluate(node => node.open), true);
});
