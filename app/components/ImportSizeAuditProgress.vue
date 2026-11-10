<template>
    <section
        class="size-audit mt-4 pa-3 border rounded"
        :aria-label="t('admin.imports.settings.auditTitle')"
        data-testid="import-size-audit"
    >
        <div class="size-audit-heading">
            <strong>{{ t('admin.imports.settings.auditTitle') }}</strong>
            <v-btn
                variant="outlined"
                :loading="starting"
                :disabled="starting || minimumKib <= 0"
                data-testid="run-size-audit"
                @click="emit('run')"
            >
                {{ t('admin.imports.settings.auditRunNow') }}
            </v-btn>
        </div>
        <p
            v-if="progress && progress.state !== 'idle'"
            role="status"
            aria-live="polite"
        >
            {{ t(progress.state === 'running' && progress.waiting_reason ? `admin.imports.settings.auditWaiting.${progress.waiting_reason}` : `admin.imports.settings.auditState.${progress.state}`) }} · {{ t('admin.imports.settings.auditProgress', { checked: progress.checked || 0, total: progress.total || 0 }) }}
        </p>
        <p v-else>
            {{ t(minimumKib > 0 ? 'admin.imports.settings.auditNotStarted' : 'admin.imports.settings.auditEnableFirst') }}
        </p>
        <p v-if="progress?.state === 'running' && !progress.waiting_reason && progress.immediate_requested">
            {{ t('admin.imports.settings.auditPriority') }}
        </p>
        <p v-if="progress && progress.state !== 'idle'">
            {{ t('admin.imports.settings.auditRemoved', { formats: progress.removed_formats || 0, books: progress.removed_books || 0 }) }}
        </p>
        <p v-if="progress?.unreadable">
            {{ t('admin.imports.settings.auditUnreadable', { count: progress.unreadable }) }}
        </p>
        <p
            v-if="actionError || progress?.error"
            role="alert"
        >
            {{ actionError || progress?.error }}
        </p>
    </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n';
withDefaults(defineProps<{ progress?: { state: string; total?: number; checked?: number; removed_formats?: number; removed_books?: number; unreadable?: number; error?: string; immediate_requested?: boolean; waiting_reason?: string } | null; minimumKib?: number; starting?: boolean; actionError?: string }>(), { minimumKib: 0, starting: false, actionError: '' });
const emit = defineEmits<{ run: [] }>();
const { t } = useI18n();
</script>

<style scoped>
.size-audit-heading { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.size-audit-heading .v-btn:focus-visible { outline: 2px solid currentColor; outline-offset: 2px; }
.size-audit { font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
</style>
