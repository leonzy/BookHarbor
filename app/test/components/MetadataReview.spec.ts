import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import { createI18n } from 'vue-i18n';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import MetadataReview from '@/components/MetadataReview.vue';
const zh = JSON.parse(readFileSync(resolve('i18n/locales/zh-CN.json'), 'utf8'));
const backend = vi.fn();
vi.mock('nuxt/app', () => ({ useNuxtApp: () => ({ $backend: backend }) }));
const vuetify = createVuetify({ components, directives });
const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } });
const mounted: ReturnType<typeof mount>[] = [];
afterEach(() => { mounted.splice(0).forEach(w => w.unmount()); backend.mockReset(); });
function render() { const w = mount(MetadataReview, { props: { count: 28 }, global: { plugins: [vuetify, i18n] } }); mounted.push(w); return w; }
describe('metadata manual review', () => {
    it('loads on expansion, shows reasons and opens books in new tabs; cursor paging deduplicates', async () => {
        const w = render(); expect(backend).not.toHaveBeenCalled(); expect(w.get('button').text()).toContain('刷新待确认列表');
        backend.mockResolvedValueOnce({ err: 'ok', books: [{ id: 1, title: 'Book one', reason: '缺少作者' }], next_after: 1 });
        (w.get('details').element as HTMLDetailsElement).open = true;
        await w.get('details').trigger('toggle'); await flushPromises();
        expect(backend).toHaveBeenCalledWith('/admin/metadata/enrichment/review?after=0');
        expect(w.text()).toContain('缺少作者'); expect(w.get('a').attributes('target')).toBe('_blank');
        expect(w.findAll('a')[1].attributes('href')).toBe('/book/1/edit');
        backend.mockResolvedValueOnce({ err: 'ok', books: [{ id: 1, title: 'Book one', reason: '缺少作者' }, { id: 2, title: 'Book two', reason: '未匹配' }], next_after: 0 });
        await w.findAll('button').at(-1)!.trigger('click'); await flushPromises();
        expect(backend).toHaveBeenLastCalledWith('/admin/metadata/enrichment/review?after=1');
        expect(w.findAll('li')).toHaveLength(2); expect(w.findAll('button')).toHaveLength(1);
    });
    it('keeps a failed load retryable and shows an empty queue', async () => {
        const w = render(); backend.mockRejectedValueOnce(new Error('offline'));
        await w.get('button').trigger('click'); await flushPromises(); expect(w.get('[role="alert"]').text()).toContain('刷新重试');
        backend.mockResolvedValueOnce({ err: 'ok', books: [], next_after: 0 });
        await w.get('button').trigger('click'); await flushPromises(); expect(w.get('[role="status"]').text()).toContain('没有待确认');
    });
});
