import { test, expect } from '@playwright/test';

test.use({ channel: 'chrome' });
for (const width of [1440, 390, 320]) {
    test(`access draft, classification, preview and apply at ${width}px`, async ({ page }) => {
        await page.setViewportSize({ width, height: 1000 });
        let revision: any = null;
        let applied = false;
        const errors: string[] = [];
        page.on('pageerror', e => errors.push(e.message));
        await page.route('**/api/admin/access**', async route => {
            const request = route.request(), url = new URL(request.url());
            const result: any = { err: 'ok' };
            if (url.pathname === '/api/admin/access') {
                result.access = { active: applied ? 1 : 0, revision, principals: [{ id: 'guest', account_id: 0, guest: true }], roots: [{ id: 'root', path: '/books/儿童读物', ready: true }] };
            } else if (request.method() === 'POST' && url.pathname.endsWith('/revision')) {
                revision = { id: 1, version: 1, base_revision: 0, status: 'draft', processed: 0, total: 0, watermark: 12, policy: request.postDataJSON().policy };
                result.revision = revision;
            } else if (url.pathname.endsWith('/prepare')) {
                revision = { ...revision, status: 'ready', processed: 1, total: 1 }; result.revision = revision;
            } else if (url.pathname.endsWith('/preview')) {
                result.books = [{ id: 1, title: '儿童百科全书' }]; result.total = 1;
            } else if (url.pathname.endsWith('/apply')) {
                expect(request.postDataJSON().watermark).toBe(12);
                applied = true; revision.status = 'active'; result.revision = revision;
            } else if (url.pathname.endsWith('/books')) {
                result.books = []; result.total = 0;
            } else { result.revision = revision; }
            await route.fulfill({ json: result });
        });
        await page.goto('/admin/access');
        await expect(page.getByRole('heading', { name: '书库访问范围', exact: true })).toBeVisible();
        await page.getByRole('tab', { name: '权限分类', exact: true }).click();
        await page.getByRole('button', { name: '新增权限分类', exact: true }).click();
        await page.getByLabel('分类名称', { exact: true }).fill('儿童读物');
        await page.getByRole('button', { name: '保存草稿', exact: true }).click();
        await expect(page.getByText('草稿已保存', { exact: true })).toBeVisible();
        await page.getByRole('button', { name: '管理分类书籍', exact: true }).click();
        const dialog = page.getByRole('dialog');
        await expect(dialog).toBeVisible();
        await expect(dialog.getByText('此分类暂无书籍', { exact: true })).toBeVisible();
        await dialog.getByRole('button', { name: '关闭', exact: true }).focus();
        await page.keyboard.press('Enter');
        await expect(dialog).toBeHidden();
        await page.getByRole('button', { name: '准备变更', exact: true }).click();
        await page.getByRole('button', { name: '检查影响并应用', exact: true }).click();
        await expect(dialog.getByText('儿童百科全书', { exact: true })).toBeVisible();
        await expect(dialog.getByRole('button', { name: '应用此版本', exact: true })).toBeDisabled();
        await dialog.getByLabel('我已核对范围及影响，确认应用此版本。', { exact: true }).check();
        await dialog.getByRole('button', { name: '应用此版本', exact: true }).click();
        await expect(page.getByText('规则已生效', { exact: true })).toBeVisible();
        expect(applied).toBe(true);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
        expect(errors).toEqual([]);
    });
}
