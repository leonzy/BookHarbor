<template>
    <v-dialog
        v-model="preview.open"
        :transition="false"
        max-width="1200"
        class="cover-preview-dialog"
        :aria-label="t('coverPreview.title', { title: preview.title })"
        @after-leave="restoreFocus"
    >
        <v-card class="cover-preview-panel">
            <div class="cover-preview-toolbar">
                <h2
                    class="text-subtitle-1"
                    :title="preview.title"
                >
                    {{ t('coverPreview.title', { title: preview.title }) }}
                </h2>
                <div class="cover-preview-controls">
                    <v-btn
                        :aria-label="t('coverPreview.zoomOut')"
                        :disabled="scale <= 1"
                        @click="scale = Math.max(1, scale - .5)"
                    >
                        −
                    </v-btn>
                    <v-btn
                        :aria-label="`${t('coverPreview.reset')} ${Math.round(scale * 100)}%`"
                        @click="scale = 1"
                    >
                        {{ Math.round(scale * 100) }}%
                    </v-btn>
                    <v-btn
                        :aria-label="t('coverPreview.zoomIn')"
                        :disabled="scale >= 4"
                        @click="scale = Math.min(4, scale + .5)"
                    >
                        +
                    </v-btn>
                    <v-btn
                        :aria-label="t('coverPreview.close')"
                        @click="preview.open = false"
                    >
                        {{ t('coverPreview.close') }}
                    </v-btn>
                </div>
            </div>
            <div
                ref="viewport"
                class="cover-preview-viewport"
                tabindex="0"
                :aria-label="t('coverPreview.imageArea')"
            >
                <div
                    v-if="failed"
                    class="pa-6 text-center"
                    role="alert"
                >
                    <p>{{ t('coverPreview.failed') }}</p>
                    <v-btn
                        class="mt-3"
                        @click="failed = false; attempt++"
                    >
                        {{ t('coverPreview.retry') }}
                    </v-btn>
                </div>
                <div
                    v-else
                    class="cover-preview-canvas"
                    :style="{ width: imageSize.width ? `${Math.max(viewportSize.width, imageSize.width + 24)}px` : '100%' }"
                >
                    <img
                        :key="`${preview.src}:${attempt}`"
                        :src="preview.src"
                        :alt="preview.title || t('book.cover')"
                        class="cover-preview-image"
                        :style="{ width: imageSize.width ? `${imageSize.width}px` : undefined, height: imageSize.height ? `${imageSize.height}px` : undefined }"
                        @load="imageLoaded"
                        @error="failed = true"
                    >
                </div>
            </div>
        </v-card>
    </v-dialog>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { useCoverPreview } from '@/composables/useCoverPreview';

const { t } = useI18n();
const { preview } = useCoverPreview();
const scale = ref(1);
const viewport = ref();
const viewportSize = ref({ width: 0, height: 0 });
const natural = ref({ width: 0, height: 0 });
let observer;
watch(viewport, element => {
    observer?.disconnect();
    if (!element) return;
    const measure = () => { viewportSize.value = { width: element.clientWidth, height: element.clientHeight }; };
    measure();
    observer = new ResizeObserver(measure);
    observer.observe(element);
});
onBeforeUnmount(() => observer?.disconnect());
const imageSize = computed(() => {
    if (!natural.value.width || !viewportSize.value.width || !viewportSize.value.height) return {};
    const fit = Math.max(0, Math.min((viewportSize.value.width - 24) / natural.value.width, (viewportSize.value.height - 24) / natural.value.height));
    return { width: natural.value.width * fit * scale.value, height: natural.value.height * fit * scale.value };
});
function imageLoaded(event) {
    natural.value = { width: event.target.naturalWidth, height: event.target.naturalHeight };
}
watch([scale, viewportSize, natural], async () => {
    await nextTick();
    if (!viewport.value) return;
    viewport.value.scrollLeft = (viewport.value.scrollWidth - viewport.value.clientWidth) / 2;
    viewport.value.scrollTop = (viewport.value.scrollHeight - viewport.value.clientHeight) / 2;
});
const failed = ref(false);
const attempt = ref(0);
let activator;
watch(() => preview.value.open, value => {
    if (value) {
        activator = document.activeElement;
        scale.value = 1;
        natural.value = { width: 0, height: 0 };
        failed.value = false;
        attempt.value = 0;
    }
});
function restoreFocus() {
    if (activator?.isConnected) activator.focus();
}
</script>

<style scoped>
.cover-preview-panel { max-height: 94dvh; display: flex; flex-direction: column; }
.cover-preview-toolbar { flex-shrink: 0; display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; padding: 12px; }
.cover-preview-toolbar h2 { min-width: 0; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.cover-preview-controls { display: flex; flex-wrap: wrap; gap: 4px; }
.cover-preview-controls .v-btn { min-width: 44px; padding-inline: 10px; }
.cover-preview-viewport { flex: 1 1 auto; height: 70dvh; overflow: auto; overscroll-behavior: contain; min-height: 120px; max-height: calc(94dvh - 130px); background: rgba(var(--v-theme-on-surface), .06); }
.cover-preview-viewport:focus-visible { outline: 3px solid rgb(var(--v-theme-primary)); outline-offset: -3px; }
.cover-preview-canvas { min-width: 100%; min-height: 100%; box-sizing: border-box; padding: 12px; display: flex; align-items: center; justify-content: center; }
.cover-preview-image { display: block; max-width: none; flex-shrink: 0; object-fit: contain; }
</style>
