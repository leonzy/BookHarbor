<template>
    <details
        v-if="state"
        class="lecture-recheck"
        data-testid="lecture-recheck"
    >
        <summary>{{ t('lectureRecheck.title') }} · {{ t('lectureRecheck.progress', { checked: state.checked, total: state.total }) }}</summary>
        <p>{{ t(state.discovery_done ? 'lectureRecheck.completeDiscovery' : 'lectureRecheck.discovering') }}</p>
        <dl>
            <div
                v-for="key in keys"
                :key="key"
            >
                <dt>{{ t('lectureRecheck.' + key) }}</dt>
                <dd>{{ Number(state.counts?.[key] || 0).toLocaleString() }}</dd>
            </div>
        </dl>
        <p>{{ t('lectureRecheck.explanation') }}</p>
    </details>
</template>

<script setup>
import { useI18n } from 'vue-i18n';
defineProps({ state: { type: Object, default: null } });
const { t } = useI18n();
const keys = ['pending', 'repaired', 'correct', 'no_evidence', 'protected', 'conflict', 'unreadable', 'deleted'];
</script>

<style scoped>
.lecture-recheck { margin-block: 1rem; }
summary { cursor: pointer; padding-block: .5rem; font-weight: 600; overflow-wrap: anywhere; }
summary:focus-visible { outline: 2px solid currentColor; outline-offset: 2px; }
p { margin-block: .5rem; font-size: .875rem; }
dl { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(9rem, 100%), 1fr)); gap: .75rem; margin-block: .75rem; }
dt { font-size: .875rem; overflow-wrap: anywhere; }
dd { margin: .2rem 0 0; font-size: 1.125rem; }
</style>
