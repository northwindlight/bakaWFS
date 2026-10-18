// npm ci; CHROMIUM_PATH=/usr/bin/chromium npm test
// 所有写操作只针对系统临时目录；不读取项目运行时配置。
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const { existsSync } = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const net = require('node:net');
const { spawn, execFileSync } = require('node:child_process');
const { chromium } = require('playwright-core');
const root = path.resolve(__dirname, '..');
const delay = ms => new Promise(r => setTimeout(r, ms));
async function freePort() {
  const server = net.createServer();
  await new Promise(r => server.listen(0, '127.0.0.1', r));
  const port = server.address().port;
  await new Promise(r => server.close(r));
  return port;
}

test('统一版：普通/漫画模式、搜索、登录退出、文件管理和窄屏', { timeout: 120000 }, async () => {
  const tmp = await fs.mkdtemp(path.join(os.tmpdir(), 'bakawfs-ui-'));
  let browser;
  const servers = [];
  try {
    execFileSync('go', ['build', '-o', path.join(tmp, 'bakaWFS'), '.'], { cwd: root });
    browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || (existsSync('/usr/bin/chromium') ? '/usr/bin/chromium' : undefined), headless: true, args: ['--no-sandbox'] });
    for (const mangaMode of [false, true]) {
      const dir = path.join(tmp, String(mangaMode));
      await fs.mkdir(path.join(dir, 'files', '测试作品'), { recursive: true });
      await fs.mkdir(path.join(dir, 'files', '普通目录'));
      const image = await fs.readFile(path.join(root, 'html/baka.png'));
      const imagePath = path.join(dir, 'files', '测试作品', '1.png');
      await fs.writeFile(imagePath, image);
      await fs.writeFile(path.join(dir, 'files', '测试作品', 'article.md'), '# 测试作品\n> 来源：本地测试\n');
      await fs.writeFile(path.join(dir, 'files', '笔记 #100%.txt'), '特殊文件名读取成功');
      await fs.writeFile(path.join(dir, 'files', '封面 #100%.png'), image);
      await fs.copyFile(path.join(tmp, 'bakaWFS'), path.join(dir, 'bakaWFS'));
      const port = await freePort();
      await fs.writeFile(path.join(dir, 'config.yaml'), `address: 127.0.0.1\nhttp_port: ${port}\nhttps_port: -1\nsecret: local-test-only\nfile_dir: files\nhtml_dir: built-in\ntemp_dir: .uploads\nusers_file: users.yaml\nauth_mode: true\nmanga_mode: ${mangaMode}\n`);
      await fs.writeFile(path.join(dir, 'users.yaml'), 'users:\n  - username: admin\n    password: local-test-only\n    role: admin\n');
      let log = '';
      const server = spawn(path.join(dir, 'bakaWFS'), ['run'], { cwd: dir });
      servers.push(server);
      server.stdout.on('data', b => { log += b; }); server.stderr.on('data', b => { log += b; });
      const base = `http://127.0.0.1:${port}`;
      let ready = false;
      for (let n = 0; n < 100; n++) {
        try { if ((await fetch(base + '/api/config')).ok) { ready = true; break; } } catch {}
        await delay(50);
      }
      assert.ok(ready, log);
      const context = await browser.newContext({ viewport: { width: 1280, height: 850 } });
      const page = await context.newPage();
      const problems = [];
      page.on('pageerror', e => problems.push(e.message));
      page.on('console', msg => { if (msg.type() === 'warning' && msg.text().includes('[Vue warn]')) problems.push(msg.text()); });
      page.on('dialog', dialog => dialog.dismiss());
      await page.route('**/*', route => {
        if (route.request().url().startsWith(base + '/')) return route.continue();
        problems.push('Unexpected external request: ' + route.request().url());
        return route.abort();
      });
      await page.goto(base + '/html/');
      assert.equal(await page.evaluate(async () => {
        const { hashFile } = await import('./hash.js');
        return hashFile(new File([], 'empty'));
      }), 'ef46db3751d8e999');
      const digest = await page.evaluate(async () => {
        const { hashFile } = await import('./hash.js');
        return hashFile(new File([new Uint8Array(4 * 1024 * 1024 + 17).fill(123)], 'test.bin'));
      });
      const hasher = await require('xxhash-wasm')();
      assert.equal(digest, hasher.h64Raw(new Uint8Array(4 * 1024 * 1024 + 17).fill(123)).toString(16).padStart(16, '0'));
      await page.getByLabel('用户名').fill('admin');
      await page.getByLabel('密码').fill('local-test-only');
      await page.getByLabel('密码').press('Enter');
      await page.locator('.brand').waitFor();
      await page.getByLabel('搜索全部文件').fill('测试作品');
      await page.locator(mangaMode ? '.poster' : '.file-toolbar ~ div .cursor-pointer').first().waitFor();
      await page.waitForFunction(() => document.body.innerText.includes('找到 1 个结果'));
      assert.equal(await page.locator('.poster').count(), mangaMode ? 1 : 0);
      await page.getByLabel('搜索全部文件').fill('');
      if (mangaMode) {
        await page.screenshot({ path: path.join(tmp, 'manga-covers.png'), fullPage: true });
        await page.getByLabel('切换文件视图').click();
      }
      await page.locator('.file-card').filter({ hasText: '笔记 #100%.txt' }).click();
      await page.getByText('特殊文件名读取成功', { exact: true }).waitFor();
      await page.keyboard.press('Escape');
      await page.locator('.file-card').filter({ hasText: '封面 #100%.png' }).click();
      await page.waitForFunction(() => document.querySelector('.fixed img')?.complete && document.querySelector('.fixed img')?.naturalWidth > 0);
      await page.keyboard.press('Escape');
      await page.getByLabel('新建文件夹').click();
      const modal = page.locator('.fixed').filter({ has: page.getByText('创建', { exact: true }) }).last();
      await modal.locator('input').fill('新建测试');
      await modal.getByText('创建', { exact: true }).click();
      await page.waitForFunction(() => document.body.innerText.includes('新建测试'));
      assert.ok((await fs.stat(path.join(dir, 'files', '新建测试'))).isDirectory());
      for (const width of [1280, 390, 320]) {
        await page.setViewportSize({ width, height: 850 });
        await page.screenshot({ path: path.join(tmp, `mode-${mangaMode}-${width}.png`), fullPage: true });
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `overflow at ${width}, manga=${mangaMode}`);
      }
      const token = await page.evaluate(() => localStorage.getItem('baka_token'));
      const userAgent = await page.evaluate(() => navigator.userAgent);
      const thumb = await fetch(base + '/thumb/测试作品/1.png?size=mid', { headers: { Authorization: 'Bearer ' + token, 'User-Agent': userAgent } });
      assert.equal(thumb.status, mangaMode ? 200 : 404);
      const specialThumb = await fetch(base + '/thumb/' + encodeURIComponent('封面 #100%.png'), { headers: { Authorization: 'Bearer ' + token, 'User-Agent': userAgent } });
      assert.equal(specialThumb.status, mangaMode ? 200 : 404);
      const after = await fs.readFile(imagePath);
      assert.equal(image.equals(after), !mangaMode, 'original-image mutation boundary');
      for (const target of ['.', 'sub/..']) {
        const res = await fetch(base + '/delete', { method: 'POST', headers: { Authorization: 'Bearer ' + token, 'User-Agent': userAgent }, body: JSON.stringify({ path: target }) });
        assert.equal(res.status, 400);
      }
      await page.locator('.file-card').filter({ hasText: '新建测试' }).getByRole('button', { name: '操作', exact: true }).click();
      await page.locator('.js-ops-panel').getByRole('button', { name: '删除' }).click();
      await page.getByRole('button', { name: '删除', exact: true }).click();
      await page.waitForFunction(() => !document.body.innerText.includes('新建测试'));

      await assert.rejects(fs.stat(path.join(dir, 'files', '新建测试')), { code: 'ENOENT' });
      await page.evaluate(() => localStorage.setItem('unrelated-setting', 'keep'));
      await page.locator('.account-btn').click();
      await page.getByLabel('用户名').waitFor();
      assert.equal(await page.evaluate(() => localStorage.getItem('unrelated-setting')), 'keep');
      assert.deepEqual(problems, []);
      await context.close();
    }
    console.log('Screenshots:', tmp);
  } finally {
    await browser?.close();
    for (const server of servers) server.kill('SIGTERM');
    // 留下截图和隔离测试目录供人工复核；无生产数据。
  }
});
