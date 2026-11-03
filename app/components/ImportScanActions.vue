<template>
    <div class="scan-actions">
        <v-text-field
            :model-value="target"
            :placeholder="pending ? progress.scope : ''"
            :label="t('admin.imports.incremental.target')"
            :hint="t(pending ? 'admin.imports.incremental.resumeTargetHint' : 'admin.imports.incremental.targetHint')"
            persistent-hint
            density="compact"
            class="scan-target"
            :disabled="disabled"
            @update:model-value="target = $event"
        />
        <div class="scan-buttons">
            <v-btn
                color="primary"
                variant="elevated"
                :disabled="disabled"
                @click="emit('run', { path: target.trim() || (pending ? progress.scope : ''), mode: 'incremental', resumeImport: willResume && progress.auto_import })"
            >
                {{ t(willResume ? 'admin.imports.incremental.resume' : 'admin.imports.incremental.scan') }}
            </v-btn>
            <v-dialog
                v-model="confirmFull"
                max-width="520"
                class="scan-confirm"
                :aria-label="t('admin.imports.incremental.fullTitle')"
                :transition="false"
            >
                <template #activator="{ props: activatorProps }">
                    <v-btn
                        v-bind="activatorProps"
                        variant="outlined"
                        :disabled="disabled || willResume"
                    >
                        {{ t('admin.imports.incremental.full') }}
                    </v-btn>
                </template>
                <v-card :title="t('admin.imports.incremental.fullTitle')">
                    <v-card-text>
                        <p>{{ t('admin.imports.incremental.fullHint') }}</p>
                        <p class="scan-scope mt-3">
                            {{ target.trim() || t('admin.imports.incremental.allDirectories') }}
                        </p>
                    </v-card-text>
                    <v-card-actions>
                        <v-spacer />
                        <v-btn @click="confirmFull = false">
                            {{ t('admin.imports.incremental.cancel') }}
                        </v-btn>
                        <v-btn
                            color="primary"
                            @click="runFull"
                        >
                            {{ t('admin.imports.incremental.startFull') }}
                        </v-btn>
                    </v-card-actions>
                </v-card>
            </v-dialog>
        </div>
    </div>
</template>

<script setup>
import { computed, ref } from 'vue';
import { useI18n } from 'vue-i18n';
const props = defineProps({ disabled: Boolean, progress: { type: Object, default: null } });
const emit = defineEmits(['run']);
const { t } = useI18n();
const target = ref('');
const confirmFull = ref(false);
const pending = computed(() => Boolean(props.progress && props.progress.state !== 'done'));
const willResume = computed(() => pending.value && (!target.value.trim() || target.value.trim() === props.progress.scope));
const runFull = () => {
    confirmFull.value = false;
    emit('run', { path: target.value.trim(), mode: 'full' });
};
</script>

<style scoped>
.scan-actions { display: flex; align-items: flex-start; flex-wrap: wrap; gap: 12px; width: 100%; min-width: 0; }
.scan-target { flex: 1 1 28rem; min-width: min(100%, 16rem); }
.scan-buttons { display: flex; flex-wrap: wrap; gap: 8px; }
.scan-scope { overflow-wrap: anywhere; }
.scan-target :deep(.v-label),
.scan-target :deep(.v-messages) { opacity: 1; }
.scan-actions :deep(.v-btn:focus-visible),
.scan-confirm :deep(.v-btn:focus-visible) { outline: 2px solid rgb(var(--v-theme-on-surface)); outline-offset: 2px; }
</style>
