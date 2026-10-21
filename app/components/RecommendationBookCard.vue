<template>
    <v-card
        :to="book.href ?? '/book/' + book.id"
        target="_blank"
        rel="noopener"
        :aria-label="book.title"
        :aria-description="$t('pagination.openBookInNewTab', { title: book.title })"
        class="recommendation-book-card ma-1"
        data-testid="recommendation-book-card"
    >
        <div
            class="recommendation-cover"
            :title="book.title"
        >
            <v-img
                :src="book.img"
                :aspect-ratio="11/15"
                alt=""
                aria-hidden="true"
                data-testid="recommendation-cover"
                cover
            />
            <BookReadBadge :read-state="book.state?.read_state" />
        </div>
        <BookCardText :book="book" /><BookFileSizes
            :sizes="book.format_sizes"
            class="px-2 pb-2"
        />
    </v-card>
</template>

<script setup>
import BookFileSizes from '@/components/BookFileSizes.vue';
import BookReadBadge from '@/components/BookReadBadge.vue';
import BookCardText from '@/components/BookCardText.vue';

defineProps({
    book: {
        type: Object,
        required: true,
    },
});
</script>

<style scoped>
.recommendation-book-card:focus-visible {
    outline: 2px solid currentColor;
    outline-offset: 2px;
}
.recommendation-cover {
    position: relative;
}
</style>
