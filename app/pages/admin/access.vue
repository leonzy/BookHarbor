<template>
    <v-container class="access-page">
        <h1 class="text-h5 mb-3">
            {{ t('accessControl.page.title') }}
        </h1>
        <p class="mb-4">
            {{ t('accessControl.page.description') }}
        </p>
        <v-alert
            v-if="error"
            type="error"
            variant="tonal"
            class="mb-4"
            role="alert"
        >
            {{ error }}
        </v-alert>
        <v-progress-linear
            v-if="loading"
            indeterminate
            :aria-label="t('accessControl.page.loading')"
        />
        <v-btn v-if="!loading && !loaded" @click="load">{{ t('accessControl.page.retry') }}</v-btn>
        <template v-else-if="loaded">
            <v-alert
                type="info"
                variant="tonal"
                class="mb-4"
            >
                {{ active ? t('accessControl.page.active', { revision: active }) : t('accessControl.page.notConfigured') }}
            </v-alert>
            <AccessPreparationStatus
                v-if="revision"
                :revision="revision"
                :busy="busy"
                :disabled="dirty"
                @prepare="action('prepare')"
                @cancel="action('cancel')"
                @refresh="refreshStatus"
                @review="openReview"
            />
            <v-tabs
                v-model="tab"
                class="mt-4"
                show-arrows
            >
                <v-tab value="users">
                    {{ t('accessControl.page.users') }}
                </v-tab>
                <v-tab value="groups">
                    {{ t('accessControl.page.groups') }}
                </v-tab>
                <v-tab value="rules">
                    {{ t('accessControl.page.rules') }}
                </v-tab>
                <v-tab value="classes">{{ t('accessControl.page.classes') }}</v-tab>
            </v-tabs>
            <v-card
                variant="outlined"
                class="pa-4 my-4"
            >
                <template v-if="tab === 'users'">
                    <v-select
                        v-model="selectedUser"
                        :items="users"
                        item-title="label"
                        item-value="id"
                        :label="t('accessControl.page.user')"
                        :disabled="busy"
                    />
                    <AccessRangeEditor
                        v-if="selectedUser"
                        :model-value="userRange"
                        :disabled="locked"
                        :target-names="targetNames"
                        :classes="policy.classes"
                        :error="attempted && userRange.mode === 'selected' && !userRange.targets.length ? t('accessControl.range.empty') : ''"
                        @update:model-value="setUserRange"
                        @choose-directory="chooseForUser"
                    />
                </template>
                <template v-else-if="tab === 'groups'">
                    <v-btn
                        :disabled="locked"
                        variant="outlined"
                        @click="addGroup"
                    >
                        {{ t('accessControl.page.addGroup') }}
                    </v-btn>
                    <p
                        v-if="!policy.groups.length"
                        class="mt-3"
                    >
                        {{ t('accessControl.page.noGroups') }}
                    </p>
                    <v-card
                        v-for="group in policy.groups"
                        :key="group.id"
                        variant="outlined"
                        class="pa-4 mt-4"
                    >
                        <v-text-field
                            v-model="group.name"
                            :error-messages="attempted && !group.name.trim() ? t('accessControl.page.nameRequired') : []"
                            :label="t('accessControl.page.groupName')"
                            :disabled="locked"
                            @update:model-value="markDirty"
                        />
                        <v-select
                            v-model="group.members"
                            :items="users"
                            item-title="label"
                            item-value="id"
                            multiple
                            chips
                            :label="t('accessControl.page.members')"
                            :disabled="locked"
                            @update:model-value="markDirty"
                        />
                        <AccessRangeEditor
                            :model-value="group.range"
                            :disabled="locked"
                            :target-names="targetNames"
                            :classes="policy.classes"
                            :error="attempted && group.range.mode === 'selected' && !group.range.targets.length ? t('accessControl.range.empty') : ''"
                            @update:model-value="group.range = $event; markDirty()"
                            @choose-directory="chooseForGroup(group.id)"
                        />
                        <v-btn
                            class="mt-3"
                            variant="text"
                            color="error"
                            :disabled="locked || groupInUse(group.id)"
                            @click="removeGroup(group.id)"
                        >
                            {{ t('accessControl.page.removeGroup') }}
                        </v-btn>
                        <p
                            v-if="groupInUse(group.id)"
                            class="text-body-2"
                        >
                            {{ t('accessControl.page.groupInUse') }}
                        </p>
                    </v-card>
                </template>
                <template v-else-if="tab === 'classes'">
                    <p class="mb-3">{{ t('accessControl.page.classHint') }}</p>
                    <v-btn :disabled="locked" variant="outlined" @click="addClass">{{ t('accessControl.page.addClass') }}</v-btn>
                    <v-card v-for="item in policy.classes" :key="item.id" variant="outlined" class="pa-4 mt-4">
                        <v-text-field v-model="item.name" :error-messages="attempted && !item.name.trim() ? t('accessControl.page.nameRequired') : []" :label="t('accessControl.page.className')" :disabled="locked" @update:model-value="markDirty" />
                        <v-btn :disabled="locked || dirty || !revision || revision.status === 'active'" @click="openClass(item.id)">{{ t('accessControl.page.classBooks') }}</v-btn>
                        <v-btn :disabled="locked || classInUse(item.id)" color="error" variant="text" @click="removeClass(item.id)">{{ t('accessControl.page.removeClass') }}</v-btn>
                        <p v-if="classInUse(item.id)">{{ t('accessControl.page.classInUse') }}</p>
                    </v-card>
                </template>
                <template v-else>
                    <p class="mb-3">
                        {{ t('accessControl.page.ruleHint') }}
                    </p>
                    <details v-if="revision?.policy?.origin_rules?.length" class="mb-4">
                        <summary>{{ t('accessControl.page.originTitle') }}</summary>
                        <p>{{ t('accessControl.page.originHint') }}</p>
                        <v-checkbox v-for="origin in revision.policy.origin_rules" :key="origin.id"
                                    v-model="policy.released_origins" :value="origin.id" :disabled="locked"
                                    :label="t('accessControl.page.releaseOrigin', { name: targetLabel(origin.target), id: origin.id })"
                                    @update:model-value="markDirty" />
                    </details>
                    <v-btn
                        :disabled="locked"
                        variant="outlined"
                        @click="chooseRule"
                    >
                        {{ t('accessControl.page.addRule') }}
                    </v-btn>
                    <v-select :items="policy.classes" item-title="name" item-value="id" :model-value="null" :disabled="locked" :label="t('accessControl.page.addClassRule')" @update:model-value="addClassRule" />
                    <v-card
                        v-for="rule in policy.rules"
                        :key="rule.id"
                        variant="outlined"
                        class="pa-4 mt-4"
                    >
                        <h2 class="text-subtitle-1 access-path">
                            {{ targetLabel(rule.target) }}
                        </h2>
                        <v-checkbox
                            v-if="!rule.target.class"
                            v-model="rule.target.recursive"
                            :disabled="locked"
                            :label="t('accessControl.range.recursive')"
                            @update:model-value="markDirty"
                        />
                        <v-switch
                            :model-value="rule.mode === 'deny_all'"
                            :disabled="locked"
                            :label="t('accessControl.range.denyAll')"
                            @update:model-value="rule.mode = $event ? 'deny_all' : 'selected'; markDirty()"
                        />
                        <template v-if="rule.mode === 'selected'">
                            <v-select
                                v-model="rule.users"
                                :items="users"
                                item-title="label"
                                item-value="id"
                                multiple
                                chips
                                :disabled="locked"
                                :label="t('accessControl.page.allowedUsers')"
                                @update:model-value="markDirty"
                            />
                            <v-select
                                v-model="rule.groups"
                                :items="policy.groups"
                                item-title="name"
                                item-value="id"
                                multiple
                                chips
                                :disabled="locked"
                                :label="t('accessControl.page.allowedGroups')"
                                @update:model-value="markDirty"
                            />
                        </template>
                        <v-btn
                            variant="text"
                            color="error"
                            :disabled="locked"
                            @click="removeRule(rule.id)"
                        >
                            {{ t('accessControl.page.removeRule') }}
                        </v-btn>
                    </v-card>
                </template>
            </v-card>
            <div class="access-actions">
                <v-btn v-if="revision?.status === 'active' && !dirty" :disabled="busy" variant="outlined" @click="markDirty(); save()">{{ t('accessControl.page.createDraft') }}</v-btn>
                <v-btn
                    color="primary"
                    :loading="busy"
                    :disabled="locked || !dirty"
                    @click="save"
                >
                    {{ t('accessControl.page.save') }}
                </v-btn>
                <span
                    v-if="dirty"
                    role="status"
                >{{ t('accessControl.page.unsaved') }}</span>
                <span
                    v-if="attempted && !valid"
                    role="alert"
                >{{ t('accessControl.page.invalid') }}</span>
            </div>
        </template>
        <v-dialog :transition="false" v-model="classDialog" max-width="700" :aria-label="t('accessControl.page.classBooks')">
            <v-card class="access-dialog">
                <v-card-title>{{ t('accessControl.page.classBooks') }}</v-card-title>
                <v-card-text>
                    <v-alert v-if="classError" type="error" role="alert">{{ classError }}</v-alert>
                    <p>{{ t('accessControl.page.classDraftHint') }}</p>
                    <v-text-field v-model="classSearch" :label="t('accessControl.page.search')" :hint="t('accessControl.page.searchBooks')" persistent-hint :disabled="classLoading" @keydown.enter="searchClassBooks" />
                    <v-btn :disabled="classLoading || !classSearch.trim()" @click="searchClassBooks">{{ t('accessControl.page.search') }}</v-btn>
                    <v-list v-if="classSearchResults.length">
                        <v-list-item v-for="book in classSearchResults" :key="book.id" :title="`${book.title} · #${book.id}`" :subtitle="book.fmt?.join(' / ')">
                            <template #append><v-btn :disabled="classLoading" @click="assignClass(true, [book.id])">{{ t('accessControl.page.assignClass') }}</v-btn></template>
                        </v-list-item>
                    </v-list>
                    <v-textarea v-model="classIDs" :label="t('accessControl.page.bookIDLabel')" :hint="t('accessControl.page.bookIDs')" persistent-hint rows="2" :error-messages="classIDs.trim() && !parsedClassIDs.length ? t('accessControl.page.invalidIDs') : []" :disabled="classLoading" />
                    <v-btn :disabled="classLoading || !parsedClassIDs.length" @click="assignClass(true)">{{ t('accessControl.page.assignClass') }}</v-btn>
                    <v-progress-linear v-if="classLoading" indeterminate />
                    <v-list>
                        <v-list-item v-for="book in classBooks" :key="book.id" :title="`${book.title} · #${book.id}`">
                            <template #append><v-btn :disabled="classLoading" variant="text" @click="assignClass(false, [book.id])">{{ t('accessControl.range.remove') }}</v-btn></template>
                        </v-list-item>
                    </v-list>
                    <p v-if="!classLoading && !classBooks.length">{{ t('accessControl.page.noClassBooks') }}</p>
                    <div class="access-actions">
                        <v-btn :disabled="classLoading || classStart === 0" @click="classStart -= 50; loadClassBooks()">{{ t('accessControl.page.previous') }}</v-btn>
                        <span>{{ t('accessControl.page.classTotal', { total: classTotal }) }}</span>
                        <v-btn :disabled="classLoading || classStart + 50 >= classTotal" @click="classStart += 50; loadClassBooks()">{{ t('accessControl.page.next') }}</v-btn>
                    </div>
                </v-card-text>
                <v-card-actions><v-btn :disabled="classLoading" @click="classDialog = false">{{ t('accessControl.page.close') }}</v-btn></v-card-actions>
            </v-card>
        </v-dialog>
        <v-dialog :transition="false"
                  v-model="picker"
                  :aria-label="t('accessControl.range.addDirectory')"
                  max-width="650"
                  :persistent="busy"
        >
            <v-card class="access-dialog" :title="t('accessControl.range.addDirectory')">
                <v-card-text>
                    <v-select
                        v-model="pickerRoot"
                        :items="roots"
                        :item-title="(root) => root.ready ? root.path : `${root.path} — ${t('accessControl.page.rootUnavailable')}`"
                        item-value="id"
                        :label="t('accessControl.page.root')"
                        @update:model-value="navigate('/')"
                    />
                    <p class="access-path mb-3">
                        {{ pickerDirectory }}
                    </p>
                    <v-alert
                        v-if="pickerError"
                        type="error"
                        variant="tonal"
                    >
                        {{ pickerError }}
                    </v-alert>
                    <v-progress-linear
                        v-if="pickerLoading"
                        indeterminate
                    />
                    <v-btn
                        v-if="pickerDirectory !== '/'"
                        variant="text"
                        :disabled="pickerLoading"
                        @click="navigate(parentDirectory)"
                    >
                        {{ t('accessControl.page.up') }}
                    </v-btn>
                    <v-list :aria-label="t('accessControl.page.directories')">
                        <v-list-item
                            v-for="directory in directories"
                            :key="directory.directory"
                            :title="directory.name"
                            prepend-icon="mdi-folder-outline"
                            :disabled="pickerLoading"
                            @click="navigate(directory.directory)"
                        />
                    </v-list>
                    <v-btn
                        v-if="moreDirectories"
                        :disabled="pickerLoading"
                        variant="text"
                        @click="loadDirectories(true)"
                    >
                        {{ t('accessControl.page.more') }}
                    </v-btn>
                    <p v-if="!pickerLoading && !directories.length">
                        {{ t('accessControl.page.noChildren') }}
                    </p>
                </v-card-text>
                <v-card-actions>
                    <v-btn @click="picker = false">
                        {{ t('accessControl.page.close') }}
                    </v-btn><v-spacer />
                    <v-btn
                        color="primary"
                        :disabled="!pickerRoot || pickerLoading || !!pickerError"
                        @click="acceptDirectory"
                    >
                        {{ t('accessControl.page.selectDirectory') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>
        <v-dialog :transition="false"
                  v-model="review"
                  :aria-label="t('accessControl.page.review')"
                  max-width="750"
                  :persistent="busy"
        >
            <v-card class="access-dialog" :title="t('accessControl.page.review')">
                <v-card-text>
                    <p>{{ t('accessControl.page.reviewHint') }}</p>
                    <v-select
                        v-model="previewUser"
                        :items="users"
                        item-title="label"
                        item-value="id"
                        :label="t('accessControl.page.user')"
                        @update:model-value="previewStart = 0; loadPreview()"
                    />
                    <v-alert
                        v-if="previewError"
                        type="error"
                        variant="tonal"
                    >
                        {{ previewError }}
                    </v-alert>
                    <v-progress-linear
                        v-if="previewLoading"
                        indeterminate
                    />
                    <p
                        v-if="previewReady"
                        role="status"
                    >
                        {{ t('accessControl.page.previewCount', { count: previewTotal }) }}
                    </p>
                    <v-list>
                        <v-list-item
                            v-for="book in previewBooks"
                            :key="book.id"
                            :title="book.title"
                        />
                    </v-list>
                    <div class="access-actions">
                        <v-btn
                            variant="text"
                            :disabled="previewLoading || previewStart === 0"
                            @click="previewStart -= 50; loadPreview()"
                        >
                            {{ t('accessControl.page.previous') }}
                        </v-btn>
                        <v-btn
                            variant="text"
                            :disabled="previewLoading || previewStart + 50 >= previewTotal"
                            @click="previewStart += 50; loadPreview()"
                        >
                            {{ t('accessControl.page.next') }}
                        </v-btn>
                    </div>
                    <v-checkbox
                        v-model="reviewConfirmed"
                        :disabled="!previewReady"
                        :label="t('accessControl.page.confirm')"
                    />
                </v-card-text>
                <v-card-actions>
                    <v-btn
                        :disabled="busy"
                        @click="review = false"
                    >
                        {{ t('accessControl.page.close') }}
                    </v-btn><v-spacer />
                    <v-btn
                        color="primary"
                        :loading="busy"
                        :disabled="!previewReady || !reviewConfirmed || busy"
                        @click="action('apply')"
                    >
                        {{ t('accessControl.page.apply') }}
                    </v-btn>
                </v-card-actions>
            </v-card>
        </v-dialog>
    </v-container>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, nextTick } from 'vue';
import { useNuxtApp } from 'nuxt/app';
import { useI18n } from 'vue-i18n';
import { onBeforeRouteLeave } from 'vue-router';
import AccessRangeEditor from '@/components/admin/AccessRangeEditor.vue';
import AccessPreparationStatus from '@/components/admin/AccessPreparationStatus.vue';
import type { AccessCeiling, AccessPolicy, AccessRevision, AccessTarget } from '@/types/access';

const { t } = useI18n();
const { $backend } = useNuxtApp();
const loading = ref(true), loaded = ref(false), busy = ref(false), dirty = ref(false), error = ref('');
const attempted = ref(false);
const active = ref(0), revision = ref<AccessRevision | null>(null), tab = ref('users');
const policy = ref<AccessPolicy>({ groups: [], principals: [], rules: [], classes: [] });
const users = ref<{ id: string; label: string }[]>([]), roots = ref<{ id: string; path: string; ready: boolean }[]>([]);
const selectedUser = ref('');
const locked = computed(() => busy.value || ['preparing', 'ready'].includes(revision.value?.status || ''));
const emptyRange = (): AccessCeiling => ({ mode: 'unrestricted', targets: [] });
const userRange = computed(() => policy.value.principals.find(p => p.id === selectedUser.value)?.range || emptyRange());
const targetLabel = (target: AccessTarget) => target.class ? policy.value.classes.find(c => c.id === target.class)?.name || t('accessControl.range.unknownClass') : `${roots.value.find(r => r.id === target.root)?.path || target.root || ''}${target.directory === '/' ? '/' : target.directory || ''}`;
const targetNames = computed(() => Object.fromEntries([...policy.value.principals.flatMap(p => p.range.targets), ...policy.value.groups.flatMap(g => g.range.targets)]
    .map(target => [target.class ? `class:${target.class}` : JSON.stringify([target.root, target.directory]), targetLabel(target)])));
const valid = computed(() => [...policy.value.principals.map(p => p.range), ...policy.value.groups.map(g => g.range)].every(r => r.mode !== 'selected' || r.targets.length > 0)
    && policy.value.groups.every(g => g.name.trim()) && policy.value.classes.every(c => c.name.trim()) && policy.value.rules.every(r => r.mode === 'deny_all' || r.users.length + r.groups.length > 0));
function markDirty() { dirty.value = true; }
function setUserRange(range: AccessCeiling) {
    if (locked.value || !selectedUser.value) return;
    const found = policy.value.principals.find(p => p.id === selectedUser.value);
    if (found) found.range = range; else policy.value.principals.push({ id: selectedUser.value, range });
    markDirty();
}
function localID() { return Array.from(crypto.getRandomValues(new Uint8Array(16)), b => b.toString(16).padStart(2, '0')).join(''); }
function addGroup() { policy.value.groups.push({ id: localID(), name: '', members: [], range: emptyRange() }); markDirty(); }
const groupInUse = (id: string) => policy.value.rules.some(r => r.groups.includes(id));
function removeGroup(id: string) { policy.value.groups = policy.value.groups.filter(g => g.id !== id); markDirty(); }
function removeRule(id: string) { policy.value.rules = policy.value.rules.filter(r => r.id !== id); markDirty(); }
function addClass() { policy.value.classes.push({ id: localID(), name: '' }); markDirty(); }
const classInUse = (id: string) => policy.value.rules.some(r => r.target.class === id) || [...policy.value.principals.map(p => p.range), ...policy.value.groups.map(g => g.range)].some(r => r.targets.some(t => t.class === id));
function removeClass(id: string) { if (locked.value || classInUse(id)) return; policy.value.classes = policy.value.classes.filter(c => c.id !== id); markDirty(); }
function addClassRule(id: string) { if (locked.value || !policy.value.classes.some(c => c.id === id)) return; policy.value.rules.push({ id: localID(), target: { class: id }, mode: 'deny_all', users: [], groups: [] }); markDirty(); }
const classDialog = ref(false), classID = ref(''), classIDs = ref(''), classLoading = ref(false), classError = ref(''), classStart = ref(0), classTotal = ref(0);
const classBooks = ref<{ id: number; title: string }[]>([]);
const classSearch = ref(''), classSearchResults = ref<{ id: number; title: string; fmt?: string[] }[]>([]);
async function searchClassBooks() {
    if (classLoading.value || !classSearch.value.trim()) return;
    classLoading.value = true; classError.value = ''; classSearchResults.value = [];
    try {
        const result = await api('/admin/book/list?' + new URLSearchParams({ search: classSearch.value.trim(), num: '20', page: '1' }));
        classSearchResults.value = result.items;
        if (!result.items.length) classError.value = t('accessControl.page.noSearchBooks');
    } catch (e) { classError.value = message(e); }
    finally { classLoading.value = false; }
}
const parsedClassIDs = computed(() => {
    const values = classIDs.value.trim().split(/[\s,，;；]+/);
    if (values.some(v => !/^[1-9]\d*$/.test(v))) return [];
    const ids = [...new Set(values.map(Number))];
    return ids.length <= 200 && ids.every(Number.isSafeInteger) ? ids : [];
});
function openClass(id: string) { classID.value = id; classIDs.value = ''; classSearch.value = ''; classSearchResults.value = []; classStart.value = 0; classDialog.value = true; loadClassBooks(); }
async function loadClassBooks() {
    if (!revision.value) return;
    classLoading.value = true; classError.value = '';
    try { const result = await api(`/admin/access/revision/${revision.value.id}/class/${classID.value}/books?start=${classStart.value}`); classBooks.value = result.books; classTotal.value = result.total; }
    catch (e) { classError.value = message(e); }
    finally { classLoading.value = false; }
}
async function assignClass(assigned: boolean, books = parsedClassIDs.value) {
    if (classLoading.value || !revision.value || dirty.value || locked.value || !books.length) return;
    classLoading.value = true; classError.value = '';
    try {
        const result = await api(`/admin/access/revision/${revision.value.id}/class/${classID.value}/books`, { method: 'PUT', body: JSON.stringify({ version: revision.value.version, books, assigned }) });
        adopt(result.revision); classIDs.value = ''; classStart.value = 0;
        await loadClassBooks();
    } catch (e) { classError.value = message(e); }
    finally { classLoading.value = false; }
}
async function api(path: string, options?: object) {
    const result = await $backend(path, options);
    if (result.err !== 'ok') throw new Error(result.msg || result.err || t('accessControl.page.requestFailed'));
    return result;
}
function message(e: unknown) { return e instanceof Error ? e.message : t('accessControl.page.requestFailed'); }
function adopt(r: AccessRevision | null) {
    revision.value = r;
    const p = r?.policy;
    policy.value = structuredClone({ groups: p?.groups || [], principals: p?.principals || [], rules: p?.rules || [], classes: p?.classes || [], released_origins: p?.released_origins || [] });
    dirty.value = false;
    attempted.value = false;
}
async function load() {
    loading.value = true; error.value = '';
    try {
        const { access } = await api('/admin/access');
        active.value = access.active; roots.value = access.roots;
        users.value = access.principals.filter((u: { admin?: boolean }) => !u.admin).map((u: { id: string; account_id: number; guest: boolean; username?: string; name?: string }) => ({ id: u.id, label: u.guest ? t('accessControl.page.guest') : (u.name && u.username ? `${u.name} (${u.username})` : u.username || u.name || t('accessControl.page.userNumber', { id: u.account_id })) }));
        selectedUser.value = users.value[0]?.id || ''; adopt(access.revision); loaded.value = true; schedule();
    } catch (e) { error.value = message(e); } finally { loading.value = false; }
}
async function save() {
    if (locked.value || !dirty.value) return;
    attempted.value = true;
    if (!valid.value) {
        error.value = t('accessControl.page.invalid');
        await nextTick();
        document.querySelector<HTMLElement>('.access-page [aria-invalid="true"]')?.focus();
        return;
    }
    busy.value = true; error.value = '';
    try {
        const existing = revision.value && revision.value.status !== 'active' ? revision.value : null;
        const result = await api(`/admin/access/revision${existing ? `/${existing.id}` : ''}`, { method: existing ? 'PUT' : 'POST', body: JSON.stringify({ version: existing?.version || 0, base_revision: active.value, policy: policy.value }) });
        adopt(result.revision);
    } catch (e) { error.value = message(e); } finally { busy.value = false; }
}
let timer: ReturnType<typeof setTimeout> | undefined;
let alive = true;
function schedule() { clearTimeout(timer); if (alive && revision.value?.status === 'preparing') timer = setTimeout(refreshStatus, 3000); }
async function refreshStatus() {
    if (!revision.value || dirty.value || busy.value) { schedule(); return; }
    try { const result = await api(`/admin/access/revision/${revision.value.id}`); if (alive) revision.value = result.revision; }
    catch (e) { error.value = message(e); }
    finally { schedule(); }
}
async function action(kind: 'prepare' | 'cancel' | 'apply') {
    if (!revision.value || busy.value || dirty.value || (kind === 'apply' && (!previewReady.value || !reviewConfirmed.value))) return;
    busy.value = true; error.value = '';
    try {
        const result = await api(`/admin/access/revision/${revision.value.id}/${kind}`, { method: 'POST', body: JSON.stringify({ version: revision.value.version, watermark: kind === 'apply' ? reviewedWatermark : revision.value.watermark || 0 }) });
        adopt(result.revision);
        if (kind === 'apply') { active.value = result.revision.id; review.value = false; }
    } catch (e) { error.value = message(e); previewError.value = review.value ? error.value : ''; previewReady.value = false; }
    finally { busy.value = false; schedule(); }
}
const picker = ref(false), pickerRoot = ref(''), pickerDirectory = ref('/'), pickerError = ref(''), pickerLoading = ref(false), moreDirectories = ref(false);
const directories = ref<{ directory: string; name: string }[]>([]);
const parentDirectory = computed(() => pickerDirectory.value.slice(0, pickerDirectory.value.lastIndexOf('/')) || '/');
let destination: { kind: 'user' | 'group' | 'rule'; id: string } = { kind: 'rule', id: '' };
let directoryRequest = 0;
function showPicker() { pickerRoot.value ||= roots.value[0]?.id || ''; picker.value = true; navigate('/'); }
function chooseForUser() { destination = { kind: 'user', id: selectedUser.value }; showPicker(); }
function chooseForGroup(id: string) { destination = { kind: 'group', id }; showPicker(); }
function chooseRule() { destination = { kind: 'rule', id: '' }; showPicker(); }
function navigate(directory: string) { pickerDirectory.value = directory; directories.value = []; loadDirectories(false); }
async function loadDirectories(append: boolean) {
    const request = ++directoryRequest; pickerError.value = ''; moreDirectories.value = false;
    if (!pickerRoot.value) { pickerError.value = t('accessControl.page.noRoots'); return; }
    pickerLoading.value = true;
    try {
        const result = await api('/admin/access/directory?' + new URLSearchParams({ root: pickerRoot.value, directory: pickerDirectory.value, start: String(append ? directories.value.length : 0) }));
        if (request !== directoryRequest || !alive) return;
        directories.value = append ? [...directories.value, ...result.directories] : result.directories; moreDirectories.value = result.has_more;
    } catch (e) { if (request === directoryRequest) pickerError.value = message(e); }
    finally { if (request === directoryRequest) pickerLoading.value = false; }
}
function acceptDirectory() {
    if (locked.value || pickerLoading.value || pickerError.value || !pickerRoot.value) return;
    const target = { root: pickerRoot.value, directory: pickerDirectory.value, recursive: true };
    if (destination.kind === 'rule') policy.value.rules.push({ id: localID(), target, mode: 'deny_all', users: [], groups: [] });
    else {
        let range: AccessCeiling | undefined;
        if (destination.kind === 'group') range = policy.value.groups.find(g => g.id === destination.id)?.range;
        else { let user = policy.value.principals.find(u => u.id === destination.id); if (!user) { user = { id: destination.id, range: { mode: 'selected', targets: [] } }; policy.value.principals.push(user); } range = user.range; }
        if (range && !range.targets.some(t => t.root === target.root && t.directory === target.directory)) range.targets.push(target);
    }
    picker.value = false; markDirty();
}
const review = ref(false), previewUser = ref(''), previewLoading = ref(false), previewReady = ref(false), previewError = ref(''), reviewConfirmed = ref(false);
const previewBooks = ref<{ id: number; title: string }[]>([]), previewTotal = ref(0), previewStart = ref(0);
let previewRequest = 0, reviewedWatermark = 0;
function openReview() { previewUser.value = selectedUser.value; review.value = true; previewStart.value = 0; loadPreview(); }
async function loadPreview() {
    const request = ++previewRequest; previewReady.value = false; reviewConfirmed.value = false; previewError.value = ''; previewBooks.value = [];
    if (!revision.value || !previewUser.value) return;
    previewLoading.value = true;
    try {
        const state = await api(`/admin/access/revision/${revision.value.id}`);
        const result = await api(`/admin/access/revision/${revision.value.id}/preview?` + new URLSearchParams({ principal: previewUser.value, start: String(previewStart.value) }));
        if (request !== previewRequest || !alive) return;
        reviewedWatermark = state.revision.watermark; previewBooks.value = result.books; previewTotal.value = result.total; previewReady.value = true;
    } catch (e) { if (request === previewRequest) previewError.value = message(e); }
    finally { if (request === previewRequest) previewLoading.value = false; }
}
function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value) { event.preventDefault(); event.returnValue = ''; } }
onBeforeRouteLeave(() => !dirty.value || window.confirm(t('accessControl.page.unsavedLeave')));
onMounted(() => { window.addEventListener('beforeunload', beforeUnload); load(); });
onBeforeUnmount(() => { window.removeEventListener('beforeunload', beforeUnload); alive = false; clearTimeout(timer); directoryRequest++; previewRequest++; });
</script>

<style scoped>
.access-page { max-width: 1050px; }
.access-actions { display: flex; flex-wrap: wrap; gap: .75rem; align-items: center; }
.access-path { overflow-wrap: anywhere; }
.access-page :deep(.v-list-item-title), .access-dialog :deep(.v-list-item-title) { white-space: normal; overflow-wrap: anywhere; }
.access-page :deep(.v-card-text), .access-dialog :deep(.v-card-text) { overscroll-behavior: contain; }
.access-page :deep(.v-alert__content), .access-dialog :deep(.v-alert__content) { color: rgb(var(--v-theme-on-surface)); }
.access-page :deep(.v-btn.bg-primary), .access-dialog :deep(.v-btn.bg-primary) { background-color: #1565c0 !important; color: #fff !important; }
.access-page :deep(button:focus-visible), .access-dialog :deep(button:focus-visible) { outline: 2px solid currentColor; outline-offset: 2px; }
@media (prefers-reduced-motion: reduce) { .access-page :deep(*), .access-dialog :deep(*) { animation: none !important; transition: none !important; } }
</style>
