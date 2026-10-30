import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import { createI18n } from 'vue-i18n';
import MetadataProgress from '@/components/MetadataProgress.vue';
import BookStorageLocations from '@/components/BookStorageLocations.vue';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
const zh = JSON.parse(readFileSync(resolve('i18n/locales/zh-CN.json'), 'utf8'));
const vuetify = createVuetify({ components, directives });
const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } });
const backend = vi.fn();
vi.mock('nuxt/app', () => ({ useNuxtApp: () => ({ $backend: backend }) }));
const mounted: ReturnType<typeof mount>[] = [];
const base = { err: 'ok', total_books: 10, files_checked: 4, files_remaining: 6, counts: { complete: 1, network_pending: 2, needs_review: 1 }, network_retries: 1, network_applied_books: 0, worker_active: true, network_enabled: true, network_max_concurrency: 8 };
afterEach(() => { mounted.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); backend.mockReset(); });
describe('Metadata progress', () => {
    it('separates checked files from online success and refreshes without a save', async () => {
        vi.useFakeTimers();
        backend.mockResolvedValue(base);
        const w = mount(MetadataProgress, { global: { plugins: [vuetify, i18n] } }); mounted.push(w);
        await flushPromises();
        expect(w.text()).toContain('已核查 4 / 10 本');
        expect(w.text()).toContain('待联网处理');
        expect(w.text()).toContain('已联网补全');
        expect(w.text()).toContain('最多8个来源、每源一个请求');
        expect(w.find('[role="progressbar"]').attributes('aria-valuenow')).toBe('40');
        await vi.advanceTimersByTimeAsync(10000); await flushPromises();
        expect(backend).toHaveBeenCalledTimes(2);
        expect(backend).toHaveBeenCalledWith('/admin/metadata/enrichment');
        const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
        await vi.advanceTimersByTimeAsync(20000); expect(backend).toHaveBeenCalledTimes(2); hidden.mockRestore();
        w.unmount(); mounted.pop(); await vi.advanceTimersByTimeAsync(20000);
        expect(backend).toHaveBeenCalledTimes(2);
    });
    it('shows a recoverable error while keeping the previous progress', async () => {
        backend.mockResolvedValueOnce(base).mockRejectedValueOnce(new Error('offline'));
        const w = mount(MetadataProgress, { global: { plugins: [vuetify, i18n] } }); mounted.push(w);
        await flushPromises(); await w.get('button').trigger('click'); await flushPromises();
        expect(w.text()).toContain('进度读取失败');
        expect(w.text()).toContain('已核查 4 / 10 本');
    });
});
describe('Storage locations', () => {
    it('shows distinct files and ZIP members for the current edition', () => {
        const w = mount(BookStorageLocations, { props: { bookId: 12, files: [
            { format: 'EPUB', storage: { directory: '/books/中文目录', filename: '第一版.epub' } },
            { format: 'MOBI', storage: { directory: '/books1', filename: '收藏.zip', archive_member: '内层.zip → 小说/第二版.mobi' } },
        ] }, global: { plugins: [i18n] } }); mounted.push(w);
        expect(w.text()).toContain('当前版本 #12'); expect(w.text()).toContain('/books/中文目录');
        expect(w.text()).toContain('收藏.zip'); expect(w.text()).toContain('内层.zip → 小说/第二版.mobi');
    });
    it('does not invent storage locations when none were authorized', () => {
        const w = mount(BookStorageLocations, { props: { bookId: 12, files: [{ format: 'EPUB' }] }, global: { plugins: [i18n] } }); mounted.push(w);
        expect(w.find('section').exists()).toBe(false);
    });
});
