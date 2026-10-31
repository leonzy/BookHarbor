import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import RunDetail from '@/pages/admin/plugins/runs/[id].vue';
const backend = vi.fn();
vi.mock('nuxt/app', () => ({
    useNuxtApp: () => ({ $backend: backend }),
    useRoute: () => ({ params: { id: '94285' } }),
    useHead: vi.fn(),
}));
vi.mock('@/stores/main', () => ({ useMainStore: () => ({ setNavbar: vi.fn() }) }));
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }));
const vuetify = createVuetify({ components, directives });
const mounted: ReturnType<typeof mount>[] = [];
afterEach(() => { mounted.splice(0).forEach(w => w.unmount()); backend.mockReset(); });
describe('plugin run history', () => {
    it.each([false, true])('shows failure details; manage actions allowed=%s', async (canManage) => {
        backend.mockResolvedValue({ err: 'ok', run: { id: 94285, status: 'failed', action: 'run', can_manage: canManage, error_code: 'provider_error', error_message: 'WeRead gateway request failed', counts: {} }, items: [] });
        const wrapper = mount(RunDetail, { global: { plugins: [vuetify], stubs: { PageJump: true } } });
        mounted.push(wrapper); await flushPromises();
        expect(wrapper.text()).toContain('WeRead gateway request failed');
        expect(wrapper.text().includes('pluginManagement.retryFailed')).toBe(canManage);
        expect(backend).toHaveBeenCalledWith('/admin/plugins/runs/94285');
    });
});
