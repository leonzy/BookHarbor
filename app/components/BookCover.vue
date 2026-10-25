<template>
    <button
        v-bind="$attrs"
        type="button"
        :aria-label="$t('coverPreview.open', { title: title || alt })"
        :title="$t('coverPreview.open', { title: title || alt })"
        :disabled="!src"
        :style="{ width: dimension($attrs.width), maxWidth: '100%' }"
        class="book-cover-preview-trigger"
        @click.stop.prevent="open(previewSrc || src, title || alt)"
    >
        <v-img
            v-bind="imageProps()"
            :src="src"
            alt=""
            width="100%"
        />
    </button>
</template>

<script setup>
import { useCoverPreview } from '@/composables/useCoverPreview';
import { useAttrs } from 'vue';

defineOptions({ inheritAttrs: false });
defineProps({
    src: { type: String, default: '' },
    previewSrc: { type: String, default: '' },
    title: { type: String, default: '' },
    alt: { type: String, default: '' },
});
const { open } = useCoverPreview();
const attrs = useAttrs();
const imageProps = () => Object.fromEntries(Object.entries(attrs).filter(([key]) =>
    ['height', 'max-height', 'maxHeight', 'min-height', 'minHeight', 'aspect-ratio', 'aspectRatio', 'cover', 'contain', 'eager'].includes(key)));
function dimension(value) {
    if (value === undefined) return '100%';
    return /^\d+(\.\d+)?$/.test(String(value)) ? `${value}px` : value;
}
</script>

<style scoped>
.book-cover-preview-trigger {
    display: block;
    flex-shrink: 0;
    padding: 0;
    border: 0;
    background: transparent;
    cursor: zoom-in;
    color: inherit;
    text-align: inherit;
}
.book-cover-preview-trigger:focus-visible {
    outline: 3px solid rgb(var(--v-theme-primary));
    outline-offset: 3px;
}
.book-cover-preview-trigger:disabled { cursor: default; }
</style>
