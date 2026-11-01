import { describe, expect, it } from 'vitest';
import { builtinPluginType } from '../../utils/builtin-plugins';

describe('built-in plugin categories', () => {
    it('keeps the new Google Books and Amazon providers in metadata', () => {
        for (const plugin_key of ['bookharbor.meta.google-books', 'bookharbor.meta.amazon']) {
            expect(builtinPluginType({ plugin_key })).toBe('meta');
        }
    });
    it('retains existing integrations, device controls and unknown fallbacks', () => {
        expect(builtinPluginType({ plugin_key: 'talebook.combo.weread' })).toBe('combo');
        expect(builtinPluginType({ plugin_key: 'talebook.push.boox' })).toBe('push');
        expect(builtinPluginType({ plugin_key: 'external.meta.test' })).toBe('');
        expect(builtinPluginType()).toBe('');
    });
});
