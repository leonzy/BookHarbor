import { describe, it, expect, vi } from 'vitest';
import { mount } from '@vue/test-utils';
import { reactive } from 'vue';
import { createI18n } from 'vue-i18n';
import BookSizeSort from '@/components/BookSizeSort.vue';

const state = vi.hoisted(() => ({ push: vi.fn(), route: { query: {} as Record<string, string> } }));
vi.mock('vue-router', () => ({ useRoute: () => state.route, useRouter: () => ({ push: state.push }) }));
const mountSort = (props = {}) => mount(BookSizeSort, {
    props,
    global: {
        plugins: [createI18n({ legacy: false, locale: 'en-US', messages: { 'en-US': {} } })],
        stubs: { VSelect: { props: ['modelValue', 'items'], emits: ['update:modelValue'], template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="item in items" :key="item.value" :value="item.value">{{ item.title }}</option></select>' } },
    },
});
describe('Book size sort', () => {
    it('keeps filters while resetting pagination', async () => {
        state.route = reactive({ query: { sort: 'size_desc', publisher: 'Press', start: '120', page: '3', path: '/books' } });
        state.push.mockClear();
        const wrapper = mountSort();
        expect(wrapper.get('select').element.value).toBe('size_desc');
        await wrapper.get('select').setValue('size_asc');
        expect(state.push).toHaveBeenCalledWith({ query: { sort: 'size_asc', publisher: 'Press', path: '/books', start: undefined, page: undefined } });
        wrapper.unmount();
    });
    it('supports a controlled value without changing the route', async () => {
        state.push.mockClear();
        const wrapper = mountSort({ modelValue: '' });
        await wrapper.get('select').setValue('size_desc');
        expect(wrapper.emitted('update:modelValue')).toEqual([['size_desc']]);
        expect(state.push).not.toHaveBeenCalled();
        wrapper.unmount();
    });
});
