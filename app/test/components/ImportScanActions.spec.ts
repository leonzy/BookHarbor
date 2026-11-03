import { mount, flushPromises } from '@vue/test-utils';
import { createI18n } from 'vue-i18n';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import { afterAll, beforeAll, afterEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import ImportScanActions from '@/components/ImportScanActions.vue';

const zh = JSON.parse(readFileSync(resolve('i18n/locales/zh-CN.json'), 'utf8'));
const wrappers: ReturnType<typeof mount>[] = [];
beforeAll(() => vi.stubGlobal('visualViewport', { width: 1024, height: 768, offsetLeft: 0, offsetTop: 0, addEventListener: vi.fn(), removeEventListener: vi.fn() }));
afterAll(() => vi.unstubAllGlobals());
afterEach(() => wrappers.splice(0).forEach(w => w.unmount()));
function render(progress: object | null = null) {
    const w = mount(ImportScanActions, { props: { progress }, attachTo: document.body, global: { plugins: [createVuetify({ components, directives }), createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zh } })] } });
    wrappers.push(w); return w;
}
describe('Incremental scan actions', () => {
    it('starts incrementally and keeps the typed target during polling', async () => {
        const w = render(); await w.get('input').setValue('/books1/Amazon');
        await w.setProps({ progress: { scope: '/books/old', state: 'done', files: 15 } });
        expect((w.get('input').element as HTMLInputElement).value).toBe('/books1/Amazon');
        await w.findAll('button')[0].trigger('click');
        expect(w.emitted('run')?.[0]?.[0]).toMatchObject({ path: '/books1/Amazon', mode: 'incremental' });
    });
    it('continues the exact paused import, and can choose another directory', async () => {
        const w = render({ scope: '/books1/Amazon', state: 'paused', auto_import: true });
        expect(w.findAll('button')[0].text()).toContain('继续');
        await w.findAll('button')[0].trigger('click');
        expect(w.emitted('run')?.[0]?.[0]).toMatchObject({ path: '/books1/Amazon', resumeImport: true });
        await w.get('input').setValue('/books/new');
        expect(w.findAll('button')[0].text()).toContain('增量扫描');
        await w.findAll('button')[0].trigger('click');
        expect(w.emitted('run')?.[1]?.[0]).toMatchObject({ path: '/books/new', mode: 'incremental', resumeImport: false });
    });
    it('does not start a full check until explicitly confirmed', async () => {
        const w = render(); await w.findAll('button')[1].trigger('click'); await flushPromises();
        expect(w.emitted('run')).toBeUndefined();
        expect(document.body.textContent).toContain('重新枚举');
        expect(document.body.querySelector('[role="dialog"]')?.getAttribute('aria-label')).toBe('全量核对');
        const confirm = Array.from(document.body.querySelectorAll('button')).find(b => b.textContent?.includes('开始全量核对'));
        expect(confirm).toBeTruthy(); confirm!.click(); await flushPromises();
        expect(w.emitted('run')?.[0]?.[0]).toMatchObject({ mode: 'full', path: '' });
    });
});
