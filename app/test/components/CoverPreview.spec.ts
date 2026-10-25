import { describe, it, expect, vi, beforeEach } from 'vitest';
import { mount } from '@vue/test-utils';
import { createVuetify } from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import BookCover from '@/components/BookCover.vue';
import CoverPreview from '@/components/CoverPreview.vue';
import { useCoverPreview } from '@/composables/useCoverPreview';

vi.mock('#app', async () => {
    const { ref } = await import('vue');
    const state = ref({ src: '', title: '', open: false });
    return { useState: () => state };
});
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }));
global.ResizeObserver = require('resize-observer-polyfill');
const vuetify = createVuetify({ components, directives });
const globalOptions = { plugins: [vuetify], mocks: { $t: (key: string) => key } };

describe('cover preview', () => {
    beforeEach(() => { useCoverPreview().preview.value = { src: '', title: '', open: false }; });

    it('opens the full cover from a thumbnail without bubbling to navigation', async () => {
        const outerClick = vi.fn();
        const wrapper = mount(BookCover, { props: { src: '/prefix/get/thumb_60x80/12.jpg', title: '三体' }, global: globalOptions });
        wrapper.element.parentElement?.addEventListener('click', outerClick);
        const button = wrapper.get('button');
        expect(button.attributes('aria-label')).toBe('coverPreview.open');
        await button.trigger('click');
        expect(useCoverPreview().preview.value).toEqual({ src: '/prefix/get/cover/12.jpg', title: '三体', open: true });
        expect(outerClick).not.toHaveBeenCalled();
        wrapper.unmount();
    });

    it('supports external covers and an explicitly supplied original image', async () => {
        const wrapper = mount(BookCover, { props: { src: '/thumb.jpg', previewSrc: 'https://example.com/original.jpg', title: '原图' }, global: globalOptions });
        await wrapper.get('button').trigger('click');
        expect(useCoverPreview().preview.value.src).toBe('https://example.com/original.jpg');
        wrapper.unmount();
    });

    it('does not guess original endpoints on external providers', () => {
        useCoverPreview().open('https://example.com/get/thumb/1');
        expect(useCoverPreview().preview.value.src).toBe('https://example.com/get/thumb/1');
    });

    it('disables missing cover actions and ignores an empty source', () => {
        const wrapper = mount(BookCover, { global: globalOptions });
        expect(wrapper.get('button').attributes('disabled')).toBeDefined();
        useCoverPreview().open('');
        expect(useCoverPreview().preview.value.open).toBe(false);
        wrapper.unmount();
    });

    it('renders a single shared dialog closed until a cover is requested', () => {
        const wrapper = mount(CoverPreview, { global: globalOptions });
        expect(wrapper.getComponent({ name: 'VDialog' }).props('modelValue')).toBe(false);
        wrapper.unmount();
    });
});
