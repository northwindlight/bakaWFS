// Opt-in acceptance against an isolated function/catalog and synthetic users.
// No production config/users or original manga files are read by this script.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const { existsSync } = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { chromium } = require('playwright-core');
const args = process.argv.slice(2);
const option = (name, fallback) => { const i = args.indexOf(name); return i < 0 ? fallback : args[i + 1]; };
const root = path.resolve(__dirname, '../..');
(async () => {
  const credentials = JSON.parse(await fs.readFile(option('--credentials', path.join(root, 'dist/scf/validation-credentials.json'))));
  const output = path.resolve(option('--output', path.join(root, 'dist/scf/browser-validation')));
  await fs.mkdir(output, { recursive: true });
  const source = path.join(root, 'html');
  const server = http.createServer(async (req, res) => {
    try {
      const pathname = decodeURIComponent(new URL(req.url, 'http://localhost').pathname);
      if (pathname === '/runtime-config.js') { res.setHeader('Content-Type', 'text/javascript'); res.end('globalThis.BAKAWFS_CONFIG = ' + JSON.stringify({ apiBase: credentials.url }) + ';'); return; }
      const filename = path.resolve(source, '.' + (pathname === '/' ? '/index.html' : pathname));
      if (!filename.startsWith(source + path.sep)) { res.writeHead(403).end(); return; }
      const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.png': 'image/png', '.wasm': 'application/wasm' };
      res.setHeader('Content-Type', types[path.extname(filename)] || 'application/octet-stream');
      res.setHeader('Cache-Control', 'no-store'); res.end(await fs.readFile(filename));
    } catch { res.writeHead(404).end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser, page;
  try {
    browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || (existsSync('/usr/bin/chromium') ? '/usr/bin/chromium' : undefined), headless: true, args: ['--no-sandbox', '--no-proxy-server'] });
    page = await browser.newPage({ viewport: { width: 1280, height: 850 } });
    page.setDefaultTimeout(90000);
    const hosts = new Set(), errors = [], dialogs = [], providerAuthorization = [];
    let partRequests = 0, interrupted = false, thumbnailRequests = 0;
    page.on('console', message => { if (message.type() === 'error') console.log('Browser error:', message.text().replace(/https?:\/\/[^\s]+/g, '[URL]')); });
    page.on('pageerror', error => errors.push(error.name + ': ' + error.message.replace(/https?:\/\/[^\s]+/g, '[URL]')));
    page.on('dialog', async dialog => { dialogs.push(dialog.message()); console.log('UI dialog:', dialog.message().replace(/https?:\/\/[^\s]+/g, '[URL]')); await dialog.accept(); });
    page.on('response', response => { const u = new URL(response.url()); if (u.pathname.startsWith('/cloud/upload/') || u.hostname === 'upload.qiniup.com') console.log('Transfer response:', u.hostname, u.pathname.replace(/\/objects\/[^/]+/, '/objects/[key]').replace(/\/uploads\/[^/]+/, '/uploads/[id]'), response.status()); });
    page.on('requestfailed', request => { const u = new URL(request.url()); console.log('Network failure:', u.hostname, request.failure()?.errorText); });
    page.on('request', request => {
      const url = new URL(request.url()); hosts.add(url.hostname);
      if (url.pathname.startsWith('/thumb')) thumbnailRequests++;
      if (url.hostname !== new URL(credentials.url).hostname && url.hostname !== '127.0.0.1' && request.headers().authorization?.startsWith('Bearer ')) providerAuthorization.push(url.hostname);
      if (request.method() === 'PUT' && url.pathname.includes('/uploads/')) partRequests++;
    });
    // Inject one XHR network-error event before any part bytes are sent.
    // Avoid DevTools interception of multi-MiB binary request bodies.
    await page.addInitScript(() => {
      const open = XMLHttpRequest.prototype.open, send = XMLHttpRequest.prototype.send;
      XMLHttpRequest.prototype.open = function(method, url, ...rest) { this.__fixturePart = method === 'PUT' && String(url).includes('/uploads/'); return open.call(this, method, url, ...rest); };
      XMLHttpRequest.prototype.send = function(body) {
        if (this.__fixturePart && !globalThis.__partFailureInjected) {
          globalThis.__partFailureInjected = true;
          queueMicrotask(() => this.dispatchEvent(new ProgressEvent('error'))); return;
        }
        return send.call(this, body);
      };
    });
    await page.goto(`http://127.0.0.1:${server.address().port}/`, { waitUntil: 'domcontentloaded' });
    await page.getByLabel('用户名').fill(credentials.username);
    await page.getByLabel('密码').fill(credentials.password);
    await page.getByLabel('密码').press('Enter');
    await page.locator('.brand').waitFor(); console.log('PASS: cloud UI login');
    await page.getByLabel('搜索全部文件').fill('样本书');
    await page.waitForFunction(() => document.body.innerText.includes('找到 1 个结果'));
    await page.getByLabel('搜索全部文件').fill('');
    if (await page.getByLabel('切换文件视图').count()) await page.getByLabel('切换文件视图').click();
    const folder = '浏览器云端验收-' + Date.now();
    await page.getByLabel('新建文件夹').click();
    const modal = page.locator('.fixed').filter({ has: page.getByText('创建', { exact: true }) }).last();
    await modal.locator('input').fill(folder); await modal.getByText('创建', { exact: true }).click();
    await page.locator('.file-card').filter({ hasText: folder }).waitFor();
    await page.locator('.file-card').filter({ hasText: folder }).click();
    await page.getByText('琪露诺把这里清空了', { exact: true }).waitFor();
    const upload = async (name, mimeType, buffer) => {
      console.log('Uploading test fixture:', name);
      await page.getByRole('button', { name: /上传/ }).first().click();
      await page.locator('input[type=file]').setInputFiles({ name, mimeType, buffer });
      await page.locator('.file-card').filter({ hasText: name }).waitFor({ timeout: 240000 });
      const progress = page.locator('.fixed').filter({ has: page.getByText('正在搬运的任务', { exact: true }) });
      if (await progress.count()) await progress.getByRole('button', { name: '×', exact: true }).click();
      console.log('PASS: browser native upload', name);
    };
    await upload('中文 #100%.txt', 'text/plain', Buffer.from('浏览器直接访问七牛私有文件'));
    await page.locator('.file-card').filter({ hasText: '中文 #100%.txt' }).click();
    await page.getByText('浏览器直接访问七牛私有文件', { exact: true }).waitFor();
    await page.keyboard.press('Escape');
    const nativeDownload = new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('native download timed out')), 90000);
      const done = download => { clearTimeout(timer); resolve(download); };
      page.once('download', done); page.context().once('page', popup => popup.once('download', done));
    });
    await page.locator('.file-card').filter({ hasText: '中文 #100%.txt' }).getByRole('button', { name: '下载', exact: true }).click();
    const downloaded = await nativeDownload;
    assert.equal(downloaded.suggestedFilename(), '中文 #100%.txt');
    assert.equal(await fs.readFile(await downloaded.path(), 'utf8'), '浏览器直接访问七牛私有文件');
    console.log('PASS: native download preserves Unicode filename and bytes');
    await upload('multipart-browser.bin', 'application/octet-stream', Buffer.alloc(11 * 1024 * 1024, 42));
    interrupted = await page.evaluate(() => globalThis.__partFailureInjected);
    assert.ok(interrupted && partRequests >= 2, 'native part retry was not exercised');
    // Imported synthetic image and both derivative tiers must work in Chromium.
    const imageResult = await page.evaluate(async api => {
      const token = localStorage.getItem('baka_token');
      const response = await fetch(api + '/files/' + encodeURIComponent('样本书') + '/1.png', { headers: { Authorization: 'Bearer ' + token } });
      const blob = await response.blob(); const bitmap = await createImageBitmap(blob);
      return { status: response.status, type: blob.type, width: bitmap.width, height: bitmap.height, host: new URL(response.url).hostname };
    }, credentials.url);
    assert.equal(imageResult.status, 200); assert.ok(imageResult.width > 0 && imageResult.height > 0); assert.ok(imageResult.host === 'manga-data.northwind.site' || imageResult.host.endsWith('.qiniucs.com')); assert.equal(thumbnailRequests, 0, 'cloud UI requested thumbnails');
    for (const width of [1280, 390, 320]) {
      await page.setViewportSize({ width, height: 850 });
      await page.screenshot({ path: path.join(output, `cloud-${width}.png`), fullPage: true });
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'horizontal overflow at ' + width);
    }
    const cleanup = await page.evaluate(async ({ api, folder }) => {
      const response = await fetch(api + '/delete', { method: 'POST', headers: { Authorization: 'Bearer ' + localStorage.getItem('baka_token'), 'Content-Type': 'application/json' }, body: JSON.stringify({ path: folder, operation_id: crypto.randomUUID() }) });
      return response.status;
    }, { api: credentials.url, folder });
    assert.equal(cleanup, 200); assert.deepEqual(errors, []); assert.deepEqual(dialogs, []); assert.deepEqual(providerAuthorization, []);
    assert.ok(!hosts.has('www.northwind.site'), 'old self-hosted API was called');
    const report = { passed: true, widths: [1280, 390, 320], nativePartRetry: true, nativeDownload: true, directPrivateRead: true, originalImage: imageResult, thumbnails: false, hosts: [...hosts].sort(), jwtSentToStorage: false, testedAt: new Date().toISOString() };
    await fs.writeFile(path.join(output, 'report.json'), JSON.stringify(report, null, 2));
    console.log(JSON.stringify(report));
  } catch (error) { if (page) { await page.screenshot({ path: path.join(output, 'failure.png'), fullPage: true }).catch(() => {}); await fs.writeFile(path.join(output, 'failure.txt'), await page.locator('body').innerText().catch(() => '')).catch(() => {}); } throw error; } finally { if (browser) await browser.close(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { console.error(error.name + ': ' + error.message.replace(/https?:\/\/[^\s]+/g, '[URL]')); process.exitCode = 1; });
