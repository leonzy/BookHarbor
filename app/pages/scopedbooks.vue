<template>
    <div>
        <BookSizeSort />
        <v-row>
            <v-col>
                <v-progress-linear
                    v-if="loading"
                    indeterminate
                    color="primary"
                    class="mb-3"
                />
                <BookCards
                    :books="books"
                    :show-empty-state="inited && !loading && books.length === 0"
                />
            </v-col>

            <v-col cols="12">
                <v-container class="max-width">
                    <AppPagination
                        v-if="page_cnt > 0"
                        v-model="page"
                        :disabled="loading"
                        :length="page_cnt"
                        circle
                        @update:model-value="change_page"
                    />
                </v-container>
                <div class="text-xs-center book-pager" />
            </v-col>
        </v-row>
    </div>
</template>

<script setup>
import BookSizeSort from '@/components/BookSizeSort.vue';
import AppPagination from '@/components/AppPagination.vue';
import BookCards from '~/components/BookCards.vue';
import { useMainStore } from '@/stores/main';
import { useI18n } from 'vue-i18n';

const store = useMainStore();
const { t } = useI18n();
const { $backend, $backend_stream, $alert } = useNuxtApp();
const route = useRoute();
const router = useRouter();

store.setNavbar(true);

const page = ref(1);
const books = ref([]);
const total = ref(0);
const page_size = 60;
const page_cnt = ref(1);
const inited = ref(false);
const loading = ref(false);

watch(total, (newTotal) => {
    page_cnt.value = newTotal > 0 ? Math.max(1, Math.ceil(newTotal / page_size)) : 0;
});

let fetchSequence = 0;
const fetchBooks = async (p = 1) => {
    const seq = ++fetchSequence;
    loading.value = true;
    books.value = [];

    const url = `/scopedbooks?start=${(p - 1) * page_size}&size=${page_size}&stream=1&scope=private&sort=${encodeURIComponent(route.query.sort || '')}`;

    let firstLine = true;
    try {
        for await (const data of $backend_stream(url)) {
            if (seq !== fetchSequence) return;
            if (firstLine) {
                firstLine = false;
                if (data.err === 'exception') {
                    if ($alert) $alert('error', data.msg || t('errors.networkError'));
                    return;
                }
                total.value = data.total || 0;
                page_cnt.value = total.value > 0 ? Math.max(1, Math.ceil(total.value / page_size)) : 0;
                page.value = p;
            } else {
                books.value.push(data);
            }
        }
    } catch (error) {
        console.error('Failed to fetch scoped books:', error);
        if ($alert) $alert('error', t('library.message.fetchBooksFailed'));
    } finally {
        if (seq === fetchSequence) loading.value = false;
    }
};

const init = async () => {
    inited.value = true;

    let p = 1;
    if (route.query.start) {
        p = 1 + parseInt(route.query.start / page_size);
    }

    await fetchBooks(p);
};

const change_page = (newPage) => {
    router.push({ query: { ...route.query, start: String((newPage - 1) * page_size) } });
};

watch(() => route.query, () => {
    if (inited.value) {
        init();
    }
}, { deep: true });

onMounted(() => {
    init();
});

useHead(() => ({
    title: t('navigation.scopedBooks')
}));
</script>

<style scoped>
.book-pager {
  margin-top: 30px;
}
</style>
