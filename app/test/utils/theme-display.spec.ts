import { describe, expect, it } from 'vitest';
import { buildThemeDisplayList } from '~/utils/theme-display';

const t = (key: string) => ({
    'theme.defaultTheme': '默认主题',
    'theme.defaultThemeDescription': 'BookHarbor 原生界面风格',
}[key] || key);

describe('theme-display', () => {
    it('keeps theme list order stable when a builtin theme is active', () => {
        const displayThemes = buildThemeDisplayList([
            { name: 'brass', active: false },
            { name: 'light-gray', active: true },
            { name: 'minimal', active: false },
        ], t);

        expect(displayThemes.map(theme => theme._key)).toEqual([
            '__default__',
            'brass',
            'light-gray',
            'minimal',
        ]);
        expect(displayThemes[0].active).toBe(false);
    });

    it('marks default active only when no custom or builtin theme is active', () => {
        const displayThemes = buildThemeDisplayList([
            { name: 'brass', active: false },
        ], t);

        expect(displayThemes.map(theme => theme._key)).toEqual(['__default__', 'brass']);
        expect(displayThemes[0].active).toBe(true);
    });

    it('keeps internal theme key when a builtin theme has a display name', () => {
        const displayThemes = buildThemeDisplayList([
            { name: 'light-gray', display_name: '浅灰主题', active: true },
        ], t);

        expect(displayThemes[1]._key).toBe('light-gray');
        expect(displayThemes[1].display_name).toBe('浅灰主题');
    });
});
