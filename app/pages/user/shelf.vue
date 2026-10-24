<template>
    <div>
        <BookSizeSort />
        <BookCoverGrid
            :books="books"
            :empty-text="t('shelf.empty')"
            key-prefix="shelf"
            sm="3"
            md="2"
            show-title
        />
    </div>
</template>

<script setup>
import BookSizeSort from '@/components/BookSizeSort.vue';
import { ref, onMounted, watch } from 'vue';
import BookCoverGrid from '@/components/BookCoverGrid.vue';
import { useMainStore } from '@/stores/main';
import { useI18n } from 'vue-i18n';

const { $backend } = useNuxtApp();
const mainStore = useMainStore();
const { t } = useI18n();

const books = ref([]);
const route = useRoute();
let sequence = 0;
async function loadBooks() {
    const current = ++sequence;
    const query = new URLSearchParams({ sort: route.query.sort || '' });
    const rsp = await $backend('/shelf?' + query);
    if (current === sequence && rsp.err === 'ok') { books.value = rsp.books || []; }
}
watch(() => route.fullPath, loadBooks);

useHead({
    title: () => t('shelf.pageTitle'),
});

onMounted(() => {
    mainStore.setNavbar(true);
    loadBooks();
});
</script>
