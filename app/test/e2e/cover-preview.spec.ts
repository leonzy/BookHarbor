import { expect, test } from '@playwright/test';

test.beforeEach(async ({ request }) => {
    await request.post('http://127.0.0.1:8080/_test/reset', { data: { installed: true, loggedIn: true, audiobookPublished: true, readingBooks: [{ id: 1, title: '三体', img: '/get/cover/1.jpg' }], history: { read_history: [{ id: 1, title: '三体', img: '/get/cover/1.jpg' }], push_history: [], visit_history: [] } } });
});

for (const path of ['/library/local', '/', '/user/history', '/book/1', '/book/1/edit', '/admin/books', '/audios', '/audios/create?book=1', '/book/1/audios']) {
    test(`opens cover without navigating at ${path}`, async ({ page, context }) => {
        await page.goto(path);
        await page.waitForLoadState('networkidle');
        const cover = page.locator('button.book-cover-preview-trigger:visible').first();
        await expect(cover).toBeEnabled();
        const url = page.url();
        const count = context.pages().length;
        await cover.click();
        const dialog = page.getByRole('dialog').filter({ has: page.locator('.cover-preview-panel') });
        await expect(dialog).toBeVisible();
        await expect(dialog.locator('img.cover-preview-image')).toBeVisible();
        expect(page.url()).toBe(url);
        expect(context.pages()).toHaveLength(count);
        await page.getByRole('button', { name: '关闭', exact: true }).last().click();
        await expect(dialog).toBeHidden();
        await expect(cover).toBeFocused();
    });
}

test('keyboard, original image, zoom, reset, Esc, retry and narrow dark layout', async ({ page, context }) => {
    await context.addCookies([{ name: 'theme', value: 'dark', url: 'http://127.0.0.1:9000' }]);
    await page.setViewportSize({ width: 320, height: 760 });
    await page.goto('/book/1');
    await page.waitForLoadState('networkidle');
    const cover = page.locator('button.book-cover-preview-trigger:visible').first();
    await cover.focus();
    await page.keyboard.press('Enter');
    const image = page.locator('img.cover-preview-image');
    await expect(image).toBeVisible();
    await expect.poll(() => image.evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth > 0)).toBe(true);
    for (let n = 0; n < 8; n++) {
        await page.keyboard.press('Tab');
        expect(await page.locator('.cover-preview-panel').evaluate(el => el.contains(document.activeElement))).toBe(true);
    }
    const initialWidth = (await image.boundingBox())!.width;
    await page.getByRole('button', { name: '放大', exact: true }).click();
    await expect(page.getByRole('button', { name: /^重置缩放/ })).toHaveText('150%');
    expect((await image.boundingBox())!.width).toBeGreaterThan(initialWidth);
    for (let n = 0; n < 5; n++) await page.getByRole('button', { name: '放大', exact: true }).click();
    await expect(page.getByRole('button', { name: '放大', exact: true })).toBeDisabled();
    await expect.poll(() => page.locator('.cover-preview-viewport').evaluate(el => el.scrollLeft > 0)).toBe(true);
    await page.getByRole('button', { name: /^重置缩放/ }).click();
    await expect(page.getByRole('button', { name: '缩小', exact: true })).toBeDisabled();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    await page.screenshot({ path: test.info().outputPath('cover-preview-mobile-dark.png') });
    await page.keyboard.press('Escape');
    await expect(image).toBeHidden();
    await expect(cover).toBeFocused();
    await page.route('**/get/cover/*', route => route.abort());
    await cover.click();
    await expect(page.getByText('封面加载失败', { exact: true })).toBeVisible();
    await page.unroute('**/get/cover/*');
    await page.getByRole('button', { name: '重试', exact: true }).click();
    await expect.poll(() => image.evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth > 0)).toBe(true);
});

test('book titles still open details in a new tab and cover buttons are not nested in links', async ({ page }) => {
    await page.goto('/library/local');
    const card = page.getByTestId('book-card').first();
    await expect(card).toBeVisible();
    expect(await page.locator('a button.book-cover-preview-trigger, button button.book-cover-preview-trigger').count()).toBe(0);
    const popup = page.waitForEvent('popup');
    await card.getByRole('link').click();
    const details = await popup;
    await expect(details).toHaveURL(/\/book\/\d+$/);
    await details.close();
    await expect(page).toHaveURL(/\/library\/local$/);
});

for (const locale of [{ code: 'en-US', close: 'Close', zoom: 'Zoom in' }, { code: 'ru-RU', close: 'Закрыть', zoom: 'Увеличить' }]) {
    test(`localizes the narrow preview in ${locale.code}`, async ({ page, context }) => {
        await context.addCookies([{ name: 'i18n_redirected', value: locale.code, url: 'http://127.0.0.1:9000' }]);
        await page.setViewportSize({ width: 320, height: 760 });
        await page.goto('/book/1');
        await page.waitForLoadState('networkidle');
        const cover = page.locator('button.book-cover-preview-trigger:visible').first();
        await cover.focus();
        await page.keyboard.press('Space');
        await expect(page.locator('img.cover-preview-image')).toBeVisible();
        await expect(page.getByRole('button', { name: locale.zoom, exact: true })).toBeVisible();
        await expect(page.getByRole('button', { name: locale.close, exact: true })).toBeInViewport();
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
        await page.mouse.click(1, 1);
        await expect(page.locator('img.cover-preview-image')).toBeHidden();
    });
}

test('keeps close and image scrolling reachable with a long title on a narrow screen', async ({ page }) => {
    const title = '书籍目录中的非常长的完整书名'.repeat(20);
    await page.route('**/api/library?*', route => route.fulfill({ contentType: 'application/x-ndjson', body: JSON.stringify({ err: 'ok', total: 1 }) + '\n' + JSON.stringify({ id: 1, title, img: '/get/cover/1.jpg' }) + '\n' }));
    await page.setViewportSize({ width: 320, height: 640 });
    await page.goto('/library/local');
    await page.waitForLoadState('networkidle');
    await page.locator('button.book-cover-preview-trigger:visible').first().click();
    const heading = page.locator('.cover-preview-toolbar h2');
    await expect(heading).toHaveAttribute('title', title);
    await expect(heading).toHaveCSS('-webkit-line-clamp', '2');
    await expect(page.getByRole('button', { name: '关闭', exact: true })).toBeInViewport();
    await expect(page.locator('.cover-preview-viewport')).toBeInViewport({ ratio: 1 });
    await expect(page.locator('img.cover-preview-image')).toBeInViewport({ ratio: 1 });
    await page.screenshot({ path: test.info().outputPath('cover-preview-long-title.png') });
});
