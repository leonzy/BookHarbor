import { describe, expect, it } from 'vitest';
import { createI18n } from 'vue-i18n';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
const [zh, en, ru] = ['zh-CN', 'en-US', 'ru-RU'].map(locale => JSON.parse(readFileSync(resolve(`i18n/locales/${locale}.json`), 'utf8')));
const leaves = (value: Record<string, unknown>, prefix: string): string[] => Object.entries(value).flatMap(([key, child]) => typeof child === 'string' ? [`${prefix}.${key}`] : leaves(child as Record<string, unknown>, `${prefix}.${key}`));
describe('Metadata progress and storage translations', () => {
    for (const [locale, messages] of Object.entries({ 'zh-CN': zh, 'en-US': en, 'ru-RU': ru })) {
        it(locale, () => {
            const errors: string[] = [];
            const i18n = createI18n({ legacy: false, locale, messages: { [locale]: messages }, onError: (error: Error) => errors.push(error.message) });
            expect(Object.keys(messages.metadataProgress).sort()).toEqual(Object.keys(zh.metadataProgress).sort());
            expect(leaves(messages.metadataProgress, 'metadataProgress').sort()).toEqual(leaves(zh.metadataProgress, 'metadataProgress').sort());
            for (const key of [...leaves(messages.metadataProgress, 'metadataProgress'), ...Object.keys(messages.bookStorage).map(k => `bookStorage.${k}`), 'coverPreview.unavailable', 'book.filteredMetadataCandidates']) {
                expect(i18n.global.t(key, { checked: 4, total: 10, id: 12, count: 3, time: '12:00' })).not.toBe(key);
            }
            expect(errors).toEqual([]);
        });
    }
});
