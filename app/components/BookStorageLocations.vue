<template>
    <section
        v-if="locations.length"
        class="storage-locations"
        data-testid="storage-locations"
        :aria-label="t('bookStorage.title')"
    >
        <h2 class="text-h6 mb-2">
            {{ t('bookStorage.title') }}
        </h2>
        <p class="text-caption mb-3">
            {{ t('bookStorage.description', { id: bookId }) }}
        </p>
        <dl>
            <div
                v-for="file in locations"
                :key="file.format"
                class="storage-locations__file"
            >
                <dt class="font-weight-bold mb-1">
                    {{ file.format }}
                </dt>
                <dd>
                    <div>{{ t('bookStorage.directory') }} <code>{{ file.storage.directory }}</code></div>
                    <div>{{ t('bookStorage.filename') }} <code>{{ file.storage.filename }}</code></div>
                    <div v-if="file.storage.archive_member">
                        {{ t('bookStorage.member') }} <code>{{ file.storage.archive_member }}</code>
                    </div>
                </dd>
            </div>
        </dl>
    </section>
</template>
<script setup>
import { computed } from 'vue';
import { useI18n } from 'vue-i18n';
const { t } = useI18n();
const props = defineProps({ files: { type: Array, default: () => [] }, bookId: { type: Number, required: true } });
const locations = computed(() => props.files.filter(file => file.storage?.directory));
</script>
<style scoped>
.storage-locations { margin-block: 24px; }
.storage-locations__file { padding-block: 12px; border-top: 1px solid rgba(var(--v-theme-on-surface), .12); }
.storage-locations dd { margin: 0; }
.storage-locations dd > div { margin-block: 4px; overflow-wrap: anywhere; }
.storage-locations code { font-size: 13px; user-select: text; }
</style>
