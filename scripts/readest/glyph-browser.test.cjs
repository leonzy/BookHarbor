// Real-book validation plus explicitly injected layout fixtures; never edits library files.
const { chromium } = require('../../app/node_modules/@playwright/test');
const { crc32, deflateSync } = require('node:zlib');
const fs = require('node:fs');
const assert = require('node:assert/strict');
const base = process.env.READEST_BASE_URL;
const id = process.env.READEST_GLYPH_BOOK;
const baseline = process.env.READEST_GLYPH_EXPECT === 'baseline';
assert(base && /^[1-9]\d*$/.test(id || ''), 'Set READEST_BASE_URL and READEST_GLYPH_BOOK');

function png(width, height) {
  const chunk = (name, data) => { const type = Buffer.from(name), size = Buffer.alloc(4), crc = Buffer.alloc(4); size.writeUInt32BE(data.length); crc.writeUInt32BE(crc32(Buffer.concat([type, data]))); return Buffer.concat([size, type, data, crc]); };
  const header = Buffer.alloc(13); header.writeUInt32BE(width); header.writeUInt32BE(height, 4); header[8] = 8; header[9] = 2;
  const raw = Buffer.alloc((width * 3 + 1) * height, 128); for (let y = 0; y < height; y++) raw[y * (width * 3 + 1)] = 0;
  return Buffer.concat([Buffer.from('89504e470d0a1a0a', 'hex'), chunk('IHDR', header), chunk('IDAT', deflateSync(raw)), chunk('IEND', Buffer.alloc(0))]);
}
function mobi(html) {
  const text = Buffer.from(html), title = Buffer.from('Glyph fixture'), header = Buffer.alloc(256 + title.length);
  header.writeUInt16BE(1); header.writeUInt32BE(text.length, 4); header.writeUInt16BE(1, 8); header.writeUInt16BE(4096, 10);
  header.write('MOBI', 16); header.writeUInt32BE(232, 20); header.writeUInt32BE(2, 24); header.writeUInt32BE(65001, 28); header.writeUInt32BE(42, 32); header.writeUInt32BE(6, 36);
  header.writeUInt32BE(256, 84); header.writeUInt32BE(title.length, 88); header.writeUInt32BE(2, 108); header.writeUInt32BE(0xffffffff, 244); title.copy(header, 256);
  const records = [header, text, png(39, 44), png(87, 44), png(240, 180), png(320, 400), png(352, 44)];
  const pdb = Buffer.alloc(78 + records.length * 8 + 2); pdb.write('Glyph fixture'); pdb.write('BOOKMOBI', 60); pdb.writeUInt16BE(records.length, 76);
  let offset = pdb.length; records.forEach((record, index) => { pdb.writeUInt32BE(offset, 78 + index * 8); offset += record.length; });
  return Buffer.concat([pdb, ...records]);
}
function fixtureBook() {
  const image = (name, attrs = '') => `<img id="${name}" recindex="1" src="glyph.png" width="93" height="105" ${attrs}/>`;
  const paragraph = text => `<p width="2em" height="2em" style="font-size:16px">${text}</p>`;
  const content = paragraph(`<font style="font-size:20px">白</font>${image('wrapped')}<font style="font-size:20px">洲。</font>`) +
    paragraph(`白${image('plain')}洲。`) + paragraph(`<font style="font-size:20px">金</font>${image('multi').replace('recindex="1"', 'recindex="2"')}<font style="font-size:20px">飞。</font>`) +
    paragraph(`<font style="font-size:20px"><span>白${image('nested')}洲。</span></font>`) +
    paragraph(`<font style="font-size:20px">图字</font>${image('wide').replace('recindex="1"', 'recindex="5"')}<font style="font-size:20px">测试。</font>`) +
    paragraph('<img id="large" recindex="3" src="large.png" width="240" height="180"/>') +
    paragraph('<img id="cover" recindex="4" src="cover.png" width="320" height="400"/>') +
    paragraph(`图${image('float', 'style="float:right"')}示。`) + paragraph(`图<a href="#top">${image('link')}</a>示。`) +
    paragraph(`图${image('block', 'style="display:block"')}示。`);
  return mobi(`<html><head><title>图字排版测试</title></head><body>${content}</body></html>`);
}

(async () => {
  const browser = await chromium.launch(); const results = [];
  try {
    for (const colorScheme of ['light', 'dark']) for (const [width, height] of [[1280, 900], [412, 915]]) {
      const context = await browser.newContext({ colorScheme, viewport: { width, height }, isMobile: width === 412, hasTouch: width === 412 });
      const page = await context.newPage();
      if (!baseline) {
        await page.goto(`${base}/read/${id}?format=mobi`);
        await page.waitForFunction(() => document.querySelector('foliate-view')?.lastLocation?.cfi, null, { timeout: 90000 });
        const view = page.locator('foliate-view').first();
        await view.evaluate(async el => {
          const flat = xs => xs.flatMap(x => [x, ...flat(x.subitems || [])]);
          await el.goTo(flat(el.book.toc).filter(x => x.label === '望江南')[2].href);
        });
        await page.waitForFunction(() => document.querySelector('foliate-view').renderer.getContents().some(x => x.doc.querySelector('img[recindex="00012"].readest-inline-glyph')), null, { timeout: 30000 });
        const real = await view.evaluate(el => el.renderer.getContents().flatMap(x => [...x.doc.querySelectorAll('img.readest-inline-glyph')]).map(n => {
          const r = n.getBoundingClientRect(); return { w: r.width, h: r.height, nw: n.naturalWidth, nh: n.naturalHeight, font: parseFloat(n.ownerDocument.defaultView.getComputedStyle(n).fontSize) };
        }));
        assert(real.length > 0);
        for (const item of real) { assert(Math.abs(item.h - item.font) < 0.2); assert(Math.abs(item.w / item.h - item.nw / item.nh) < 0.01); }
        console.log(JSON.stringify({ realBook: true, width, glyphs: real }));
        if (process.env.READEST_SCREENSHOT_DIR) { fs.mkdirSync(process.env.READEST_SCREENSHOT_DIR, { recursive: true }); await page.screenshot({ path: `${process.env.READEST_SCREENSHOT_DIR}/glyph-real-${width}-${colorScheme}.png` }); }
      }
      const bytes = fixtureBook();
      await page.route(`**/api/book/${id}/reader-bootstrap?*`, route => route.fulfill({ json: {
        err: 'ok', schema: 'talebook.reader.bootstrap.v1', engine: 'readest', book: { id: +id, format: 'mobi', revision: 'glyph-fixture' },
        resource: { url: `/read/resource/${id}.mobi?revision=glyph-fixture`, mime: 'application/x-mobipocket-ebook', range: true }, navigation: { back: `/book/${id}`, fallback: `/book/${id}` },
      } }));
      await page.route(`**/read/resource/${id}.mobi?revision=glyph-fixture`, route => {
        const range = route.request().headers().range?.match(/^bytes=(\d+)-(\d+)$/); const a = range ? +range[1] : 0, b = range ? Math.min(+range[2], bytes.length - 1) : bytes.length - 1;
        return route.fulfill({ status: range ? 206 : 200, headers: { 'Content-Type': 'application/x-mobipocket-ebook', 'Content-Length': String(b - a + 1), 'Accept-Ranges': 'bytes', ETag: '"glyph-fixture"', ...(range ? { 'Content-Range': `bytes ${a}-${b}/${bytes.length}` } : {}) }, body: route.request().method() === 'HEAD' ? '' : bytes.subarray(a, b + 1) });
      });
      await page.goto(`${base}/readest/talebook-launch.html?bookId=${id}&format=mobi`);
      await page.waitForFunction(() => document.querySelector('foliate-view')?.renderer?.getContents().some(x => x.doc.querySelector('#wrapped')?.complete && x.doc.querySelector('#cover')?.naturalWidth), null, { timeout: 60000 });
      const inspect = () => page.locator('foliate-view').evaluate(el => {
        const doc = el.renderer.getContents().find(x => x.doc.querySelector('#wrapped')).doc;
        return [...doc.querySelectorAll('img')].map(n => { const r = n.getBoundingClientRect(); return { id: n.id, glyph: n.classList.contains('readest-inline-glyph'), w: r.width, h: r.height, ratio: n.naturalWidth / n.naturalHeight, font: parseFloat(doc.defaultView.getComputedStyle(n).fontSize) }; });
      });
      const original = await inspect();
      if (!baseline) {
        for (const n of original.filter(x => ['wrapped', 'plain', 'multi', 'nested', 'wide'].includes(x.id))) { assert(n.glyph, `${n.id}: ${JSON.stringify(original)}`); assert(Math.abs(n.h - n.font) < 0.2); assert(Math.abs(n.w / n.h - n.ratio) < 0.01); }
        for (const n of original.filter(x => ['large', 'cover', 'float', 'link', 'block'].includes(x.id))) assert.equal(n.glyph, false, n.id);
        for (const size of [32, 12, 64]) {
          await page.locator('foliate-view').evaluate((el, size) => { const doc = el.renderer.getContents().find(x => x.doc.querySelector('#wrapped')).doc; let style = doc.querySelector('#qa-glyph-zoom'); if (!style) { style = doc.createElement('style'); style.id = 'qa-glyph-zoom'; doc.head.append(style); } style.textContent = `html,body,p{font-size:${size}px!important} font{font-size:${size * 1.25}px!important}`; }, size);
          const changed = await inspect();
          for (const n of changed.filter(x => x.glyph)) { assert(n.h <= n.font + 0.2); assert(n.h >= Math.min(size, 20) - 0.2); assert(Math.abs(n.w / n.h - n.ratio) < 0.01); }
        }
      }
      results.push({ width, colorScheme, images: original }); await context.close();
    }
    if (process.env.READEST_GLYPH_BASELINE && !baseline) {
      const before = JSON.parse(fs.readFileSync(process.env.READEST_GLYPH_BASELINE, 'utf8'));
      for (const row of results) for (const n of row.images.filter(x => ['large', 'cover', 'float', 'link', 'block'].includes(x.id))) {
        const previous = before.find(x => x.width === row.width && x.colorScheme === row.colorScheme).images.find(x => x.id === n.id);
        assert(Math.abs(n.w - previous.w) < 0.2 && Math.abs(n.h - previous.h) < 0.2, `normal illustration changed: ${n.id}`);
      }
    }
    if (process.env.READEST_GLYPH_RESULT) fs.writeFileSync(process.env.READEST_GLYPH_RESULT, JSON.stringify(results, null, 2));
    console.log(JSON.stringify({ fixture: true, baseline, widths: results.map(x => x.width), normalIllustrationsUnchanged: !baseline, zoom: !baseline }));
  } finally { await browser.close(); }
})().catch(error => { console.error(error.stack); process.exitCode = 1; });
