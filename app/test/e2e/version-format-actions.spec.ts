import { test, expect } from '@playwright/test';
import { createServer, type Server } from 'node:http';
import { readFile } from 'node:fs/promises';
import { resolve, extname, sep } from 'node:path';
import { readFileSync } from 'node:fs';
const fixture = JSON.parse(readFileSync(new URL('./mocks/api_book_1.json', import.meta.url), 'utf8'));

// Isolated static-build regression; all APIs and delivery are mocked.
let server: Server;
let origin: string;
test.beforeAll(async () => {
    const root = resolve('.output/public');
    server = createServer(async (req, res) => {
        const file = resolve(root, '.' + decodeURIComponent(new URL(req.url || '/', 'http://localhost').pathname));
        if (!file.startsWith(root + sep)) { res.writeHead(403); res.end(); return; }
        const types: Record<string, string> = { '.js': 'application/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml' };
        try { res.setHeader('Content-Type', types[extname(file)] || 'text/html'); res.end(await readFile(file)); }
        catch { res.setHeader('Content-Type', 'text/html'); res.end(await readFile(resolve(root, '200.html'))); }
    });
    await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
    origin = `http://127.0.0.1:${(server.address() as { port: number }).port}`;
});
test.afterAll(async () => { server.closeAllConnections(); await new Promise<void>(resolve => server.close(() => resolve())); });

for (const [width, theme] of [[1280, 'light'], [320, 'light'], [320, 'dark']] as const) {
    test(`chooses another version for reading and delivery at ${width}px ${theme}`, async ({ page, context }, testInfo) => {
        await page.setViewportSize({ width, height: 900 });
        await context.addCookies([{ name: 'theme', value: theme, url: origin }]);
        let sent: { path: string; body: Record<string, unknown> } | undefined;
        const errors: string[] = [];
        page.on('pageerror', error => errors.push(error.message));
        await context.route('**/api/**', async route => {
            const path = new URL(route.request().url()).pathname;
            let json: unknown = { err: 'ok', books: [], actions: [], state: {} };
            if (path === '/api/user/info') json = { err: 'ok', user: { is_login: true, is_admin: true, nickname: 'Test' }, sys: { title: 'BookHarbor', socials: [], allow: {}, friends: [], footer: '', books: 2 } };
            if (path === '/api/book/1') json = { ...fixture, book: { ...fixture.book, files: [{ format: 'EPUB', size: 2048 }], versions: [{ id: 455745, title: '另一版本', files: [{ format: 'MOBI', size: 4096 }, { format: 'EPUB', size: 8192 }] }] } };
            if (path === '/api/user/devices') json = { err: 'ok', device_types: [{ text: 'BOOX', value: 'boox', default_port: 8088 }], devices: [{ name: '测试设备', type: 'boox', ip: '127.0.0.1', port: 8088 }] };
            if (path.endsWith('/send_to_device')) { sent = { path, body: route.request().postDataJSON() }; json = { err: 'ok' }; }
            await route.fulfill({ json });
        });
        await context.route('**/read/**', route => route.fulfill({ contentType: 'text/plain', body: 'isolated reader destination' }));
        await page.goto(origin + '/book/1');
        await expect(page.getByTestId('book-format-epub')).toHaveCount(2);
        await expect(page.getByTestId('book-format-mobi')).toHaveAttribute('aria-label', /#455745/);
        await page.getByTestId('book-format-mobi').click();
        const downloads = page.getByTestId('book-download-dialog');
        await expect(downloads.locator('a')).toHaveCount(3);
        await expect(downloads.locator('a[href="/api/book/455745.MOBI"]')).toContainText('#455745');
        await downloads.getByRole('button', { name: '关闭', exact: true }).click();
        const read = page.getByTestId('open-online-reader');
        await expect(read).toBeVisible();
        await read.focus(); await page.keyboard.press('Enter');
        const dialog = page.getByTestId('book-read-format-dialog');
        await expect(dialog).toBeVisible();
        await expect(dialog).toHaveClass(new RegExp(`v-theme--${theme}`));
        await expect(dialog.locator('a')).toHaveCount(3);
        await page.waitForTimeout(400);
        await page.screenshot({ path: testInfo.outputPath(`read-${width}.png`) });
        await page.keyboard.press('Escape');
        await expect(dialog).toBeHidden();
        await expect(read).toBeFocused();
        await read.click();
        const popupPromise = page.waitForEvent('popup');
        await dialog.locator('a[href="/read/455745?format=mobi"]').click();
        const popup = await popupPromise;
        await expect(popup).toHaveURL(origin + '/read/455745?format=mobi');
        await popup.close();
        await page.getByTestId('book-action-send').click();
        const send = page.getByRole('button', { name: '发送', exact: true });
        await expect(send).toBeDisabled();
        await page.getByTestId('send-device-format').click();
        await page.getByRole('option', { name: /EPUB.*#455745/ }).click();
        const selectedText = page.getByTestId('send-device-format').locator('.v-select__selection-text');
        await expect(selectedText).toContainText('#455745');
        expect(await selectedText.evaluate(e => e.scrollWidth <= e.clientWidth + 1)).toBe(true);
        await page.getByTestId('send-device-type').click();
        await page.getByRole('option', { name: 'BOOX', exact: true }).click();
        await expect(send).toBeEnabled();
        await page.locator('.v-overlay--active .v-card-title').click();
        await page.waitForTimeout(400);
        await page.screenshot({ path: testInfo.outputPath(`send-${width}.png`) });
        await send.click();
        await expect.poll(() => sent).toEqual({ path: '/api/book/455745/send_to_device', body: { device_type: 'boox', device_url: 'http://127.0.0.1:8088', format: 'EPUB' } });
        expect(errors).toEqual([]);
    });
}
