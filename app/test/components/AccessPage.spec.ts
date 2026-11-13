import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import AccessPage from '@/pages/admin/access.vue';
import AccessRangeEditor from '@/components/admin/AccessRangeEditor.vue';
import AccessPreparationStatus from '@/components/admin/AccessPreparationStatus.vue';

const backend = vi.fn();
vi.mock('nuxt/app', () => ({ useNuxtApp: () => ({ $backend: backend }) }));
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }));
global.ResizeObserver = require('resize-observer-polyfill');
const vuetify = createVuetify({ components, directives });
const mounted: ReturnType<typeof mount>[] = [];
afterEach(() => { mounted.splice(0).forEach(w => w.unmount()); backend.mockReset(); });
const overview = (revision: object | null = null) => ({ err: 'ok', access: {
    active: 0, revision, principals: [{ id: 'reader', account_id: 1, guest: false }], roots: [{ id: 'root', path: '/books', ready: true }],
} });
const state = (status: string) => ({ id: 3, version: 1, base_revision: 0, status, processed: 2, total: 2, watermark: 9,
    policy: { groups: [], principals: [], rules: [], classes: [] }, updated_at: 1, message: '' });
async function page() {
    const wrapper = mount(AccessPage, { global: { plugins: [vuetify] } });
    mounted.push(wrapper); await flushPromises(); return wrapper;
}

describe('access administration page', () => {
    it('excludes administrators from ordinary user scope controls', async () => {
        const result = overview();
        result.access.principals.push({ id: 'admin', account_id: 2, guest: false, admin: true } as any);
        backend.mockResolvedValue(result);
        const wrapper = await page();
        expect(wrapper.findAllComponents(AccessRangeEditor)).toHaveLength(1);
    });
    it('keeps explicit historical releases in subsequent drafts without submitting server-owned origins', async () => {
        const draft = { ...state('draft'), policy: { ...state('draft').policy, released_origins: ['origin:1:1:old'], origin_rules: [{ id: 'origin:1:1:old', target: { root: 'root', directory: '/old' }, mode: 'deny_all', users: [], groups: [] }] } };
        backend.mockResolvedValueOnce(overview(draft)).mockResolvedValueOnce({ err: 'ok', revision: draft });
        const wrapper = await page();
        wrapper.findComponent(AccessRangeEditor).vm.$emit('update:modelValue', { mode: 'deny_all', targets: [] });
        await flushPromises();
        await wrapper.findAll('button').find(b => b.text() === 'accessControl.page.save')!.trigger('click');
        await flushPromises();
        const policy = JSON.parse(backend.mock.calls[1][1].body).policy;
        expect(policy.released_origins).toEqual(['origin:1:1:old']);
        expect(policy.origin_rules).toBeUndefined();
    });
    it('saves a user restriction as a draft without applying it', async () => {
        backend.mockResolvedValueOnce(overview()).mockResolvedValueOnce({ err: 'ok', revision: state('draft') });
        const wrapper = await page();
        wrapper.findComponent(AccessRangeEditor).vm.$emit('update:modelValue', { mode: 'deny_all', targets: [] });
        await flushPromises();
        await wrapper.findAll('button').find(b => b.text() === 'accessControl.page.save')!.trigger('click');
        await flushPromises();
        const [path, options] = backend.mock.calls[1];
        expect(path).toBe('/admin/access/revision');
        expect(JSON.parse(options.body).policy.principals).toEqual([{ id: 'reader', range: { mode: 'deny_all', targets: [] } }]);
        expect(backend.mock.calls.some(([url]) => String(url).endsWith('/apply'))).toBe(false);
        expect(wrapper.findComponent(AccessPreparationStatus).props('revision').status).toBe('draft');
    });
    it('blocks saving an empty selected range', async () => {
        backend.mockResolvedValue(overview());
        const wrapper = await page();
        wrapper.findComponent(AccessRangeEditor).vm.$emit('update:modelValue', { mode: 'selected', targets: [] });
        await flushPromises();
        await wrapper.findAll('button').find(b => b.text() === 'accessControl.page.save')!.trigger('click');
        await flushPromises();
        expect(wrapper.text()).toContain('accessControl.page.invalid');
        expect(backend).toHaveBeenCalledTimes(1);
    });
    it('keeps edits after an optimistic version conflict', async () => {
        backend.mockResolvedValueOnce(overview(state('draft'))).mockResolvedValueOnce({ err: 'access.conflict', msg: 'Changed elsewhere' });
        const wrapper = await page();
        wrapper.findComponent(AccessRangeEditor).vm.$emit('update:modelValue', { mode: 'deny_all', targets: [] });
        await flushPromises();
        await wrapper.findAll('button').find(b => b.text() === 'accessControl.page.save')!.trigger('click');
        await flushPromises();
        expect(wrapper.text()).toContain('Changed elsewhere');
        expect(wrapper.text()).toContain('accessControl.page.unsaved');
        expect(wrapper.findComponent(AccessRangeEditor).props('modelValue').mode).toBe('deny_all');
    });
    it('does not render administration controls on forbidden responses', async () => {
        backend.mockResolvedValue({ err: 'user.not_admin', msg: 'Forbidden' });
        const wrapper = await page();
        expect(wrapper.text()).toContain('Forbidden');
        expect(wrapper.findComponent(AccessRangeEditor).exists()).toBe(false);
    });
    it('saves a new permission category before allowing membership edits', async () => {
        backend.mockResolvedValueOnce(overview()).mockResolvedValueOnce({ err: 'ok', revision: state('draft') });
        const wrapper = await page();
        await wrapper.findAll('button').find(b => b.text() === 'accessControl.page.classes')!.trigger('click');
        await wrapper.findAll('button').find(b => b.text() === 'accessControl.page.addClass')!.trigger('click');
        const input = wrapper.findComponent(components.VTextField);
        input.vm.$emit('update:modelValue', '儿童书籍');
        await flushPromises();
        expect(wrapper.findAll('button').find(b => b.text() === 'accessControl.page.classBooks')!.attributes('disabled')).toBeDefined();
        await wrapper.findAll('button').find(b => b.text() === 'accessControl.page.save')!.trigger('click');
        await flushPromises();
        const body = JSON.parse(backend.mock.calls[1][1].body);
        expect(body.policy.classes).toEqual([{ id: expect.any(String), name: '儿童书籍' }]);
        expect(backend.mock.calls.some(([path]) => String(path).endsWith('/apply'))).toBe(false);
    });
});
