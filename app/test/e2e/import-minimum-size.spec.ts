import { test, expect } from '@playwright/test';

test('validates and persists minimum import size including zero', async ({ request, page }) => {
    await request.post('http://127.0.0.1:8080/_test/reset', { data: { installed: true } });
    let minimum = 10;
    await page.route('**/api/admin/import/settings', async route => {
        if (route.request().method() === 'POST') minimum = route.request().postDataJSON().min_file_size_kib;
        await route.fulfill({ json: { err: 'ok', settings: {
            import_directories: ['/books'], scan_upload_path: '/books', import_mode: 'index',
            min_file_size_kib: minimum, auto_watch_enabled: false,
            directory_check: { status: 'ok', readable: true, writable: true }, watch_status: { state: 'off' },
        } } });
    });
    await page.route('**/api/admin/import/directory/check', route => route.fulfill({ json: { err: 'ok', directory: { status: 'ok', readable: true, writable: true } } }));
    await page.route('**/api/admin/import/watch/status', route => route.fulfill({ json: { err: 'ok', watch_status: { state: 'off', size_audit: { state: 'running', checked: 3, total: 7, removed_formats: 2, removed_books: 1, unreadable: 1 } } } }));
    await page.goto('/admin/imports', { waitUntil: 'networkidle' });
    await expect(page.getByTestId('import-size-audit')).toContainText('已核查 3 个文件');
    await page.getByRole('button', { name: '导入设置' }).click();
    const input = page.getByTestId('import-minimum-size').locator('input');
    const save = page.getByRole('button', { name: /保存设置|保存并核查书库/ });
    await expect(input).toHaveValue('10');
    await input.scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'test-results/import-minimum-desktop.png', fullPage: true, animations: 'disabled' });
    for (const invalid of ['-1', '1.5', '']) {
        await input.fill(invalid);
        await save.click();
        await expect(input).toBeFocused();
        expect(minimum).toBe(10);
        await expect(page.getByText('请输入 0 至 2147483647 的整数。')).toBeVisible();
    }
    await input.fill('0');
    await save.click();
    await expect.poll(() => minimum).toBe(0);
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('button', { name: '导入设置' }).click();
    await expect(input).toHaveValue('0');
    await input.fill('24');
    await save.click();
    await expect.poll(() => minimum).toBe(24);
    await page.reload({ waitUntil: 'networkidle' });
    await page.setViewportSize({ width: 320, height: 800 });
    await page.getByRole('button', { name: '导入设置' }).click();
    await expect(input).toHaveValue('24');
    await expect(input).toBeVisible();
    await expect(save).toBeVisible();
    await expect(page.getByRole('dialog').filter({ has: page.getByTestId('import-minimum-size') }).getByText(/1 KiB = 1024/)).toBeVisible();
    await page.screenshot({ path: 'test-results/import-minimum-mobile.png', fullPage: true, animations: 'disabled' });
    const contrast = async () => page.evaluate(() => {
        const rgb = (value: string) => (value.match(/[0-9.]+/g) || []).slice(0, 3).map(Number);
        const lum = (a: number[]) => a.map(v => v / 255).map(v => v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4).reduce((a, v, i) => a + v * [0.2126, 0.7152, 0.0722][i], 0);
        const ratio = (a: number[], b: number[]) => (Math.max(lum(a), lum(b)) + 0.05) / (Math.min(lum(a), lum(b)) + 0.05);
        const button = getComputedStyle(document.querySelector('.import-size-save')!);
        const alert = document.querySelector('.import-size-warning')!;
        const card = rgb(getComputedStyle(alert.closest('.v-card')!).backgroundColor);
        const underlay = getComputedStyle(alert.querySelector('.v-alert__underlay')!);
        const backdrop = rgb(underlay.backgroundColor).map((v, i) => v * Number(underlay.opacity) + card[i] * (1 - Number(underlay.opacity)));
        return { button: ratio(rgb(button.color), rgb(button.backgroundColor)), warning: ratio(rgb(getComputedStyle(alert).color), backdrop) };
    });
    let ratios = await contrast();
    expect(ratios.button).toBeGreaterThanOrEqual(4.5);
    expect(ratios.warning).toBeGreaterThanOrEqual(4.5);
    await page.context().addCookies([{ name: 'theme', value: 'dark', url: 'http://127.0.0.1:3000' }]);
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('button', { name: '导入设置' }).click();
    await expect(input).toHaveValue('24');
    await input.scrollIntoViewIfNeeded();
    ratios = await contrast();
    expect(ratios.button).toBeGreaterThanOrEqual(4.5);
    expect(ratios.warning).toBeGreaterThanOrEqual(4.5);
    await page.screenshot({ path: 'test-results/import-minimum-dark.png', fullPage: true, animations: 'disabled' });
});
