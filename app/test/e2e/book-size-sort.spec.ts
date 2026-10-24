import { test, expect } from '@playwright/test';

test.beforeEach(async ({ request }) => {
    await request.post(`${process.env.MOCK_API_URL || 'http://127.0.0.1:8080'}/_test/reset`, { data: { installed: true, loggedIn: true } });
});
test('size sorting resets paging, retains filters, and persists on reload', async ({ page }) => {
    const calls: URLSearchParams[] = [];
    await page.route('**/api/library?*', route => {
        const q = new URL(route.request().url()).searchParams;
        calls.push(q);
        return route.fulfill({ contentType: 'application/x-ndjson', body: [
            { err: 'ok', total: 180 },
            { id: 1, title: '大小排序图书', img: '/get/cover/1', format_sizes: { EPUB: 1024 } },
        ].map(row => JSON.stringify(row)).join('\n') + '\n' });
    });
    await page.goto('/library/local?publisher=Press&start=120');
    await page.getByTestId('book-size-sort').getByRole('combobox').first().click();
    await page.getByRole('option', { name: '大小：从大到小', exact: true }).click();
    await expect.poll(() => calls.some(q => q.get('sort') === 'size_desc' && q.get('start') === '0' && q.get('publisher') === 'Press')).toBe(true);
    const jump = page.getByTestId('page-jump').last();
    await jump.locator('input').fill('2');
    await jump.locator('input').press('Enter');
    await expect.poll(() => calls.some(q => q.get('sort') === 'size_desc' && q.get('start') === '60')).toBe(true);
    await page.reload();
    await expect(page.getByTestId('book-size-sort')).toContainText('大小：从大到小');
    await expect(page.getByTestId('book-card').first()).toHaveAttribute('target', '_blank');
    await page.setViewportSize({ width: 320, height: 900 });
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    await page.getByTestId('book-size-sort').getByRole('combobox').first().click();
    await page.getByRole('option', { name: '大小：从小到大', exact: true }).click();
    await expect.poll(() => calls.some(q => q.get('sort') === 'size_asc' && q.get('start') === '0')).toBe(true);
});
test('shared list sorting survives browser back', async ({ page }) => {
    await page.goto('/recent?start=60');
    await page.getByTestId('book-size-sort').getByRole('combobox').first().click();
    await page.getByRole('option', { name: '大小：从小到大', exact: true }).click();
    await expect(page).toHaveURL(/sort=size_asc/);
    expect(new URL(page.url()).searchParams.has('start')).toBe(false);
    await page.goBack();
    await expect(page).toHaveURL(/start=60/);
    await expect(page.getByTestId('book-size-sort')).toContainText('默认排序');
});

test('private list sends its scope together with size sorting', async ({ page }) => {
    const queries: URLSearchParams[] = [];
    await page.route('**/api/scopedbooks?*', route => {
        queries.push(new URL(route.request().url()).searchParams);
        return route.fulfill({ contentType: 'application/x-ndjson', body: JSON.stringify({ err: 'ok', total: 0 }) + '\n' });
    });
    await page.goto('/me/private?sort=size_desc');
    await expect.poll(() => queries.some(q => q.get('scope') === 'private' && q.get('sort') === 'size_desc')).toBe(true);
});

test('a slow reading response cannot replace a newer sort', async ({ page }) => {
    let oldFinished = false;
    await page.route('**/api/reading?*', async route => {
        const sort = new URL(route.request().url()).searchParams.get('sort');
        if (sort === 'size_asc') await new Promise(resolve => setTimeout(resolve, 1200));
        await route.fulfill({ json: { err: 'ok', books: sort ? [{
            id: 1, title: sort === 'size_asc' ? '过期顺序' : '最新顺序', img: '/get/cover/1', format_sizes: { EPUB: 1024 },
        }] : [] } });
        if (sort === 'size_asc') oldFinished = true;
    });
    await page.goto('/me/history');
    const sort = page.getByTestId('book-size-sort').getByRole('combobox').first();
    await sort.click();
    await page.getByRole('option', { name: '大小：从小到大', exact: true }).click();
    await sort.click();
    await page.getByRole('option', { name: '大小：从大到小', exact: true }).click();
    await expect(page.getByText('最新顺序', { exact: true })).toBeVisible();
    await expect.poll(() => oldFinished).toBe(true);
    await expect(page.getByText('最新顺序', { exact: true })).toBeVisible();
    await expect(page.getByText('过期顺序', { exact: true })).toHaveCount(0);
});

test('Russian page jump remains on one line at 320px with enlarged text', async ({ page, context }) => {
    await context.addCookies([{ name: 'i18n_redirected', value: 'ru-RU', url: process.env.PLAYWRIGHT_BASE_URL || 'http://localhost:3000' }]);
    await page.setViewportSize({ width: 320, height: 900 });
    await page.route('**/api/recent?*', route => route.fulfill({ json: { err: 'ok', total: 351165, books: [] } }));
    await page.goto('/recent?sort=size_desc');
    await expect(page.getByTestId('book-size-sort')).toContainText('Размер: по убыванию');
    await page.addStyleTag({ content: 'html { font-size: 32px !important }' });
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    const jump = page.getByTestId('page-jump').last();
    const input = await jump.locator('input').boundingBox();
    const button = await jump.getByTestId('page-jump-submit').boundingBox();
    expect(input && button && Math.abs(input.y - button.y) < 10).toBeTruthy();
    expect(input!.width).toBeGreaterThanOrEqual(40);
    await jump.locator('input').fill('5853');
    expect(await jump.locator('input').evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
});

for (const theme of ['light', 'dark']) for (const width of [320, 1440]) {
    test(`size control supports keyboard at ${width}px in ${theme} theme`, async ({ page, context }) => {
        await context.addCookies([{ name: 'theme', value: theme, url: process.env.PLAYWRIGHT_BASE_URL || 'http://localhost:3000' }]);
        await page.setViewportSize({ width, height: 900 });
        await page.goto('/recent');
        const input = page.getByTestId('book-size-sort').getByRole('combobox', { name: '排序', exact: true });
        await input.focus();
        await input.press('ArrowDown');
        await expect(page.getByRole('option', { name: '大小：从小到大', exact: true })).toBeVisible();
        await page.waitForTimeout(350);
        await page.screenshot({ path: test.info().outputPath(`size-sort-${theme}-${width}.png`) });
        await input.press('Escape');
        await expect(page.getByRole('option', { name: '大小：从小到大', exact: true })).toBeHidden();
        const ratio = await page.getByTestId('book-size-sort').locator('.v-messages').evaluate(el => {
            const rgba = (s: string) => { const v = s.match(/[\d.]+/g)?.map(Number) || [0, 0, 0]; return [...v.slice(0, 3), v[3] ?? 1]; };
            const fg = rgba(getComputedStyle(el).color);
            let opacity = fg[3], bg = [255, 255, 255, 1], parent: Element | null = el;
            while (parent) {
                const style = getComputedStyle(parent);
                opacity *= Number(style.opacity);
                const b = rgba(style.backgroundColor);
                if (b[3] === 1) { bg = b; break; }
                parent = parent.parentElement;
            }
            const lum = (c: number[]) => c.slice(0, 3).map(v => { v /= 255; return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }).reduce((a, v, i) => a + v * [0.2126, 0.7152, 0.0722][i], 0);
            const a = lum(fg.slice(0, 3).map((v, i) => v * opacity + bg[i] * (1 - opacity))), b = lum(bg);
            return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
        });
        expect(ratio).toBeGreaterThanOrEqual(4.5);
        await page.addStyleTag({ content: 'html { font-size: 32px !important }' });
        await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    });
}
