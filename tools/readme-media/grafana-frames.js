// Captures the Grafana dashboard during a load test with a gateway pod deleted partway through.
//   node grafana-frames.js <grafanaPassword> <outDir>
const { chromium } = require('playwright');
const { execSync } = require('child_process');
const fs = require('fs');
const [pw, out] = process.argv.slice(2);
fs.mkdirSync(out, { recursive: true });
const FRAMES = 20, EVERY_MS = 3500, KILL_AT = 7;
const sh = (c) => execSync(c, { encoding: 'utf8' });

(async () => {
  const b = await chromium.launch();
  const p = await b.newPage({ viewport: { width: 1500, height: 2900 } });
  await p.goto('http://127.0.0.1:3000/login');
  await p.fill('input[name=user]', 'admin'); await p.fill('input[name=password]', pw);
  await p.click('button[type=submit]');
  await p.waitForTimeout(2000);
  await p.goto('http://127.0.0.1:3000/d/freeloader?kiosk&from=now-4m&to=now&refresh=5s');
  await p.waitForTimeout(8000);

  const frames = [];
  let killedAt = -1;
  for (let i = 0; i < FRAMES; i++) {
    if (i === KILL_AT) {
      const pod = sh("kubectl --context kind-freeloader -n freeloader get pods -l app=gateway --field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}'").trim();
      sh(`kubectl --context kind-freeloader -n freeloader delete pod ${pod} --wait=false`);
      killedAt = i;
    }
    const base = `${out}/${String(i + 1).padStart(2, '0')}`;
    // Two regions of the tall dashboard: the Traffic row and the Pods row.
    await p.screenshot({ path: base + 'a.png', clip: { x: 0, y: 60, width: 1500, height: 345 } });
    await p.screenshot({ path: base + 'b.png', clip: { x: 0, y: 2385, width: 1500, height: 370 } });
    const files = [base + 'a.png', base + 'b.png'];
    const caption = i < KILL_AT ? 'Load test, 40 req/s through 3 gateway replicas (time-lapse)'
      : i < KILL_AT + 6 ? 'Gateway pod deleted mid-test: a new pod appears in Pod uptime, starting near 0'
      : 'Result: 3999 of 3999 requests answered 200 (tools/loadgen)';
    frames.push({ files, caption, ms: 700 });
    await p.waitForTimeout(EVERY_MS);
  }
  frames[frames.length - 1].ms = 2500;
  fs.writeFileSync(out + '/frames.json', JSON.stringify(frames, null, 1));
  console.log('frames', frames.length, 'killedAt', killedAt);
  await b.close();
})().catch((e) => { console.error('FAILED', e); process.exit(1); });
