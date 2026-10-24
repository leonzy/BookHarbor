<template>
    <div class="book-size-sort">
        <v-select
            :model-value="value"
            :items="items"
            :label="t('bookSort.label')"
            :hint="t('bookSort.hint')"
            persistent-hint
            density="compact"
            variant="outlined"
            data-testid="book-size-sort"
            @update:model-value="change"
        />
    </div>
</template>

<script setup>
import { computed } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { useI18n } from 'vue-i18n';

const props = defineProps({ modelValue: { type: String, default: undefined } });
const emit = defineEmits(['update:modelValue']);
const route = useRoute();
const router = useRouter();
const { t } = useI18n();
const value = computed(() => props.modelValue ?? (['size_asc', 'size_desc'].includes(route.query.sort) ? route.query.sort : ''));
const items = computed(() => [
    { title: t('bookSort.default'), value: '' },
    { title: t('bookSort.ascending'), value: 'size_asc' },
    { title: t('bookSort.descending'), value: 'size_desc' },
]);
function change(sort) {
    emit('update:modelValue', sort);
    if (props.modelValue === undefined) {
        router.push({ query: { ...route.query, sort: sort || undefined, start: undefined, page: undefined } });
    }
}
</script>

<style scoped>
.book-size-sort {
    width: min(100%, 22rem);
    margin-block: 0.75rem 1rem;
}
.book-size-sort :deep(.v-messages) {
    color: rgb(var(--v-theme-on-surface));
    opacity: 0.75;
}
</style>
