import { useState } from '#app';

export function useCoverPreview() {
    const preview = useState('book-cover-preview', () => ({ src: '', title: '', open: false }));
    function open(src: string, title = '') {
        if (!src) return;
        // Thumbnail routes contain the same book ID as the original cover.
        preview.value = { src: src.startsWith('/') ? src.replace(/\/get\/thumb(?:_\d+x\d+)?\//, '/get/cover/') : src, title, open: true };
    }
    return { preview, open };
}
