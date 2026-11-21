<template>
    <v-dialog
        :model-value="modelValue"
        max-width="760"
        scrollable
        aria-labelledby="mcp-guide-title"
        @update:model-value="emit('update:modelValue', $event)"
    >
        <v-card class="mcp-guide">
            <div class="d-flex align-start pa-4 pb-2">
                <div>
                    <h2
                        id="mcp-guide-title"
                        class="text-h6"
                    >
                        {{ t('pluginManagement.mcpGuideTitle') }}
                    </h2>
                    <div class="text-caption text-medium-emphasis mt-1">
                        talebook.combo.mcp
                    </div>
                </div>
                <v-spacer />
                <v-btn
                    icon="mdi-close"
                    variant="text"
                    :aria-label="t('common.close')"
                    @click="emit('update:modelValue', false)"
                />
            </div>
            <v-divider />
            <div class="pa-4">
                <v-alert
                    type="info"
                    variant="tonal"
                    density="compact"
                    class="mb-4"
                >
                    {{ t('pluginManagement.mcpGuideIntro') }}
                </v-alert>

                <v-tabs
                    v-model="tab"
                    color="primary"
                    density="compact"
                    class="mb-3"
                >
                    <v-tab value="http">
                        HTTP
                    </v-tab>
                    <v-tab value="stdio">
                        stdio
                    </v-tab>
                </v-tabs>
                <v-window v-model="tab">
                    <v-window-item value="http">
                        <section class="mcp-guide__section">
                            <v-progress-linear v-if="busy" indeterminate />
                            <v-alert v-if="error" type="error" variant="tonal" role="alert">
                                {{ error }}
                            </v-alert>
                            <p v-if="account">
                                {{ t('pluginManagement.mcpAccount', { username: account.username }) }}
                            </p>
                            <v-alert v-if="account && !account.enabled" type="info" variant="tonal">
                                {{ t('pluginManagement.disabledByAdminHint') }}
                            </v-alert>
                            <template v-if="account?.enabled">
                                <v-text-field :model-value="endpoint" label="MCP URL" readonly density="compact" variant="outlined" />
                                <v-text-field
                                    v-if="token"
                                    :model-value="token"
                                    :label="t('pluginManagement.field_access_token')"
                                    readonly
                                    autocomplete="off"
                                    density="compact"
                                    variant="outlined"
                                />
                                <div class="d-flex flex-wrap ga-2 mb-3">
                                    <v-btn v-if="!token" color="primary" :disabled="busy" @click="changeToken('reveal')">
                                        {{ t(account.configured ? 'pluginManagement.mcpShowToken' : 'pluginManagement.mcpGenerateToken') }}
                                    </v-btn>
                                    <v-btn v-if="token" color="primary" :disabled="busy" @click="copy(httpExample)">
                                        {{ t('pluginManagement.mcpCopyConfig') }}
                                    </v-btn>
                                    <v-btn v-if="token" variant="text" :disabled="busy" @click="copy(token)">
                                        {{ t('pluginManagement.mcpCopyToken') }}
                                    </v-btn>
                                    <v-btn v-if="account.configured" variant="text" :disabled="busy" @click="rotate">
                                        {{ t('pluginManagement.mcpRotateToken') }}
                                    </v-btn>
                                </div>
                                <v-switch
                                    v-if="account.configured"
                                    :model-value="account.allow_write"
                                    :disabled="busy"
                                    :label="t('pluginManagement.mcpAllowWrite')"
                                    density="compact"
                                    color="primary"
                                    @update:model-value="setWrite"
                                />
                                <p>{{ t('pluginManagement.mcpPersonalHint') }}</p>
                                <p v-if="copied" role="status">{{ t('pluginManagement.mcpCopied') }}</p>
                                <pre
                                    v-if="token"
                                    class="mcp-guide__code"
                                    data-testid="mcp-http-example"
                                ><code>{{ httpExample }}</code></pre>
                            </template>
                        </section>
                    </v-window-item>
                    <v-window-item value="stdio">
                        <section class="mcp-guide__section">
                            <h3 class="text-subtitle-1 mb-2">
                                {{ t('pluginManagement.mcpGuideStdioTitle') }}
                            </h3>
                            <p>{{ t('pluginManagement.mcpGuideStdioHint') }}</p>
                            <pre
                                class="mcp-guide__code"
                                data-testid="mcp-stdio-example"
                            ><code>{{ stdioExample }}</code></pre>
                        </section>
                    </v-window-item>
                </v-window>

                <v-alert
                    type="warning"
                    variant="tonal"
                    density="compact"
                    class="mt-4"
                >
                    {{ t('pluginManagement.mcpGuideSafety') }}
                </v-alert>
            </div>
        </v-card>
    </v-dialog>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { useNuxtApp } from 'nuxt/app';
import { withBasePath } from '~/utils/base-path';

const props = defineProps<{ modelValue: boolean }>();
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>();
const { t } = useI18n();
const tab = ref('http');
const origin = ref('https://books.example.com');

onMounted(() => {
    origin.value = window.location.origin;
});

const { $backend } = useNuxtApp();
const path = '/plugins/talebook.combo.mcp?personal_token=1';
type PersonalToken = { err: string; msg?: string; username: string; enabled: boolean; configured: boolean; allow_write: boolean; access_token?: string };
const account = ref<PersonalToken | null>(null);
const token = ref('');
const busy = ref(false);
const error = ref('');
const copied = ref(false);
let requestGeneration = 0;

async function load(body?: { action: 'reveal' | 'rotate' | 'settings'; allow_write?: boolean }) {
    const generation = ++requestGeneration;
    busy.value = true;
    error.value = '';
    copied.value = false;
    try {
        const result = await $backend(path, body ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : undefined) as PersonalToken;
        if (generation !== requestGeneration || !props.modelValue) return;
        if (result.err !== 'ok') throw new Error(result.msg || '');
        const { access_token: value, ...publicState } = result;
        account.value = publicState;
        if (value) token.value = value;
    } catch (e) {
        if (generation === requestGeneration) {
            error.value = e instanceof Error && e.message ? e.message : t('pluginManagement.mcpLoadError');
        }
    } finally {
        if (generation === requestGeneration) busy.value = false;
    }
}

watch(() => props.modelValue, (open) => {
    requestGeneration++;
    token.value = '';
    account.value = null;
    error.value = '';
    if (open) {
        tab.value = 'http';
        void load();
    }
}, { immediate: true });

function changeToken(action: 'reveal' | 'rotate') {
    return load({ action });
}
function rotate() {
    if (window.confirm(t('pluginManagement.mcpRotateConfirm'))) void changeToken('rotate');
}
function setWrite(value: boolean | null) {
    return load({ action: 'settings', allow_write: value === true });
}
async function copy(value: string) {
    try {
        if (navigator.clipboard?.writeText) {
            await navigator.clipboard.writeText(value);
        } else {
            const field = document.createElement('textarea');
            field.value = value;
            field.className = 'mcp-guide__clipboard';
            const active = document.activeElement as HTMLElement | null;
            active?.closest('.mcp-guide')?.appendChild(field);
            try {
                field.select();
                if (!document.execCommand('copy')) throw new Error();
            } finally {
                field.remove();
                active?.focus();
            }
        }
        copied.value = true;
    } catch {
        error.value = t('pluginManagement.mcpCopyError');
    }
}

const endpoint = computed(() => `${origin.value}${withBasePath('/mcp')}`);
const httpExample = computed(() => JSON.stringify({
    mcpServers: {
        bookharbor: {
            type: 'http',
            url: endpoint.value,
            headers: { Authorization: `Bearer ${token.value}` },
        },
    },
}, null, 2));

const stdioExample = JSON.stringify({
    mcpServers: {
        bookharbor: {
            command: 'docker',
            args: [
                'exec', '-i', '-u', 'talebook', 'bookharbor',
                'booksvr', 'mcp', '--user', '你的BookHarbor用户名',
            ],
        },
    },
}, null, 2);
</script>

<style scoped>
.mcp-guide__section + .mcp-guide__section { margin-top: 16px; }
.mcp-guide__section p { margin-bottom: 8px; color: rgba(var(--v-theme-on-surface), .72); font-size: 14px; }
.mcp-guide__code { max-width: 100%; max-height: 360px; margin: 10px 0 0; padding: 14px; overflow: auto; border: 1px solid rgba(var(--v-theme-on-surface), .12); border-radius: 8px; background: rgba(var(--v-theme-on-surface), .055); font: 12px/1.55 monospace; white-space: pre; overscroll-behavior: contain; }
.mcp-guide__code code { font: inherit; }
.mcp-guide :deep(.mcp-guide__clipboard) { position: fixed; top: 0; left: 0; opacity: 0; pointer-events: none; }
@media (max-width: 600px) {
    .mcp-guide__code { max-height: 320px; padding: 10px; font-size: 11px; }
}
</style>
