<template>
    <section
        :aria-label="t('admin.imports.statistics.heading')"
        class="import-statistics mt-4"
    >
        <dl class="import-statistics-grid">
            <div
                v-for="field in fields"
                :key="field"
                class="import-statistic pa-3 rounded border"
            >
                <dt class="text-body-2">
                    {{ t(`admin.imports.statistics.${field}`) }}
                </dt>
                <dd
                    class="text-h5 mt-1"
                    :data-testid="`import-stat-${field}`"
                >
                    {{ summary && Number.isFinite(summary[field]) ? n(summary[field]) : '—' }}
                </dd>
            </div>
        </dl>
        <p class="text-body-2 mt-2">
            {{ t('admin.imports.statistics.explanation') }}
        </p>
    </section>
</template>

<script setup>
import { useI18n } from 'vue-i18n';
defineProps({ summary: { type: Object, default: null } });
const { t, n } = useI18n();
const fields = ['library_books', 'available_books', 'done', 'missing_source_books'];
</script>

<style scoped>
.import-statistics-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 11rem), 1fr)); gap: 12px; }
.import-statistic { min-width: 0; }
dd { margin-inline-start: 0; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
</style>
