<template>
    <div>
        <v-tabs
            v-model="activeTab"
            class="mb-4"
        >
            <v-tab :value="0">
                {{ t('user.readingRecord.currentlyReading') }} [{{ readingCount }}]
            </v-tab>
            <v-tab :value="1">
                {{ t('user.readingRecord.finishedReading') }} [{{ finishedCount }}]
            </v-tab>
            <v-tab :value="2">
                {{ t('user.readingRecord.readingRecord') }}
            </v-tab>
        </v-tabs>

        <BookSizeSort />
        <v-tabs-window v-model="activeTab">
            <v-tabs-window-item :value="0">
                <BookCoverGrid
                    :books="readingBooks"
                    :empty-text="t('user.readingRecord.noCurrentlyReading')"
                    key-prefix="reading"
                    adaptive
                    show-title
                    show-summary
                />
            </v-tabs-window-item>

            <v-tabs-window-item :value="1">
                <BookCoverGrid
                    :books="finishedBooks"
                    :empty-text="t('user.readingRecord.noFinishedReading')"
                    key-prefix="finished"
                    adaptive
                    show-title
                    show-summary
                />
            </v-tabs-window-item>

            <v-tabs-window-item :value="2">
                <div
                    v-if="history.length === 0"
                    class="text-center py-8 text-grey"
                >
                    {{ t('user.history.noHistory') }}
                </div>
                <template v-else>
                    <div
                        v-for="item in history"
                        :key="item.name"
                    >
                        <v-row>
                            <v-col cols="12">
                                <legend>{{ item.name }}</legend>
                                <v-divider class="mb-2" />
                            </v-col>
                        </v-row>
                        <BookCoverGrid
                            :books="item.books"
                            :empty-text="t('user.history.noRecords')"
                            :key-prefix="'history-' + item.name"
                            adaptive
                            show-title
                            show-summary
                        />
                    </div>
                </template>
            </v-tabs-window-item>
        </v-tabs-window>
    </div>
</template>

<script setup>
import BookSizeSort from '@/components/BookSizeSort.vue';
import { ref, computed, onMounted, watch } from 'vue';
import BookCoverGrid from '@/components/BookCoverGrid.vue';
import { useMainStore } from '@/stores/main';
import { useI18n } from 'vue-i18n';

const { $backend } = useNuxtApp();
const mainStore = useMainStore();
const route = useRoute();
const { t } = useI18n();

const activeTab = ref(route.query.tab === 'finished' ? 1 : 0);
const user = ref({});
const readingBooks = ref([]);
const finishedBooks = ref([]);

const readingCount = computed(() => readingBooks.value.length);
const finishedCount = computed(() => finishedBooks.value.length);

watch(() => route.query.tab, (tab) => {
    activeTab.value = tab === 'finished' ? 1 : 0;
});

useHead({
    title: t('user.readingRecord.pageTitle'),
});

const get_history = (his) => {
    if (!his) { return []; }
    const ordered = [...his];
    if (['size_asc', 'size_desc'].includes(route.query.sort)) ordered.sort((a, b) => {
        const size = book => Math.max(0, ...Object.values(book.format_sizes || {}).map(Number));
        const x = size(a), y = size(b);
        return (x > 0) !== (y > 0) ? (x > 0 ? -1 : 1) : (x !== y ? (route.query.sort === 'size_asc' ? x - y : y - x) : b.id - a.id);
    });
    return ordered.map(b => {
        b.href = '/book/' + b.id;
        return b;
    });
};

const history = computed(() => {
    if (user.value.extra === undefined) { return []; }
    return [
        { name: t('user.history.onlineReading'), books: get_history(user.value.extra.read_history) },
        { name: t('user.history.pushedBooks'), books: get_history(user.value.extra.push_history) },
        { name: t('user.history.browseHistory'), books: get_history(user.value.extra.visit_history) },
    ];
});

let readingSequence = 0;
let finishedSequence = 0;
const loadReadingBooks = async () => {
    const sequence = ++readingSequence;
    try {
        const rsp = await $backend('/reading?sort=' + encodeURIComponent(route.query.sort || ''));
        if (sequence === readingSequence && rsp.err === 'ok') {
            readingBooks.value = rsp.books || [];
        }
    } catch (e) {
        console.error('Failed to load reading books:', e);
    }
};

const loadFinishedBooks = async () => {
    const sequence = ++finishedSequence;
    try {
        const rsp = await $backend('/read-done?sort=' + encodeURIComponent(route.query.sort || ''));
        if (sequence === finishedSequence && rsp.err === 'ok') {
            finishedBooks.value = rsp.books || [];
        }
    } catch (e) {
        console.error('Failed to load finished books:', e);
    }
};

watch(() => route.query.sort, () => { loadReadingBooks(); loadFinishedBooks(); });

onMounted(() => {
    mainStore.setNavbar(true);
    loadReadingBooks();
    loadFinishedBooks();
    $backend('/user/info?detail=1')
        .then(rsp => {
            user.value = rsp.user;
        });
});
</script>
