<template>
    <section
        v-if="progress && progress.state !== 'idle'"
        class="size-audit mt-4 pa-3 border rounded"
        :aria-label="t('admin.imports.settings.auditTitle')"
        data-testid="import-size-audit"
    >
        <strong>{{ t('admin.imports.settings.auditTitle') }}</strong>
        <p
            role="status"
            aria-live="polite"
        >
            {{ t(`admin.imports.settings.auditState.${progress.state}`) }} · {{ t('admin.imports.settings.auditProgress', { checked: progress.checked || 0, total: progress.total || 0 }) }}
        </p>
        <p>{{ t('admin.imports.settings.auditRemoved', { formats: progress.removed_formats || 0, books: progress.removed_books || 0 }) }}</p>
        <p v-if="progress.unreadable">
            {{ t('admin.imports.settings.auditUnreadable', { count: progress.unreadable }) }}
        </p>
        <p
            v-if="progress.error"
            role="alert"
        >
            {{ progress.error }}
        </p>
    </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n';
defineProps<{ progress?: { state: string; total?: number; checked?: number; removed_formats?: number; removed_books?: number; unreadable?: number; error?: string } | null }>();
const { t } = useI18n();
</script>

<style scoped>
.size-audit { font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
</style>
