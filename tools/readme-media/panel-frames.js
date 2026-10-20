// Captures the panel demo as frames: node panel-frames.js <adminToken> <outDir>
const { chromium } = require('playwright');
const fs = require('fs');
const BASE = 'http://127.0.0.1:8090';
const [token, out, appName = 'doc-search'] = process.argv.slice(2);
fs.mkdirSync(out, { recursive: true });
const frames = [];
let n = 0;
async function shot(p, caption, ms = 1600) {
  const file = `${out}/${String(++n).padStart(2, '0')}.png`;
  await p.screenshot({ path: file });
  frames.push({ file, caption, ms });
}

(async () => {
  const b = await chromium.launch();
  const p = await b.newPage({ viewport: { width: 1100, height: 800 } });
  await p.goto(BASE + '/');
  await shot(p, 'Sign in with the gateway\'s admin token', 1400);
  await p.fill('input[type=password]', token);
  await p.click('text=Sign in');
  await p.waitForSelector('h2:has-text("Apps")');
  await p.waitForTimeout(300);
  await shot(p, 'Apps: each one has its own key and budget', 1800);

  const name = appName;
  await p.click('text=New app');
  await p.waitForSelector('[role=dialog]');
  await p.fill('[role=dialog] input >> nth=0', appName.slice(0, 4));
  await shot(p, 'Create an app...', 900);
  await p.fill('[role=dialog] input >> nth=0', name);
  const nums = p.locator('[role=dialog] .row input');
  await nums.nth(0).fill('100');
  await nums.nth(1).fill('50000');
  await nums.nth(2).fill('0');
  await p.getByRole('dialog').getByRole('button', { name: 'ok', exact: true }).click();
  await shot(p, '...set limits, cache TTL and provider order', 2200);
  await p.click('text=Create app');
  const key = (await p.textContent('[data-testid=api-key]')).trim();
  await p.waitForTimeout(300);
  await shot(p, 'The key is shown once. Only its SHA-256 is stored', 2400);
  await p.click("text=I've saved it");

  // real calls through the gateway with that key
  const lines = [];
  let ok = 0;
  for (let i = 0; i < 40; i++) {
    const r = await fetch(BASE + '/v1/chat/completions', { method: 'POST',
      headers: { Authorization: 'Bearer ' + key, 'Content-Type': 'application/json' },
      body: JSON.stringify({ model: 'fast', messages: [{ role: 'user', content: 'demo ' + i }] }) });
    if (i === 0) {
      const j = await r.json();
      if (!j.choices) { console.error('first call failed', r.status, JSON.stringify(j)); }
      lines.push('$ curl localhost:8090/v1/chat/completions \\');
      lines.push('    -H "Authorization: Bearer fl_' + key.slice(3, 9) + '..." \\');
      lines.push('    -d \'{"model":"fast","messages":[...]}\'');
      lines.push('HTTP ' + r.status + '  provider=' + r.headers.get('x-freeloader-provider') + '  cache=' + r.headers.get('x-freeloader-cache') + '  attempts=' + r.headers.get('x-freeloader-attempts') + '  model=' + j.model);
      lines.push('"content": ' + JSON.stringify(j.choices[0].message.content).slice(0, 70));
    }
    if (r.status === 200) ok++;
  }
  lines.push('... 40 requests sent, ' + ok + ' answered 200');
  fs.writeFileSync(out + '/terminal.json', JSON.stringify(lines));
  frames.push({ terminal: lines, caption: 'Point any OpenAI client at the gateway', ms: 3000 });

  // usage appears
  await p.click('nav >> text=Usage');
  const filters = () => p.locator('.filters label');
  for (let i = 0; i < 40; i++) {
    await p.reload(); await p.waitForSelector('h2:has-text("Usage")');
    const opts = await filters().nth(1).locator('option').allTextContents();
    if (opts.includes(name)) break;
    await p.waitForTimeout(1000);
  }
  await filters().nth(1).locator('select').selectOption({ label: name });
  await p.waitForTimeout(300);
  await shot(p, 'Usage: the ledger rolls events up per app, provider and day', 3000);
  await p.click('nav >> text=Providers');
  await p.waitForSelector('article');
  await p.waitForTimeout(400);
  await shot(p, 'Providers: state and today\'s burn against each limit', 2600);
  await p.click('nav >> text=Apps');
  await p.waitForSelector('table');
  await shot(p, 'Disable an app and it stops being served', 1500);
  await p.click(`button[aria-label="Disable ${name}"]`);
  await p.waitForSelector('text=Disabled');
  await shot(p, 'Disable an app and it stops being served', 1800);
  fs.writeFileSync(out + '/frames.json', JSON.stringify(frames, null, 1));
  console.log('frames', frames.length, 'ok', ok);
  await b.close();
})().catch(e => { console.error('FAILED', e); process.exit(1); });
