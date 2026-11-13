<template>
    <fieldset class="access-range" :disabled="disabled">
        <legend class="text-subtitle-1">{{ t('accessControl.range.label') }}</legend>
        <p class="text-body-2 mb-3">{{ t('accessControl.range.intersectionHint') }}</p>
        <v-radio-group
            :model-value="modelValue.mode"
            :disabled="disabled"
            :label="t('accessControl.range.mode')"
            @update:model-value="setMode"
        >
            <v-radio :label="t('accessControl.range.unrestricted')" value="unrestricted" />
            <v-radio :label="t('accessControl.range.selected')" value="selected" />
            <v-radio :label="t('accessControl.range.denyAll')" value="deny_all" />
        </v-radio-group>
        <template v-if="modelValue.mode === 'selected'">
            <p class="text-body-2 mb-3">{{ t('accessControl.range.directoryHint') }}</p>
            <ul class="access-range__targets">
                <li v-for="(target, index) in modelValue.targets" :key="targetKey(target)">
                    <div class="access-range__target">
                        <span class="access-range__path">{{ targetLabel(target) }}</span>
                        <v-btn
                            variant="text"
                            :disabled="disabled"
                            :aria-label="t('accessControl.range.removeTarget', { name: targetLabel(target) })"
                            @click="remove(index)"
                        >{{ t('accessControl.range.remove') }}</v-btn>
                    </div>
                    <v-checkbox
                        v-if="!target.class"
                        :model-value="Boolean(target.recursive)"
                        :disabled="disabled"
                        :label="t('accessControl.range.recursive')"
                        hide-details
                        density="compact"
                        @update:model-value="setRecursive(index, Boolean($event))"
                    />
                </li>
            </ul>
            <p v-if="modelValue.targets.length === 0" class="text-body-2 mb-3" role="status">
                {{ t('accessControl.range.empty') }}
            </p>
            <v-btn variant="outlined" :disabled="disabled" @click="emit('choose-directory')">
                {{ t('accessControl.range.addDirectory') }}
            </v-btn>
            <v-select v-if="classes.length" :items="classes" item-title="name" item-value="id"
                      :label="t('accessControl.page.addClassTarget')" :disabled="disabled" :model-value="null"
                      @update:model-value="addClass" />
        </template>
        <v-alert v-if="error" type="error" variant="tonal" class="mt-3" role="alert">
            {{ error }}
        </v-alert>
    </fieldset>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n';
import type { AccessCeiling, AccessMode, AccessTarget } from '@/types/access';

const props = withDefaults(defineProps<{
    modelValue: AccessCeiling;
    disabled?: boolean;
    error?: string;
    targetNames?: Record<string, string>;
    classes?: { id: string; name: string }[];
}>(), { disabled: false, error: '', targetNames: () => ({}), classes: () => [] });
const emit = defineEmits<{
    'update:modelValue': [value: AccessCeiling];
    'choose-directory': [];
}>();
const { t } = useI18n();
function addClass(id: string) {
    if (props.disabled || !props.classes.some(c => c.id === id) || props.modelValue.targets.some(t => t.class === id)) return;
    emit('update:modelValue', { mode: 'selected', targets: [...props.modelValue.targets, { class: id }] });
}

function targetKey(target: AccessTarget) {
    return target.class ? `class:${target.class}` : JSON.stringify([target.root, target.directory]);
}
function targetLabel(target: AccessTarget) {
    return props.targetNames[targetKey(target)] || target.directory || t('accessControl.range.unknownClass');
}
function setMode(mode: unknown) {
    if (props.disabled || !['unrestricted', 'selected', 'deny_all'].includes(String(mode))) return;
    emit('update:modelValue', {
        mode: mode as AccessMode,
        targets: mode === 'selected' ? props.modelValue.targets.map(target => ({ ...target })) : [],
    });
}
function remove(index: number) {
    if (props.disabled) return;
    // Removing the last item retains selected mode. It must never turn a
    // restricted user into an unrestricted user by interpreting [] as public.
    emit('update:modelValue', {
        mode: props.modelValue.mode,
        targets: props.modelValue.targets.filter((_, i) => i !== index).map(target => ({ ...target })),
    });
}
function setRecursive(index: number, recursive: boolean) {
    if (props.disabled) return;
    emit('update:modelValue', {
        mode: props.modelValue.mode,
        targets: props.modelValue.targets.map((target, i) => i === index ? { ...target, recursive } : { ...target }),
    });
}
</script>

<style scoped>
.access-range { border: 0; padding: 0; min-width: 0; }
.access-range__targets { list-style: none; margin: 0 0 1rem; padding: 0; }
.access-range__targets li { padding-block: .75rem; border-bottom: 1px solid rgba(var(--v-border-color), var(--v-border-opacity)); }
.access-range__target { display: flex; align-items: flex-start; gap: .5rem; flex-wrap: wrap; }
.access-range__path { flex: 1 1 12rem; overflow-wrap: anywhere; min-width: 0; padding-block: .5rem; }
.access-range :deep(button:focus-visible) { outline: 2px solid currentColor; outline-offset: 2px; }
</style>
