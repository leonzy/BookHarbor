import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import { createI18n } from 'vue-i18n';
import MetadataSourceLimits from '@/components/MetadataSourceLimits.vue';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
const zh = JSON.parse(readFileSync(resolve('i18n/locales/zh-CN.json'), 'utf8'));
const backend = vi.fn();
vi.mock('nuxt/app', () => ({ useNuxtApp: () => ({ $backend: backend }) }));
const vuetify = createVuetify({ components, directives });
const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } });
const sources = [
    { id: 19, name: '七猫小说', interval_seconds: 5, requests: 20, processed: 10, matched: 3 },
    { id: 3, name: 'Legado', interval_seconds: 5, requests: 0, processed: 0, matched: 0, unavailable: 'no_sources' },
];
const mounted: ReturnType<typeof mount>[] = [];
afterEach(() => { mounted.splice(0).forEach(w => w.unmount()); backend.mockReset(); });
function render() { const w = mount(MetadataSourceLimits, { props: { sources }, global: { plugins: [vuetify, i18n] } }); mounted.push(w); return w; }
describe('independent source intervals', () => {
    it('preserves an unsaved interval during polling and only saves that source', async () => {
        const w = render(); const input = w.findAll('input')[0];
        await input.setValue('12');
        await w.setProps({ sources: sources.map(s => ({ ...s, requests: s.requests + 1 })) });
        expect((input.element as HTMLInputElement).value).toBe('12');
        backend.mockResolvedValue({ err: 'ok', interval_seconds: 12 });
        await w.findAll('form')[0].trigger('submit'); await flushPromises();
        expect(backend).toHaveBeenCalledExactlyOnceWith('/admin/metadata/enrichment/sources/19', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ interval_seconds: 12 }) });
        expect(w.emitted('saved')).toHaveLength(1); expect(w.text()).toContain('请求间隔已保存');
        await w.setProps({ sources: sources.map(s => ({ ...s })) });
        expect((input.element as HTMLInputElement).value).toBe('12');
        expect(w.text()).toContain('未配置已启用的文本书源');
        expect(input.attributes('aria-label')).toContain('七猫小说');
    });
    it('rejects invalid intervals locally and keeps a failed save editable', async () => {
        const w = render(); const input = w.findAll('input')[0];
        await input.setValue('0'); await w.findAll('form')[0].trigger('submit'); await flushPromises();
        expect(backend).not.toHaveBeenCalled(); expect(w.text()).toContain('1至3600');
        await input.setValue('8'); expect(w.text()).not.toContain('1至3600'); backend.mockRejectedValue(new Error('offline'));
        await w.findAll('form')[0].trigger('submit'); await flushPromises();
        expect(w.text()).toContain('保存失败'); expect((input.element as HTMLInputElement).disabled).toBe(false);
    });
});
