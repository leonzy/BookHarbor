<template>
    <form
        class="page-jump"
        data-testid="page-jump"
        @submit.prevent="jump"
    >
        <v-text-field
            v-model="draft"
            :label="t('pagination.pageNumber')"
            :error-messages="error"
            :disabled="disabled"
            inputmode="numeric"
            autocomplete="off"
            variant="outlined"
            density="compact"
            hide-details="auto"
            class="page-jump__input"
            data-testid="page-jump-input"
            @update:model-value="error = ''"
        />
        <span
            v-if="length !== undefined"
            class="text-body-2"
        >{{ t('pagination.totalPages', { count: length }) }}</span>
        <v-btn
            type="submit"
            :disabled="disabled"
            variant="outlined"
            data-testid="page-jump-submit"
        >
            {{ t('pagination.go') }}
        </v-btn>
    </form>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';

const props = defineProps<{ modelValue: number; length?: number; disabled?: boolean }>();
const emit = defineEmits<{ 'update:modelValue': [page: number] }>();
const { t } = useI18n();
const draft = ref(String(props.modelValue));
const error = ref('');
watch(() => props.modelValue, () => {
    draft.value = String(props.modelValue);
    error.value = '';
});
watch(() => props.length, () => {
    if (error.value) error.value = props.length === undefined ? t('pagination.invalidPage') : t('pagination.invalidRange', { count: props.length });
});
function jump() {
    if (props.disabled) return;
    const value = String(draft.value ?? '').trim();
    const page = Number(value);
    if (!/^\d+$/.test(value) || !Number.isSafeInteger(page) || page < 1 || (props.length !== undefined && page > props.length)) {
        error.value = props.length === undefined
            ? t('pagination.invalidPage')
            : t('pagination.invalidRange', { count: props.length });
        return;
    }
    draft.value = String(page);
    error.value = '';
    if (page !== props.modelValue) emit('update:modelValue', page);
}
</script>

<style scoped>
.page-jump {
    display: inline-flex;
    align-items: flex-start;
    justify-content: center;
    flex-wrap: nowrap;
    flex: 0 0 auto;
    gap: 0.5rem;
    width: max-content;
    max-width: 100%;
    padding-block: 0.5rem;
}
.page-jump__input {
    flex: 0 1 5rem;
    width: 5rem;
    min-width: 0;
}
.page-jump > span {
    display: inline-flex;
    align-items: center;
    min-height: 40px;
    white-space: nowrap;
}
.page-jump > .v-btn {
    flex: 0 0 auto;
    height: 40px;
    min-width: 0;
    padding-inline: 12px;
}
@media (max-width: 599px) {
    .page-jump { gap: 6px; }
    .page-jump__input { flex: 0 0 min(3.75rem, 80px); width: min(3.75rem, 80px); min-width: 60px; }
    .page-jump > span { flex: 0 1 auto; min-width: 0; white-space: normal; font-size: 12px !important; }
    .page-jump > .v-btn { padding-inline: 8px; font-size: 12px; }
}
</style>
