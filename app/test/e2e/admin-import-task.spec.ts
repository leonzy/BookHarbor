import { test, expect } from '@playwright/test';

for (const width of [1440, 320]) test(`stops and resumes by keyboard at ${width}px`, async ({ page, request }) => {
    await page.setViewportSize({ width, height: 900 });
    await request.post(`${process.env.MOCK_API_URL || 'http://127.0.0.1:8080'}/_test/reset`, { data: { installed: true } });
    let watch = { state: 'off', manual_scan_running: true, paused: false };
    let failCancel = true;
    const summary = { total: 12, done: 12, todo: 0, failed: 0, skipped: 0, library_books: 12, available_books: 12, missing_source_books: 0 };
    await page.route('**/api/admin/import/settings', route => route.fulfill({ json: { err: 'ok', settings: { watch_status: watch } } }));
    await page.route('**/api/admin/scan/list?*', route => route.fulfill({ json: { err: 'ok', items: [], total: 0, scan_dir: '/books', import_mode: 'index', summary, watch_status: watch } }));
    await page.route('**/api/admin/scan/status', route => route.fulfill({ json: { err: 'ok', running: watch.manual_scan_running, status: {}, summary, watch_status: watch } }));
    await page.route('**/api/admin/import/task', route => {
        if (route.request().method() === 'DELETE') {
            if (failCancel) return route.fulfill({ json: { err: 'error', msg: '停止失败，请重试' } });
            watch = { ...watch, paused: true };
        }
        return route.fulfill({ json: { err: 'ok', watch_status: watch } });
    });
    await page.goto('/admin/imports');
    const stop = page.getByRole('button', { name: '停止当前任务' });
    await expect(stop).toBeVisible();
    await stop.focus();
    await page.keyboard.press('Enter');
    await expect(page.getByText('停止失败，请重试')).toBeVisible();
    await page.getByRole('button', { name: '关闭', exact: true }).click();
    await expect(page.getByRole('dialog')).toBeHidden();
    failCancel = false;
    await stop.focus();
    await page.keyboard.press('Space');
    await expect(page.getByText('正在停止，当前书籍处理完成后结束。')).toBeVisible();
    await expect(page.getByRole('button', { name: '停止中' })).toBeDisabled();
    watch = { ...watch, manual_scan_running: false };
    await expect(page.getByText(/任务已停止，已完成结果保留/)).toBeVisible();
    await expect(page.getByTestId('import-stat-done')).toHaveText('12');
    if (width === 320) {
        const drawer = page.locator('.v-navigation-drawer').first();
        if ((await drawer.getAttribute('class'))?.includes('v-navigation-drawer--active')) {
            await page.getByRole('banner').getByRole('button').first().click();
        }
        await expect(drawer).not.toHaveClass(/v-navigation-drawer--active/);
    }
    await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    await page.route('**/api/admin/scan/run', route => {
        watch = { ...watch, manual_scan_running: true, paused: false };
        return route.fulfill({ json: { err: 'ok' } });
    });
    const scan = page.getByRole('button', { name: '扫描书籍' });
    await scan.focus();
    await page.keyboard.press('Enter');
    await expect(stop).toBeEnabled();
});
