import { afterEach, describe, expect, it, vi } from 'vitest';
import { mount, type VueWrapper } from '@vue/test-utils';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import AccessRangeEditor from '@/components/admin/AccessRangeEditor.vue';
import AccessPreparationStatus from '@/components/admin/AccessPreparationStatus.vue';
import type { AccessCeiling, AccessRevision } from '@/types/access';

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }));
global.ResizeObserver = require('resize-observer-polyfill');
const vuetify = createVuetify({ components, directives });
const mounted: VueWrapper[] = [];
afterEach(() => { while (mounted.length) mounted.pop()?.unmount(); });

describe('AccessRangeEditor', () => {
    const range = (): AccessCeiling => ({
        mode: 'selected', targets: [{ root: 'source', directory: '/children', recursive: true }],
    });
    function editor(value = range(), disabled = false) {
        const wrapper = mount(AccessRangeEditor, {
            props: { modelValue: value, disabled }, global: { plugins: [vuetify] },
        });
        mounted.push(wrapper);
        return wrapper;
    }
    it('removing the last target preserves restriction instead of becoming public', async () => {
        const value = range();
        const wrapper = editor(value);
        await wrapper.findAll('button').find(button => button.text() === 'accessControl.range.remove')!.trigger('click');
        expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([{ mode: 'selected', targets: [] }]);
        expect(value.targets).toHaveLength(1);
    });
    it('explicitly clearing the mode removes stale targets without mutating props', async () => {
        const value = range();
        const wrapper = editor(value);
        wrapper.findComponent(components.VRadioGroup).vm.$emit('update:modelValue', 'deny_all');
        expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([{ mode: 'deny_all', targets: [] }]);
        expect(value.mode).toBe('selected');
        expect(value.targets).toHaveLength(1);
    });
    it('disabled editors cannot emit programmatic modifications', () => {
        const wrapper = editor(range(), true);
        wrapper.findComponent(components.VRadioGroup).vm.$emit('update:modelValue', 'unrestricted');
        wrapper.findComponent(components.VCheckbox).vm.$emit('update:modelValue', false);
        expect(wrapper.emitted('update:modelValue')).toBeUndefined();
    });
    it('requests a directory picker without granting an empty selection', async () => {
        const wrapper = editor({ mode: 'selected', targets: [] });
        expect(wrapper.get('[role="status"]').text()).toBe('accessControl.range.empty');
        await wrapper.findAll('button').find(button => button.text() === 'accessControl.range.addDirectory')!.trigger('click');
        expect(wrapper.emitted('choose-directory')).toHaveLength(1);
        expect(wrapper.emitted('update:modelValue')).toBeUndefined();
    });
    it('adds a controlled category without replacing directory targets', async () => {
        const wrapper = editor();
        await wrapper.setProps({ classes: [{ id: 'fiction', name: '小说' }] });
        wrapper.findComponent(components.VSelect).vm.$emit('update:modelValue', 'fiction');
        expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([{ mode: 'selected', targets: [...range().targets, { class: 'fiction' }] }]);
        wrapper.findComponent(components.VSelect).vm.$emit('update:modelValue', 'not-a-category');
        expect(wrapper.emitted('update:modelValue')).toHaveLength(1);
    });
});

describe('AccessPreparationStatus', () => {
    function status(state: AccessRevision['status'], total = 100, processed = 100) {
        const wrapper = mount(AccessPreparationStatus, {
            props: {
                revision: { id: 1, version: 1, base_revision: 0, status: state, total, processed, message: '', updated_at: 1 },
            }, global: { plugins: [vuetify] },
        });
        mounted.push(wrapper);
        return wrapper;
    }
    it('a full progress bar does not claim that permissions were applied', () => {
        const wrapper = status('preparing');
        expect(wrapper.text()).toContain('accessControl.preparation.notActive');
        expect(wrapper.text()).not.toContain('accessControl.preparation.active');
        expect(wrapper.findAll('button').some(button => button.text() === 'accessControl.preparation.review')).toBe(false);
    });
    it('unknown totals use indeterminate progress', () => {
        const wrapper = status('preparing', 0, 0);
        expect(wrapper.findComponent(components.VProgressLinear).props('indeterminate')).toBe(true);
        expect(wrapper.text()).toContain('accessControl.preparation.counting');
    });
    it('ready requires a separate impact review and exposes no direct apply action', async () => {
        const wrapper = status('ready');
        await wrapper.findAll('button').find(button => button.text() === 'accessControl.preparation.review')!.trigger('click');
        expect(wrapper.emitted('review')).toHaveLength(1);
        expect(wrapper.emitted('apply')).toBeUndefined();
        expect(wrapper.text()).toContain('accessControl.preparation.notActive');
    });
});
