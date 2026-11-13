import { formatFileSize } from './file-size';
import { readerPathForBook } from './comic-reader';

export function bookFileOptions(book: any) {
    const seen = new Set<string>();
    return [book, ...(book?.versions || [])].flatMap(version => {
        const bookId = Number(version?.id);
        if (!Number.isSafeInteger(bookId) || bookId <= 0) return [];
        return (version.files || []).flatMap((file: any) => {
            const format = String(file.format || '').trim().toUpperCase();
            const key = `${bookId}:${format}`;
            if (!format || seen.has(key)) return [];
            seen.add(key);
            const reader = readerPathForBook({ ...version, files: [file] });
            return [{
                key, bookId, format, size: file.size,
                href: file.href || `/api/book/${bookId}.${format}`,
                title: `${format} · ${formatFileSize(file.size)} · #${bookId}`,
                bookTitle: version.title || book.title || '',
                readerPath: reader ? `${reader}?format=${encodeURIComponent(format.toLowerCase())}` : '',
            }];
        });
    });
}
