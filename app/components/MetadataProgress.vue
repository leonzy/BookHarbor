<template>
    <section
        id="metadata-progress"
        class="metadata-progress"
        data-testid="metadata-progress"
        :aria-label="t('metadataProgress.title')"
    >
        <div class="metadata-progress__heading">
            <h2 class="text-h6">
                {{ t('metadataProgress.title') }}
            </h2>
            <v-btn
                variant="text"
                size="small"
                :loading="loading"
                @click="load"
            >
                {{ t('metadataProgress.refresh') }}
            </v-btn>
        </div>
        <p class="text-body-2 mb-3">
            {{ t('metadataProgress.description') }}
        </p>
        <v-alert
            v-if="error"
            type="error"
            variant="tonal"
            density="compact"
        >
            {{ t('metadataProgress.loadError') }}
        </v-alert>
        <template v-if="state">
            <p
                class="text-body-2 mb-2"
                aria-live="polite"
            >
                {{ t('metadataProgress.checked', { checked: state.files_checked, total: state.total_books }) }}
            </p>
            <v-progress-linear
                :model-value="percent"
                :aria-label="t('metadataProgress.checked', { checked: state.files_checked, total: state.total_books })"
                color="primary"
                height="6"
                rounded
            />
            <dl class="metadata-progress__counts">
                <div
                    v-for="item in counters"
                    :key="item.key"
                >
                    <dt>{{ t('metadataProgress.' + item.key) }}</dt><dd>{{ item.value.toLocaleString() }}</dd>
                </div>
            </dl>
            <p class="text-caption mb-1">
                {{ t(state.worker_active ? 'metadataProgress.running' : 'metadataProgress.paused') }} · {{ t(state.network_enabled ? 'metadataProgress.onlineOn' : 'metadataProgress.onlineOff') }}
            </p>
            <p class="text-caption mb-1">
                {{ t('metadataProgress.rate') }}
            </p>
            <MetadataSourceLimits
                :sources="state.sources || []"
                @saved="load"
            />
            <p
                v-if="state.last_activity_at"
                class="text-caption"
            >
                {{ t('metadataProgress.lastActivity', { time: new Date(state.last_activity_at * 1000).toLocaleString() }) }}
            </p>
        </template>
        <v-progress-linear
            v-else-if="loading"
            indeterminate
            :aria-label="t('metadataProgress.title')"
        />
    </section>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import { useI18n } from 'vue-i18n';
import { useNuxtApp } from 'nuxt/app';
import MetadataSourceLimits from '@/components/MetadataSourceLimits.vue';
const { t } = useI18n();
const { $backend } = useNuxtApp();
const state = ref(null);
const loading = ref(false);
const error = ref(false);
let timer;
let disposed = false;
const percent = computed(() => state.value?.total_books ? Math.min(100, state.value.files_checked * 100 / state.value.total_books) : 0);
const counters = computed(() => {
    if (!state.value) return [];
    const value = state.value;
    const counts = value.counts || {};
    return [
        ['remaining', value.files_remaining], ['filled', value.written_books], ['complete', counts.complete], ['partial', counts.partial],
        ['waiting', counts.network_pending], ['retrying', value.network_retries],
        ['applied', value.network_applied_books], ['review', counts.needs_review],
        ['fileRefresh', counts.file_refresh], ['unreadable', Math.max(0, (counts.file_retry || 0) - (counts.file_refresh || 0))], ['protected', counts.protected],
    ].map(([key, count]) => ({ key, value: Number(count || 0) }));
});
async function load() {
    if (loading.value || disposed) return;
    loading.value = true;
    try {
        const response = await $backend('/admin/metadata/enrichment');
        if (response.err !== 'ok') throw new Error(response.err);
        if (!disposed) { state.value = response; error.value = false; }
    } catch {
        if (!disposed) error.value = true;
    } finally {
        loading.value = false;
    }
}
function refreshVisible() {
    if (!document.hidden) load();
}
onMounted(() => {
    load();
    timer = setInterval(refreshVisible, 10000);
    document.addEventListener('visibilitychange', refreshVisible);
});
onBeforeUnmount(() => {
    disposed = true;
    clearInterval(timer);
    document.removeEventListener('visibilitychange', refreshVisible);
});
</script>

<style scoped>
.metadata-progress { margin-block: 16px 24px; padding: 16px; border: 1px solid rgba(var(--v-theme-on-surface), .15); border-radius: 8px; }
.metadata-progress__heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.metadata-progress__heading h2 { min-width: 0; overflow-wrap: anywhere; }
.metadata-progress__counts { display: grid; grid-template-columns: repeat(auto-fit, minmax(115px, 1fr)); gap: 12px; margin-block: 16px; }
.metadata-progress__counts dt { font-size: 12px; }
.metadata-progress__counts dd { margin: 4px 0 0; font-size: 20px; font-variant-numeric: tabular-nums; }
</style>
