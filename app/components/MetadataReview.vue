<template>
    <details
        class="metadata-review"
        @toggle="toggle"
    >
        <summary>{{ t('metadataProgress.reviewQueue', { count }) }}</summary>
        <p class="text-body-2 mt-3">
            {{ t('metadataProgress.reviewHelp') }}
        </p>
        <v-btn
            variant="text"
            size="small"
            :loading="loading"
            @click="load(true)"
        >
            {{ t('metadataProgress.reviewRefresh') }}
        </v-btn>
        <v-alert
            v-if="error"
            type="error"
            variant="tonal"
            density="compact"
            role="alert"
        >
            {{ t('metadataProgress.reviewLoadError') }}
        </v-alert>
        <p
            v-if="loaded && !books.length"
            class="text-body-2"
            role="status"
        >
            {{ t('metadataProgress.reviewEmpty') }}
        </p>
        <ul class="metadata-review__books">
            <li
                v-for="book in books"
                :key="book.id"
            >
                <a
                    :href="`/book/${book.id}`"
                    target="_blank"
                    rel="noopener noreferrer"
                >{{ book.title }} · #{{ book.id }}</a>
                <p class="text-body-2">
                    {{ book.reason }}
                </p>
                <a
                    :href="`/book/${book.id}/edit`"
                    target="_blank"
                    rel="noopener noreferrer"
                    :aria-label="t('metadataProgress.reviewEditBook', { title: book.title })"
                >{{ t('metadataProgress.reviewEdit') }}</a>
            </li>
        </ul>
        <v-btn
            v-if="next"
            variant="outlined"
            :loading="loading"
            @click="load(false)"
        >
            {{ t('metadataProgress.reviewMore') }}
        </v-btn>
    </details>
</template>

<script setup>
import { ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { useNuxtApp } from 'nuxt/app';
const props = defineProps({ count: { type: Number, default: 0 } });
const { t } = useI18n();
const { $backend } = useNuxtApp();
const books = ref([]), next = ref(0), loading = ref(false), error = ref(false), loaded = ref(false), open = ref(false);
function toggle(event) { open.value = event.target.open; if (open.value && !loaded.value) load(true); }
watch(() => props.count, () => { if (open.value && loaded.value) load(true); });
async function load(reset) {
    if (loading.value) return;
    loading.value = true; error.value = false;
    try {
        const result = await $backend(`/admin/metadata/enrichment/review?after=${reset ? 0 : next.value}`);
        if (result.err !== 'ok') throw new Error(result.err);
        books.value = reset ? result.books : [...new Map([...books.value, ...result.books].map(book => [book.id, book])).values()];
        next.value = result.next_after; loaded.value = true;
    } catch { error.value = true; }
    finally { loading.value = false; }
}
</script>

<style scoped>
.metadata-review { margin-block: 16px; padding: 12px; border: 1px solid rgba(var(--v-theme-on-surface), .15); border-radius: 8px; overflow-wrap: anywhere; }
.metadata-review summary { cursor: pointer; font-weight: 600; padding-inline-end: 48px; }
.metadata-review__books a { display: inline-block; max-inline-size: calc(100% - 48px); }
.metadata-review summary:focus-visible, .metadata-review a:focus-visible { outline: 2px solid rgb(var(--v-theme-primary)); outline-offset: 3px; }
.metadata-review__books { list-style: none; padding: 0; }
.metadata-review__books li { padding-block: 12px; border-block-end: 1px solid rgba(var(--v-theme-on-surface), .15); }
.metadata-review__books p { margin-block: 6px; }
.metadata-review :deep(.v-btn) { max-inline-size: calc(100% - 48px); height: auto; min-block-size: 36px; padding-block: 8px; }
.metadata-review :deep(.v-btn__content) { white-space: normal; line-height: 1.4; }
</style>
