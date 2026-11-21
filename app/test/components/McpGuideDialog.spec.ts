// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import McpGuideDialog from '@/components/McpGuideDialog.vue';

vi.mock('vue-i18n', () => ({
    useI18n: () => ({ t: (key: string) => key }),
}));

const backend = vi.hoisted(() => vi.fn());
vi.mock('nuxt/app', () => ({ useNuxtApp: () => ({ $backend: backend }) }));
const account = { err: 'ok', username: 'reader-a', enabled: true, configured: false, allow_write: false };
const vuetify = createVuetify({ components, directives });
global.ResizeObserver = require('resize-observer-polyfill');
(globalThis as Record<string, unknown>).visualViewport = {
    addEventListener: () => {}, removeEventListener: () => {},
    width: 1024, height: 768, scale: 1, offsetLeft: 0, offsetTop: 0,
};

afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = '';
});

function mountGuide() {
    backend.mockReset().mockResolvedValue(account);
    vi.stubGlobal('location', { origin: 'http://192.168.1.96:8850' });
    return mount(McpGuideDialog, {
        props: { modelValue: true },
        global: { plugins: [vuetify] },
        attachTo: document.body,
    });
}

describe('McpGuideDialog.vue', () => {
    it('generates the current account token, copies config and clears the token on close', async () => {
        const wrapper = mountGuide();
        const writeText = vi.fn().mockResolvedValue(undefined);
        vi.stubGlobal('navigator', { clipboard: { writeText } });
        await flushPromises();
        expect(document.querySelector('[data-testid="mcp-http-example"]')).toBeNull();
        const findButton = (key: string) => [...document.querySelectorAll('button')].find(button => button.textContent?.includes(key));
        backend.mockResolvedValueOnce({ ...account, configured: true, access_token: 'generated-test-token' });
        findButton('mcpGenerateToken')?.click();
        await flushPromises();
        expect(backend).toHaveBeenLastCalledWith('/plugins/talebook.combo.mcp?personal_token=1', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action: 'reveal' }) });
        const example = document.querySelector('[data-testid="mcp-http-example"]');
        expect(example?.textContent).toContain('http://192.168.1.96:8850/mcp');
        expect(example?.textContent).toContain('Bearer generated-test-token');
        findButton('mcpCopyConfig')?.click();
        await flushPromises();
        expect(writeText).toHaveBeenCalledWith(example?.textContent);
        await wrapper.setProps({ modelValue: false });
        await wrapper.setProps({ modelValue: true });
        await flushPromises();
        expect(document.querySelector('[data-testid="mcp-http-example"]')).toBeNull();
        wrapper.unmount();
    });

    it('shows the existing container stdio configuration and closes', async () => {
        const wrapper = mountGuide();
        await wrapper.vm.$nextTick();
        const tabs = document.querySelectorAll('.v-tab');
        (tabs[1] as HTMLElement).click();
        await new Promise(resolve => setTimeout(resolve, 0));
        const example = document.querySelector('[data-testid="mcp-stdio-example"]');
        const { args } = JSON.parse(example?.textContent || '{}').mcpServers.bookharbor;
        expect(args).toEqual(['exec', '-i', '-u', 'talebook', 'bookharbor', 'booksvr', 'mcp', '--user', '你的BookHarbor用户名']);
        const close = document.querySelector('[aria-label="common.close"]') as HTMLElement;
        close.click();
        expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([false]);
        wrapper.unmount();
    });
});
