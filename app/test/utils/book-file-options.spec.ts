import { describe, it, expect } from 'vitest';
import { bookFileOptions } from '~/utils/book-file-options';

describe('version file actions', () => {
    it('keeps different versions of the same format and sends their actual IDs', () => {
        const choices = bookFileOptions({ id: 7, files: [{ format: 'EPUB', size: 2048 }], versions: [
            { id: 8, files: [{ format: 'epub' }, { format: 'MOBI' }] },
            { id: 7, files: [{ format: 'EPUB' }] },
        ] });
        expect(choices.map(c => c.key)).toEqual(['7:EPUB', '8:EPUB', '8:MOBI']);
        expect(choices.map(c => c.readerPath)).toEqual(['/read/7?format=epub', '/read/8?format=epub', '/read/8?format=mobi']);
        expect(choices[0].title).toContain('2.0 KiB');
    });
    it('excludes unsupported reading formats without hiding them from sending', () => {
        const choices = bookFileOptions({ id: 1, files: [{ format: 'DJVU' }, { format: 'TXT' }] });
        expect(choices[0].readerPath).toBe('');
        expect(choices[1].readerPath).toBe('/read/1?format=txt');
        expect(bookFileOptions({ id: 0 })).toEqual([]);
        expect(bookFileOptions({ id: 1, files: [] })).toEqual([]);
    });
    it('retains each comic version media type and format', () => {
        const choices = bookFileOptions({ id: 1, files: [], versions: [{ id: 2, media_type: 'comic', files: [{ format: 'CBZ' }, { format: 'ZIP' }] }] });
        expect(choices.map(c => c.readerPath)).toEqual(['/read-comic/2?format=cbz', '/read-comic/2?format=zip']);
    });
});
