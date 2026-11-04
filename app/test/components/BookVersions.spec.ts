import { mount } from '@vue/test-utils';
import { createI18n } from 'vue-i18n';
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import BookVersions from '@/components/BookVersions.vue';

const messages = JSON.parse(readFileSync(resolve('i18n/locales/zh-CN.json'), 'utf8'));
const render = (versions = []) => mount(BookVersions, {
    props: { versions },
    global: { plugins: [createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': messages } })] },
});
describe('BookVersions', () => {
    it('does not invent versions for an ungrouped book', () => {
        expect(render().find('section').exists()).toBe(false);
    });
    it('retains two EPUB versions with distinct IDs, downloads and source paths', () => {
        const data = [2, 3].map(id => ({ id, href: `/book/${id}`, title: `版本 ${id}`, authors: ['作者'], files: [{ format: 'EPUB', size: 2048, href: `/api/book/${id}.EPUB`, storage: { path: `/books/${id}/source.epub` } }] }));
        const w = render(data);
        expect(w.findAll('article')).toHaveLength(2);
        expect(w.get('a[href="/api/book/2.EPUB"]').attributes('aria-label')).toContain('版本 2');
        expect(w.get('a[href="/book/3"]').attributes('target')).toBe('_blank');
        expect(w.text()).toContain('/books/2/source.epub');
        expect(w.text()).toContain('/books/3/source.epub');
        expect(w.text()).toContain('2.0 KiB');
    });
    it('renders permitted downloads without fabricating restricted storage paths', () => {
        const w = render([{ id: 2, href: '/book/2', title: '公开版本', files: [{ format: 'MOBI', size: 1024, href: '/api/book/2.MOBI' }] }]);
        expect(w.find('code').exists()).toBe(false);
        expect(w.get('a[href="/api/book/2.MOBI"]').text()).toContain('MOBI');
    });
});
