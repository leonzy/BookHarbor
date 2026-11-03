<template>
    <section
        v-if="progress"
        class="scan-progress mt-4 pa-3 border rounded"
        :aria-label="t('admin.imports.incremental.progress')"
    >
        <div class="scan-progress-title">
            <strong>{{ t(`admin.imports.incremental.mode.${progress.mode}`) }}</strong>
            <span
                role="status"
                aria-live="polite"
            >{{ t(`admin.imports.incremental.state.${progress.state}`) }}<template v-if="progress.state === 'running'"> · {{ t(`admin.imports.incremental.phase.${progress.phase || 'scanning'}`) }}</template></span>
        </div>
        <p class="scan-path mt-2">
            {{ progress.scope }}
        </p>
        <dl class="scan-counts mt-3">
            <div
                v-for="field in fields"
                :key="field"
            >
                <dt>{{ t(`admin.imports.incremental.count.${field}`) }}</dt>
                <dd :data-testid="`scan-count-${field}`">
                    {{ n(Number(progress[field] || 0)) }}
                </dd>
            </div>
        </dl>
        <p
            v-if="progress.current"
            class="scan-path mt-3"
        >
            {{ progress.current }}
        </p>
        <p
            v-if="progress.error"
            class="mt-2"
            role="alert"
        >
            {{ progress.error }}
        </p>
        <p
            v-if="progress.mode === 'baseline'"
            class="text-body-2 mt-2"
        >
            {{ t('admin.imports.incremental.baselineHint') }}
        </p>
        <p
            v-if="progress.recheck_required"
            class="text-body-2 mt-2"
        >
            {{ t('admin.imports.incremental.eventGap') }}
        </p>
        <p class="text-body-2 mt-2">
            {{ t('admin.imports.incremental.countHint') }}
        </p>
    </section>
</template>

<script setup>
import { useI18n } from 'vue-i18n';
defineProps({ progress: { type: Object, default: null } });
const { t, n } = useI18n();
const fields = ['directories', 'enumerated', 'pending', 'files', 'ready', 'imported', 'skipped', 'failed'];
</script>

<style scoped>
.scan-progress { min-width: 0; }
.scan-progress-title { display: flex; flex-wrap: wrap; gap: 8px 16px; align-items: baseline; }
.scan-path { overflow-wrap: anywhere; }
.scan-counts { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 8rem), 1fr)); gap: 12px; }
.scan-counts dd { margin: 4px 0 0; font-variant-numeric: tabular-nums; font-size: 1.1rem; }
</style>
