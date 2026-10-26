// Opt-in real-site regression. Credentials, book contents and IDs stay in the environment.
const { chromium } = require('../../app/node_modules/@playwright/test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const base = process.env.READEST_BASE_URL;
const samples = JSON.parse(process.env.READEST_NATIVE_SAMPLES || '[]');
assert(base && process.env.READEST_PASSWORD && samples.length, 'Set READEST_BASE_URL, READEST_PASSWORD and READEST_NATIVE_SAMPLES');

(async () => {
  const browser = await chromium.launch();
  try {
    const context = await browser.newContext(process.env.READEST_MOBILE === '1' ? {
      viewport: { width: 412, height: 915 }, isMobile: true, hasTouch: true,
      userAgent: 'Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Mobile Safari/537.36',
    } : { viewport: { width: 1280, height: 900 } });
    assert.equal((await (await context.request.post(base + '/api/user/sign_in', {
      form: { username: process.env.READEST_USERNAME || 'admin', password: process.env.READEST_PASSWORD },
    })).json()).err, 'ok');
    const conversions = [];
    context.on('request', r => { if (r.method() === 'POST' && /\/convert(?:\?|$)/.test(r.url())) conversions.push(r.url()); });
    for (const { id, format } of samples) {
      const page = await context.newPage();
      const originalFormats = (await (await context.request.get(`${base}/api/book/${id}`)).json()).book.fmt;
      const bootstrap = await (await context.request.get(`${base}/api/book/${id}/reader-bootstrap?engine=readest&format=${format}`)).json();
      assert.equal(bootstrap.err, 'ok');
      assert.equal(bootstrap.book.format, format);
      const head = await context.request.head(base + bootstrap.resource.url);
      assert.equal(head.status(), 200);
      assert.equal(head.headers()['content-type'], bootstrap.resource.mime);
      assert(head.headers().etag);
      const range = await context.request.get(base + bootstrap.resource.url, { headers: { Range: 'bytes=0-15' } });
      assert.equal(range.status(), 206);
      assert.equal((await range.body()).length, 16);
      const explicitFormat = originalFormats.length > 1 ? `&format=${format}` : '';
      await page.goto(`${base}/read/${id}?reader=readest${explicitFormat}`);
      await page.waitForFunction(() => {
        const view = document.querySelector('foliate-view');
        return view?.book?.sections?.length && view?.lastLocation?.cfi;
      }, null, { timeout: 90000 });
      const view = page.locator('foliate-view').first();
      const opened = await view.evaluate(async el => {
        const before = el.lastLocation.cfi;
        const contents = el.renderer.getContents();
        const text = contents.map(x => x.doc.body?.innerText || x.doc.documentElement?.textContent || '').join('').length;
        await el.next();
        return { before, text, sections: el.book.sections.length, toc: el.book.toc?.length || 0 };
      });
      await page.waitForFunction(before => document.querySelector('foliate-view').lastLocation.cfi !== before, opened.before);
      if (opened.toc) {
        const expectedIndex = await view.evaluate(async el => {
          const flatten = xs => xs.flatMap(x => [x, ...flatten(x.subitems || [])]);
          const toc = flatten(el.book.toc);
          const chapter = toc.find(x => /第一章|第一回|chapter\s*1\b/i.test(String(x.label)));
          const target = (chapter || toc[Math.min(1, toc.length - 1)]).href;
          const resolved = await el.goTo(target);
          return resolved?.index;
        });
        assert(Number.isInteger(expectedIndex), 'TOC target must resolve');
        await page.waitForFunction(index => document.querySelector('foliate-view').renderer.getContents().some(x => x.index === index), expectedIndex);
      }
      assert(await view.evaluate(el => el.renderer.getContents().some(x => (x.doc.querySelector('body')?.textContent || '').trim().length > 80)), 'Actual body text must be present after chapter navigation');
      console.log(JSON.stringify({ format, sections: opened.sections, toc: opened.toc, body: true, pagination: true, conversionRequests: conversions.length }));
      if (process.env.READEST_SCREENSHOT_DIR) {
        fs.mkdirSync(process.env.READEST_SCREENSHOT_DIR, { recursive: true });
        await page.screenshot({ path: `${process.env.READEST_SCREENSHOT_DIR}/native-${format}.png` });
      }
      assert.deepEqual((await (await context.request.get(`${base}/api/book/${id}`)).json()).book.fmt, originalFormats, 'Reading must not persist a converted format');
      await page.close();
    }
    assert.equal(conversions.length, 0, 'Opening native books must not start conversion');
    // Fault injection replaces only browser responses, never a source file or library record.
    const { id, format } = samples[0];
    for (const colorScheme of ['light', 'dark']) {
      const page = await context.newPage();
      await page.setViewportSize({ width: 320, height: 800 });
      await page.emulateMedia({ colorScheme, reducedMotion: 'reduce' });
      await page.route(`**/read/resource/${id}.${format}?*`, async route => {
        const data = Buffer.from('invalid native Kindle file\n'.repeat(128));
        const range = route.request().headers().range?.match(/^bytes=(\d+)-(\d+)$/);
        const start = range ? +range[1] : 0;
        const end = range ? Math.min(+range[2], data.length - 1) : data.length - 1;
        await route.fulfill({ status: range ? 206 : 200, headers: {
          'Content-Type': format === 'azw3' ? 'application/vnd.amazon.mobi8-ebook' : format === 'azw' ? 'application/vnd.amazon.ebook' : 'application/x-mobipocket-ebook',
          'Content-Length': String(end - start + 1), 'Accept-Ranges': 'bytes', ETag: '"browser-fault-only"',
          ...(range ? { 'Content-Range': `bytes ${start}-${end}/${data.length}` } : {}),
        }, body: route.request().method() === 'HEAD' ? '' : data.subarray(start, end + 1) });
      });
      await page.goto(`${base}/read/${id}?format=${format}`);
      const manual = page.locator(`a[href="/book/${id}?convert=epub"]`);
      await manual.waitFor({ state: 'visible', timeout: 90000 });
      assert.equal(await manual.locator('xpath=ancestor::*[@role="alert"][1]').count(), 1);
      await page.evaluate(() => { document.documentElement.style.fontSize = '32px'; });
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Recovery must reflow at 200%');
      await manual.scrollIntoViewIfNeeded();
      const box = await manual.boundingBox();
      assert(box && box.x >= 0 && box.x + box.width <= 320 && box.height >= 24);
      await manual.focus();
      await page.keyboard.press('Shift+Tab');
      await page.keyboard.press('Tab');
      assert(await manual.evaluate(el => document.activeElement === el), 'Recovery link must accept keyboard focus');
      assert(await manual.evaluate(el => getComputedStyle(el).outlineStyle !== 'none'), 'Keyboard focus must be visible');
      if (process.env.READEST_SCREENSHOT_DIR) await page.screenshot({ path: `${process.env.READEST_SCREENSHOT_DIR}/native-failure-${colorScheme}.png` });
      await page.keyboard.press('Enter');
      await page.waitForURL(`**/book/${id}?convert=epub`);
      await page.getByRole('dialog').waitFor({ state: 'visible' });
      assert.equal(conversions.length, 0, 'Manual fallback only opens the confirmation dialog');
      console.log(JSON.stringify({ failureInjection: true, colorScheme, recovery: true, manualConfirmation: true, conversionRequests: 0 }));
      await page.close();
    }
    await browser.close();
  } finally { await browser.close(); }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
