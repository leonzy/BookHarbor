import { existsSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const appRoot = process.cwd();

describe('Nuxt i18n configuration', () => {
    it('resolves vueI18n from the module i18n directory', () => {
        const config = readFileSync(resolve(appRoot, 'nuxt.config.ts'), 'utf8');
        const match = config.match(/vueI18n:\s*['"]([^'"]+)['"]/);
        expect(match).not.toBeNull();
        expect(existsSync(resolve(appRoot, 'i18n', match![1]))).toBe(true);
    });
});
