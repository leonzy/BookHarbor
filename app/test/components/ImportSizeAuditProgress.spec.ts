import { mount } from '@vue/test-utils';
import { createI18n } from 'vue-i18n';
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import ImportSizeAuditProgress from '@/components/ImportSizeAuditProgress.vue';
const messages = JSON.parse(readFileSync(resolve(process.cwd(), 'i18n/locales/zh-CN.json'), 'utf8'));
const render = (progress?: object, minimumKib = 10) => mount(ImportSizeAuditProgress, { props: { progress, minimumKib }, global: { stubs: { VBtn: { props: ['disabled', 'loading'], template: '<button :disabled="disabled"><slot /></button>' } }, plugins: [createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': messages } })] } });
describe('Existing small file audit', () => {
    it('offers run now before a job without inventing progress', () => {
        const view = render();
        expect(view.text()).toContain('按当前已保存的阈值开始核查');
        expect(view.text()).not.toContain('已核查 0');
        expect(render(undefined, 0).get('button').attributes('disabled')).toBeDefined();
    });
    it('shows removals, preserved originals and unreadable exceptions', () => {
        const view = render({ state: 'running', checked: 3, total: 7, removed_formats: 2, removed_books: 1, unreadable: 1, error: 'database busy' });
        expect(view.text()).toContain('已核查 3 个文件，初始候选 7 个');
        expect(view.text()).toContain('已移除 2 个文件版本、1 本书目，原文件保留');
        expect(view.text()).toContain('1 个文件不可读或来源受保护');
        expect(view.get('[role="alert"]').text()).toBe('database busy');
    });
});

it('emits run and explains the next safe checkpoint', async () => { const view = render({state: 'running', waiting_reason: 'import_batch'}); await view.get('button').trigger('click'); expect(view.emitted('run')).toHaveLength(1); expect(view.text()).toContain('等待当前文件或导入批次完成'); });
