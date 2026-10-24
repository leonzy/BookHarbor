import { expect, test } from '@playwright/test';

const mock = process.env.MOCK_API_URL || 'http://127.0.0.1:8080';

test('new installations use BookHarbor while the website title remains editable', async ({ page, request }) => {
    await request.post(`${mock}/_test/reset`, { data: { installed: false } });
    await page.goto('/install');
    await expect(page.getByText('安装 BookHarbor', { exact: true })).toBeVisible();
    await expect(page.getByLabel('网站标题', { exact: true })).toHaveValue('BookHarbor');
    await page.getByLabel('网站标题', { exact: true }).fill('我的家庭书库');
    await expect(page.getByLabel('网站标题', { exact: true })).toHaveValue('我的家庭书库');
});

test('project help links lead to the independent repository', async ({ page, request }) => {
    await request.post(`${mock}/_test/reset`, { data: { installed: true, loggedIn: true } });
    await page.goto('/recent');
    await page.getByRole('button', { name: '打开帮助与关于菜单', exact: true }).click();
    const menu = page.locator('.sidebar-help__card');
    await expect(menu.locator('a[href="https://github.com/leonzy/BookHarbor"]')).toBeVisible();
    await expect(menu.locator('a[href="https://github.com/leonzy/BookHarbor/releases"]')).toBeVisible();
});
