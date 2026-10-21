import { mount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { createI18n } from 'vue-i18n';
import ImportStatistics from '@/components/ImportStatistics.vue';

vi.stubGlobal('useI18n', () => ({ t: (key: string) => key.split('.').at(-1), n: (value: number) => value.toLocaleString('en-US') }));
const summary = { library_books: 380783, available_books: 351165, done: 410140, missing_source_books: 29618 };
describe('Import statistics', () => {
    it('separates books, successful files and confirmed missing sources while idle', () => {
        const wrapper = mount(ImportStatistics, { props: { summary } });
        for (const [key, value] of Object.entries(summary)) {
            expect(wrapper.get(`[data-testid="import-stat-${key}"]`).text()).toBe(value.toLocaleString('en-US'));
        }
        expect(wrapper.findAll('dt')).toHaveLength(4);
    });
    it('does not claim zero books before the response or for missing API fields', async () => {
        const wrapper = mount(ImportStatistics);
        expect(wrapper.findAll('dd').map(item => item.text())).toEqual(['—', '—', '—', '—']);
        await wrapper.setProps({ summary: { ...summary, missing_source_books: 0 } });
        expect(wrapper.get('[data-testid="import-stat-missing_source_books"]').text()).toBe('0');
    });
    it.each(['zh-CN', 'en-US', 'ru-RU'])('has valid translations and file units in %s', (locale) => {
        const messages = JSON.parse(readFileSync(resolve(process.cwd(), `i18n/locales/${locale}.json`), 'utf8'));
        const i18n = createI18n({ legacy: false, locale, messages: { [locale]: messages } });
        for (const key of ['library_books', 'available_books', 'done', 'missing_source_books', 'explanation']) {
            expect(i18n.global.t(`admin.imports.statistics.${key}`)).not.toContain('admin.imports.statistics');
        }
        expect(i18n.global.t('admin.imports.message.importProgress', [3, 2, 1, 4])).toContain('4');
        expect(messages.admin.imports.tab.done).not.toBe('已导入');
        expect(messages.admin.imports.message.scanProgress).not.toContain('{0} 本');
    });
});
