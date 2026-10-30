<template>
    <section
        v-if="sources.length"
        class="source-limits"
        :aria-label="t('metadataProgress.sources')"
    >
        <h3 class="text-subtitle-1 mb-2">
            {{ t('metadataProgress.sources') }}
        </h3>
        <p class="text-caption mb-3">
            {{ t('metadataProgress.sourceHelp') }}
        </p>
        <div
            v-for="source in sources"
            :key="source.id"
            class="source-limit"
            :data-source-id="source.id"
        >
            <div class="source-limit__description">
                <h4 class="text-body-1 font-weight-medium">
                    {{ source.name }}
                </h4>
                <p class="text-caption">
                    {{ t('metadataProgress.sourceCounts', { requests: source.requests, processed: source.processed, matched: source.matched }) }}
                </p>
                <p
                    class="text-caption source-limit__status"
                    role="status"
                >
                    {{ source.unavailable ? t('metadataProgress.unavailableReasons.' + source.unavailable) : source.failures ? t('metadataProgress.sourceFailureReasons.' + (source.last_error || 'request_failed')) : '' }}
                </p>
                <a
                    v-if="source.last_failure_run"
                    class="text-caption"
                    :href="`/admin/plugins/runs/${source.last_failure_run}`"
                    target="_blank"
                    rel="noopener noreferrer"
                    :aria-label="t('metadataProgress.sourceFailureLink', { name: source.name, code: source.last_failure_code })"
                >{{ t('metadataProgress.sourceFailureLink', { name: source.name, code: source.last_failure_code }) }}</a>
            </div>
            <form
                novalidate
                class="source-limit__form"
                :aria-label="t('metadataProgress.sourceSettings', { name: source.name })"
                @submit.prevent="save(source)"
            >
                <v-text-field
                    :ref="el => fields[source.id] = el"
                    v-model="drafts[source.id]"
                    :label="t('metadataProgress.intervalLabel')"
                    :aria-label="t('metadataProgress.interval', { name: source.name })"
                    type="number"
                    min="1"
                    max="3600"
                    step="1"
                    inputmode="numeric"
                    :error-messages="errors[source.id] || []"
                    :disabled="saving[source.id]"
                    density="compact"
                    variant="outlined"
                    hide-details="auto"
                    @update:model-value="edit(source.id)"
                />
                <v-btn
                    type="submit"
                    variant="outlined"
                    :loading="saving[source.id]"
                    :aria-label="t('metadataProgress.saveInterval', { name: source.name })"
                >
                    {{ t('common.save') }}
                </v-btn>
            </form>
            <p
                class="text-caption source-limit__saved"
                role="status"
            >
                {{ saved[source.id] ? t('metadataProgress.intervalSaved', { name: source.name }) : '' }}
            </p>
        </div>
    </section>
</template>

<script setup>
import { reactive, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { useNuxtApp } from 'nuxt/app';
const props = defineProps({ sources: { type: Array, default: () => [] } });
const emit = defineEmits(['saved']);
const { t } = useI18n();
const { $backend } = useNuxtApp();
const fields = {}, confirmed = {};
const drafts = reactive({}), dirty = reactive({}), saving = reactive({}), errors = reactive({}), saved = reactive({});
watch(() => props.sources, sources => {
    for (const source of sources) if (!dirty[source.id] && (confirmed[source.id] === undefined || source.interval_seconds === confirmed[source.id])) { drafts[source.id] = source.interval_seconds; delete confirmed[source.id]; }
}, { immediate: true });
function edit(id) { dirty[id] = true; errors[id] = ''; saved[id] = false; }
async function save(source) {
    const id = source.id, raw = String(drafts[id] ?? '').trim(), seconds = Number(raw);
    saved[id] = false;
    if (!raw || !Number.isInteger(seconds) || seconds < 1 || seconds > 3600) { errors[id] = t('metadataProgress.intervalInvalid'); fields[id]?.focus(); return; }
    errors[id] = ''; saving[id] = true;
    try {
        const result = await $backend(`/admin/metadata/enrichment/sources/${id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ interval_seconds: seconds }) });
        if (result.err !== 'ok') throw new Error(result.err);
        drafts[id] = result.interval_seconds; confirmed[id] = result.interval_seconds; dirty[id] = false; saved[id] = true; emit('saved');
    } catch { errors[id] = t('metadataProgress.intervalError'); }
    finally { saving[id] = false; }
}
</script>

<style scoped>
.source-limits { margin-block: 16px; }
.source-limit { display: grid; grid-template-columns: minmax(0, 1fr) minmax(320px, 420px); gap: 8px 20px; padding-block: 14px; border-block-start: 1px solid rgba(var(--v-theme-on-surface), .15); }
.source-limit__description { min-width: 0; overflow-wrap: anywhere; }
.source-limit__description p { margin-block: 4px; }
.source-limit__form { display: flex; align-items: flex-start; gap: 8px; min-width: 0; padding-inline-end: 64px; }
.source-limit__form :deep(.v-input) { min-width: 0; }
.source-limit__status { min-height: 1.4em; }
.source-limit__saved { grid-column: 1 / -1; }
@media (max-width: 760px) { .source-limit { grid-template-columns: minmax(0, 1fr); } .source-limit__form { display: grid; grid-template-columns: minmax(0, 1fr); } .source-limit__form :deep(.v-btn) { justify-self: start; } }
</style>
