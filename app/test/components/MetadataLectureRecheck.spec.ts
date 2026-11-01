import { describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import { createI18n } from 'vue-i18n';
import { baseCompile } from '@intlify/message-compiler';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import MetadataLectureRecheck from '@/components/MetadataLectureRecheck.vue';

for (const locale of ['zh-CN', 'en-US', 'ru-RU']) {
    const messages = JSON.parse(readFileSync(resolve(`i18n/locales/${locale}.json`), 'utf8'));
    const i18n = createI18n({ legacy: false, locale, messages: { [locale]: messages } });
    describe(`Lecture recheck ${locale}`, () => {
        it('renders every outcome, discovery and completed progress without exposing source paths', async () => {
            const counts = { repaired: 10, no_evidence: 2, protected: 1, unreadable: 1 };
            const w = mount(MetadataLectureRecheck, { props: { state: { total: 20, checked: 14, discovery_done: false, counts } }, global: { plugins: [i18n] } });
            expect(w.get('summary').text()).toContain('14 / 20');
            expect(w.findAll('dt')).toHaveLength(8);
            expect(w.text()).toContain(messages.lectureRecheck.discovering);
            expect(w.text()).toContain(messages.lectureRecheck.explanation);
            await w.setProps({ state: { total: 20, checked: 20, discovery_done: true, counts } });
            expect(w.text()).toContain(messages.lectureRecheck.completeDiscovery);
            expect(w.get('summary').text()).toContain('20 / 20');
            w.unmount();
        });
        it('does not invent a result when the older API omits the ledger', () => {
            const w = mount(MetadataLectureRecheck, { global: { plugins: [i18n] } });
            expect(w.find('details').exists()).toBe(false);
            w.unmount();
        });
        it('compiles all outcome and explanatory translations', () => {
            for (const text of Object.values(messages.lectureRecheck)) {
                const errors: string[] = [];
                baseCompile(String(text), { onError: e => errors.push(e.message) });
                expect(errors).toEqual([]);
            }
        });
    });
}
