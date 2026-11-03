import { mount } from '@vue/test-utils';
import { createI18n } from 'vue-i18n';
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import ImportScanProgress from '@/components/ImportScanProgress.vue';

const messages = JSON.parse(readFileSync(resolve(process.cwd(), 'i18n/locales/zh-CN.json'), 'utf8'));
const render = (progress: object | null) => mount(ImportScanProgress, {
    props: { progress },
    global: { plugins: [createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': messages } })] },
});
describe('Persistent directory scan progress', () => {
    it('keeps paused progress and separates checked files from imported books', () => {
        const wrapper = render({ mode: 'baseline', state: 'paused', phase: 'importing', scope: '/books1/Amazon', current: '/books1/Amazon/large-book.azw3', files: 51, ready: 41, imported: 29, skipped: 10, pending: 10 });
        expect(wrapper.text()).toContain('已暂停，可继续');
        expect(wrapper.text()).toContain('建立目录基线');
        expect(wrapper.get('[data-testid="scan-count-files"]').text()).toBe('51');
        expect(wrapper.get('[data-testid="scan-count-imported"]').text()).toBe('29');
        expect(wrapper.text()).not.toContain('%');
        expect(wrapper.text()).toContain('/books1/Amazon/large-book.azw3');
    });
    it('shows waiting and monitor gaps instead of pretending completion', () => {
        const wrapper = render({ mode: 'incremental', state: 'waiting', recheck_required: true, error: 'mount unavailable' });
        expect(wrapper.text()).toContain('等待目录恢复');
        expect(wrapper.text()).toContain('监控事件缺口');
        expect(wrapper.get('[role="alert"]').text()).toBe('mount unavailable');
    });
    it('has no fabricated progress before a task exists', () => {
        expect(render(null).find('section').exists()).toBe(false);
    });
    it.each(['zh-CN', 'en-US', 'ru-RU'])('has translated task states and controls in %s', (locale) => {
        const data = JSON.parse(readFileSync(resolve(process.cwd(), `i18n/locales/${locale}.json`), 'utf8')).admin.imports.incremental;
        for (const key of ['scan', 'resume', 'full', 'baselineHint', 'eventGap', 'countHint']) expect(data[key]).toBeTruthy();
        for (const key of ['queued', 'running', 'paused', 'waiting', 'failed', 'done']) expect(data.state[key]).toBeTruthy();
        expect(JSON.stringify(data)).not.toMatch(/[@<]/);
    });
});
