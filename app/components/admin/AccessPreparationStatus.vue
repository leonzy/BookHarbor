<template>
    <section class="access-preparation" :aria-label="t('accessControl.preparation.label')">
        <h2 class="text-subtitle-1">{{ t(`accessControl.preparation.${revision.status}`) }}</h2>
        <p v-if="revision.status !== 'active'" class="text-body-2 my-2">
            {{ t('accessControl.preparation.notActive') }}
        </p>
        <template v-if="revision.status === 'preparing'">
            <v-progress-linear
                :model-value="percentage"
                :indeterminate="revision.total <= 0"
                :aria-label="t('accessControl.preparation.label')"
            />
            <p class="text-body-2 mt-2" role="status">
                {{ revision.total > 0
                    ? t('accessControl.preparation.progress', { done: revision.processed, total: revision.total })
                    : t('accessControl.preparation.counting') }}
            </p>
        </template>
        <v-alert v-if="revision.message" type="error" variant="tonal" class="my-3" role="alert">
            {{ revision.message }}
        </v-alert>
        <div class="access-preparation__actions">
            <v-btn
                v-if="['draft', 'failed', 'cancelled'].includes(revision.status)"
                :loading="busy"
                :disabled="busy || disabled"
                color="primary"
                @click="emit('prepare')"
            >{{ t('accessControl.preparation.prepare') }}</v-btn>
            <v-btn
                v-if="revision.status === 'ready'"
                :disabled="busy || disabled"
                color="primary"
                @click="emit('review')"
            >{{ t('accessControl.preparation.review') }}</v-btn>
            <v-btn
                v-if="['preparing', 'ready'].includes(revision.status)"
                :disabled="busy || disabled"
                variant="outlined"
                @click="emit('cancel')"
            >{{ t('accessControl.preparation.cancel') }}</v-btn>
            <v-btn :disabled="busy || disabled" variant="text" @click="emit('refresh')">
                {{ t('accessControl.preparation.refresh') }}
            </v-btn>
        </div>
    </section>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { useI18n } from 'vue-i18n';
import type { AccessRevision } from '@/types/access';

const props = withDefaults(defineProps<{ revision: AccessRevision; busy?: boolean; disabled?: boolean }>(), { busy: false, disabled: false });
const emit = defineEmits<{ prepare: []; review: []; cancel: []; refresh: [] }>();
const { t } = useI18n();
const percentage = computed(() => props.revision.total > 0
    ? Math.max(0, Math.min(100, props.revision.processed / props.revision.total * 100)) : 0);
</script>

<style scoped>
.access-preparation { min-width: 0; }
.access-preparation [role="status"] { font-variant-numeric: tabular-nums; }
.access-preparation__actions { display: flex; flex-wrap: wrap; gap: .5rem; margin-top: 1rem; }
.access-preparation :deep(button:focus-visible) { outline: 2px solid currentColor; outline-offset: 2px; }
</style>
