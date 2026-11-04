<template>
    <section
        v-if="versions.length"
        class="book-versions"
        :aria-label="t('bookVersions.title')"
        data-testid="book-versions"
    >
        <h2 class="text-h6 mb-2">
            {{ t('bookVersions.title') }}
        </h2>
        <p class="mb-3">
            {{ t('bookVersions.description') }}
        </p>
        <article
            v-for="version in versions"
            :key="version.id"
            class="book-version"
        >
            <h3 class="text-subtitle-1 font-weight-bold">
                <a
                    :href="version.href"
                    target="_blank"
                    rel="noopener noreferrer"
                >{{ version.title }} · #{{ version.id }}</a>
            </h3>
            <p v-if="version.authors?.length">
                {{ version.authors.join(' / ') }}
            </p>
            <ul>
                <li
                    v-for="file in version.files || []"
                    :key="file.href"
                >
                    <a
                        :href="file.href"
                        target="_blank"
                        rel="noopener noreferrer"
                        :aria-label="t('bookVersions.download', { title: version.title, format: file.format })"
                    >{{ file.format }} · {{ formatFileSize(file.size) }}</a>
                    <p v-if="file.storage?.path">
                        {{ t('bookVersions.location') }} <code>{{ file.storage.path }}</code>
                    </p>
                </li>
            </ul>
        </article>
    </section>
</template>

<script setup>
import { useI18n } from 'vue-i18n';
import { formatFileSize } from '~/utils/file-size';
defineProps({ versions: { type: Array, default: () => [] } });
const { t } = useI18n();
</script>

<style scoped>
.book-versions { margin-block: 24px; min-width: 0; }
.book-version { padding-block: 12px; border-top: 1px solid rgba(var(--v-theme-on-surface), .12); overflow-wrap: anywhere; }
.book-version ul { padding-inline-start: 20px; }
.book-version li { margin-block: 8px; }
.book-version code { font-size: .875rem; user-select: text; }
.book-version a { color: inherit; text-decoration: underline; text-underline-offset: 3px; }
.book-version a:focus-visible { outline: 2px solid rgb(var(--v-theme-on-surface)); outline-offset: 2px; }
</style>
