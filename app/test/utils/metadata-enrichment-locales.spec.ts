import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { baseCompile } from '@intlify/message-compiler';
import { createI18n } from 'vue-i18n';
import { describe, expect, it } from 'vitest';

describe('metadata enrichment settings messages', () => {
    it.each(['zh-CN', 'en-US', 'ru-RU'])('renders the controls and explanation in %s', (locale) => {
        const messages = JSON.parse(readFileSync(resolve(process.cwd(), `i18n/locales/${locale}.json`), 'utf8'));
        const { global: i18n } = createI18n({ legacy: false, locale, messages: { [locale]: messages } });
        for (const key of ['admin.settings.label.autoFillMeta', 'admin.settings.label.autoFillKeepCover', 'pluginManagement.metadataBehaviorDescription']) {
            const text = key.split('.').reduce((value, segment) => value[segment], messages);
            const errors: string[] = [];
            baseCompile(text, { onError: error => errors.push(error.message) });
            expect(errors, key).toEqual([]);
            expect(text.trim(), key).not.toBe('');
            expect(i18n.t(key)).toBe(text);
        }
    });
});
