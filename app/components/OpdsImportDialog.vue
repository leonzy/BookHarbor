<template>
    <!-- OPDS Import Dialog -->
    <v-dialog
        :model-value="dialogVisible"
        width="800"
        max-height="90vh"
        @update:model-value="onDialogUpdate"
    >
        <v-card>
            <v-card-title>
                从其他 OPDS 导入
                <v-spacer />
                <v-chip
                    v-if="opdsImportState === 'browsing' && selectedOpdsBooks.length > 0"
                    color="primary"
                    size="small"
                >
                    已选: {{ selectedOpdsBooks.length }} 本
                </v-chip>
            </v-card-title>
            <v-card-text style="padding: 16px;">

                <!-- OPDS 源列表界面 -->
                <div v-if="opdsImportState === 'source_list'">
                    <div class="d-flex align-center justify-space-between mb-4">
                        <div class="text-h6">已保存的 OPDS 源</div>
                        <v-btn
                            size="small"
                            color="primary"
                            variant="outlined"
                            @click="showAddOpdsSourceDialog"
                        >
                            <v-icon start>mdi-plus</v-icon>
                            添加 OPDS 源
                        </v-btn>
                    </div>

                    <div v-if="opdsSources.length > 0" class="border rounded-lg pa-2">
                        <v-list class="bg-transparent">
                            <v-list-item
                                v-for="source in opdsSources"
                                :key="source.id"
                                class="mb-2 rounded-lg"
                                variant="outlined"
                                style="border-width: 1.5px; transition: all 0.2s; cursor: pointer;"
                                @click="selectOpdsSource(source)"
                            >
                                <template #prepend>
                                    <v-icon
                                        :color="source.active ? 'primary' : 'grey'"
                                        size="large"
                                    >
                                        mdi-server
                                    </v-icon>
                                </template>

                                <v-list-item-title class="font-weight-bold text-body-1 mb-1">
                                    {{ source.name }}
                                    <v-chip
                                        v-if="!source.active"
                                        size="x-small"
                                        color="grey"
                                        class="ml-2"
                                        variant="flat"
                                    >
                                        已停用
                                    </v-chip>
                                </v-list-item-title>

                                <v-list-item-subtitle class="text-body-2">
                                    <div class="text-grey-darken-2 font-weight-medium">{{ source.url }}</div>
                                    <div v-if="source.description" class="text-grey mt-1">{{ source.description }}</div>
                                </v-list-item-subtitle>

                                <template #append>
                                    <v-menu location="end">
                                        <template #activator="{ props }">
                                            <v-btn
                                                size="small"
                                                variant="text"
                                                icon="mdi-dots-vertical"
                                                v-bind="props"
                                                @click.stop
                                            />
                                        </template>
                                        <v-list>
                                            <v-list-item
                                                prepend-icon="mdi-pencil"
                                                title="编辑"
                                                @click.stop="editOpdsSource(source)"
                                            />
                                            <v-list-item
                                                prepend-icon="mdi-delete"
                                                title="删除"
                                                class="text-red"
                                                @click.stop="deleteOpdsSource(source)"
                                            />
                                        </v-list>
                                    </v-menu>
                                </template>
                            </v-list-item>
                        </v-list>
                    </div>

                    <div v-else class="text-body-2 text-grey pa-4 text-center border rounded-lg">
                        暂无已保存的 OPDS 源，请点击上方按钮添加
                    </div>

                    <!-- 手动输入区域 -->
                    <div class="mt-4">
                        <div class="text-subtitle-2 text-grey-darken-1 mb-2">或手动输入地址连接：</div>
                        <v-text-field
                            v-model="opdsUrl"
                            label="OPDS 目录地址"
                            placeholder="例如: http://example.com:8080/opds"
                            variant="outlined"
                            density="compact"
                            hide-details
                            @keyup.enter="connectToOpds"
                        />
                        <div class="text-caption text-grey mt-1">
                            提示：输入完整的 OPDS 地址，包含协议、主机、端口和路径
                        </div>
                    </div>
                </div>

                <!-- 目录浏览界面 -->
                <div v-else-if="opdsImportState === 'browsing'">
                    <!-- 返回按钮 -->
                    <div class="mb-3">
                        <v-btn
                            size="small"
                            variant="outlined"
                            @click="backToSourceList"
                        >
                            <v-icon start>mdi-arrow-left</v-icon>
                            返回源列表
                        </v-btn>
                    </div>

                    <!-- 面包屑导航 -->
                    <div class="mb-3">
                        <div class="d-flex align-center flex-wrap">
                            <v-icon size="small" class="mr-1">mdi-folder</v-icon>
                            <template
                                v-for="(crumb, index) in breadcrumbs"
                                :key="index"
                            >
                                <span
                                    class="text-body-2"
                                    :class="{
                                        'text-primary cursor-pointer': !crumb.disabled,
                                        'text-grey': crumb.disabled
                                    }"
                                    @click.stop="handleBreadcrumbClick(crumb, index)"
                                >
                                    {{ crumb.title }}
                                </span>
                                <v-icon
                                    v-if="index < breadcrumbs.length - 1"
                                    size="small"
                                    class="mx-1"
                                >
                                    mdi-chevron-right
                                </v-icon>
                            </template>
                        </div>
                    </div>

                    <!-- 目录内容区域 -->
                    <div
                        class="border rounded-lg"
                        style="max-height: 400px; min-height: 200px; overflow-y: auto;"
                    >
                        <div
                            class="pa-2"
                            :style="{ minHeight: opdsItems.length === 0 ? '200px' : 'auto' }"
                        >
                            <!-- 加载动画 -->
                            <div
                                v-if="opdsLoading && opdsItems.length === 0"
                                class="text-center py-12"
                            >
                                <v-progress-circular indeterminate color="primary" size="32" />
                                <div class="mt-4 text-body-1 text-grey">正在加载目录...</div>
                            </div>

                            <!-- 返回上级目录 -->
                            <div
                                v-if="navigationHistory.length > 0 && !opdsLoading"
                                class="d-flex align-center pa-3 rounded cursor-pointer mb-1"
                                style="cursor: pointer;"
                                @click="goBackInOpdsPath"
                            >
                                <v-icon class="mr-2" color="primary">mdi-arrow-left</v-icon>
                                <span class="text-body-1">返回上级目录</span>
                            </div>

                            <!-- 项目列表 -->
                            <template v-if="!opdsLoading">
                                <div
                                    v-for="(item, index) in opdsItems"
                                    :key="item.id || index"
                                    class="mb-1"
                                >
                                    <!-- 文件夹 -->
                                    <div
                                        v-if="item.type === 'folder'"
                                        class="d-flex align-center pa-3 rounded cursor-pointer"
                                        @click="navigateToOpdsFolder(item)"
                                    >
                                        <v-icon class="mr-3" color="amber">mdi-folder</v-icon>
                                        <div class="flex-grow-1">
                                            <div class="font-weight-medium text-body-1">{{ item.title }}</div>
                                            <div v-if="item.summary" class="text-caption text-grey">{{ item.summary }}</div>
                                        </div>
                                        <v-icon size="small">mdi-chevron-right</v-icon>
                                    </div>

                                    <!-- 书籍 -->
                                    <div
                                        v-else-if="item.type === 'book'"
                                        class="d-flex align-center pa-3 rounded"
                                    >
                                        <v-checkbox
                                            v-model="selectedOpdsBooks"
                                            :value="item"
                                            color="primary"
                                            hide-details
                                            class="ma-0 pa-0 mr-2 flex-shrink-0"
                                        />
                                        <v-icon class="mr-3 flex-shrink-0" color="blue" size="small">mdi-book</v-icon>
                                        <div class="flex-grow-1 min-w-0">
                                            <div class="font-weight-medium text-body-1 text-truncate">{{ item.title }}</div>
                                            <div v-if="item.author" class="text-caption text-grey">作者: {{ item.author }}</div>
                                            <div v-if="item.summary" class="text-caption text-grey text-truncate">{{ item.summary }}</div>
                                        </div>
                                        <div v-if="item.cover_link" class="ml-3 flex-shrink-0">
                                            <v-avatar size="40" rounded="sm">
                                                <BookCover :src="item.cover_link" :title="item.title" alt="封面" cover />
                                            </v-avatar>
                                        </div>
                                    </div>
                                </div>
                            </template>

                            <!-- 空状态 -->
                            <div
                                v-if="opdsItems.length === 0 && !opdsLoading"
                                class="text-center py-8"
                            >
                                <v-icon size="48" color="grey">mdi-folder-open-outline</v-icon>
                                <div class="mt-2 text-grey">目录为空</div>
                            </div>
                        </div>
                    </div>
                </div>

                <!-- 导入进度界面 -->
                <div v-else-if="opdsImportState === 'importing'">
                    <div class="text-center py-8">
                        <v-progress-circular indeterminate color="primary" size="64" />
                        <div class="mt-4 text-h6">正在导入书籍到待处理列表...</div>
                        <div class="mt-2 text-body-1">
                            已导入 {{ opdsImportProgress.done }} / {{ opdsImportProgress.total }} 本
                        </div>
                        <v-progress-linear
                            v-model="opdsImportProgress.percent"
                            color="primary"
                            height="20"
                            class="mt-4"
                        >
                            <template #default="{ value }">
                                <strong>{{ Math.ceil(value) }}%</strong>
                            </template>
                        </v-progress-linear>
                        <div class="mt-4">
                            <v-alert type="info" variant="tonal" class="text-left">
                                <div class="text-body-2">导入的书籍会自动添加到"待处理"列表</div>
                                <div class="text-body-2 mt-1">导入完成后，请在主界面点击"扫描书籍"按钮来识别这些书籍</div>
                            </v-alert>
                        </div>
                    </div>
                </div>

                <!-- 导入完成界面 -->
                <div v-else-if="opdsImportState === 'completed'">
                    <div class="text-center py-8">
                        <v-icon size="64" color="success">mdi-check-circle</v-icon>
                        <div class="mt-4 text-h6 text-success">导入完成！</div>
                        <div class="mt-2 text-body-1">
                            成功导入 {{ opdsImportResult.done }} 本书籍到待处理列表
                        </div>
                        <div v-if="opdsImportResult.fail > 0" class="mt-2 text-body-1 text-warning">
                            失败 {{ opdsImportResult.fail }} 本
                        </div>
                        <div class="mt-4">
                            <v-alert type="success" variant="tonal" class="text-left">
                                <div class="text-body-2">书籍已成功添加到扫描目录</div>
                                <div class="text-body-2 mt-1">
                                    接下来请：
                                    <ol class="mt-2 pl-4">
                                        <li>关闭此窗口</li>
                                        <li>在主界面上点击"扫描书籍"按钮</li>
                                        <li>在"待处理"列表中查看导入的书籍</li>
                                        <li>选择要导入的书籍并点击"导入"</li>
                                    </ol>
                                </div>
                            </v-alert>
                        </div>
                    </div>
                </div>

                <!-- 错误状态 -->
                <div v-else-if="opdsImportState === 'error'">
                    <div class="text-center py-8">
                        <v-icon size="64" color="error">mdi-alert-circle</v-icon>
                        <div class="mt-4 text-h6 text-error">{{ opdsError }}</div>
                        <div class="mt-4">
                            <v-btn color="primary" @click="resetOpdsImportState">返回</v-btn>
                        </div>
                    </div>
                </div>
            </v-card-text>

            <v-card-actions class="d-flex justify-end gap-2 pa-4">
                <v-btn
                    color="primary"
                    variant="outlined"
                    :disabled="isLeftButtonDisabled"
                    @click="handleLeftButtonClick"
                >
                    {{ leftButtonText }}
                </v-btn>
                <v-tooltip
                    :text="opdsImportState === 'browsing' && selectedOpdsBooks.length === 0 ? '请先勾选要导入的书籍' : ''"
                    :disabled="!(opdsImportState === 'browsing' && selectedOpdsBooks.length === 0)"
                    location="top"
                >
                    <template #activator="{ props: tooltipProps }">
                        <span v-bind="tooltipProps">
                            <v-btn
                                :loading="isRightButtonLoading"
                                :disabled="isRightButtonDisabled"
                                color="primary"
                                @click="handleRightButtonClick"
                            >
                                {{ rightButtonText }}
                                <template v-if="opdsImportState === 'browsing' && selectedOpdsBooks.length > 0">
                                    ({{ selectedOpdsBooks.length }})
                                </template>
                            </v-btn>
                        </span>
                    </template>
                </v-tooltip>
            </v-card-actions>
        </v-card>
    </v-dialog>

    <!-- OPDS 源管理对话框 -->
    <v-dialog v-model="opdsSourceDialogVisible" width="600">
        <v-card>
            <v-card-title>{{ opdsSourceDialogTitle }}</v-card-title>
            <v-card-text>
                <v-form ref="opdsSourceForm">
                    <v-text-field
                        v-model="currentOpdsSource.name"
                        label="源名称"
                        placeholder="例如：我的 Calibre 图书馆"
                        variant="outlined"
                        required
                        class="mb-4"
                    />
                    <v-text-field
                        v-model="currentOpdsSource.url"
                        label="OPDS 目录 URL"
                        placeholder="例如：http://example.com:8080/opds"
                        variant="outlined"
                        required
                        class="mb-4"
                        hint="完整的 OPDS 目录地址，包含协议、主机、端口和路径"
                        persistent-hint
                    />
                    <v-textarea
                        v-model="currentOpdsSource.description"
                        label="描述"
                        placeholder="简短描述此 OPDS 源"
                        variant="outlined"
                        rows="3"
                        class="mb-4"
                    />
                    <v-checkbox
                        v-model="currentOpdsSource.active"
                        label="启用此源"
                        color="primary"
                        hide-details
                    />
                </v-form>
            </v-card-text>
            <v-card-actions>
                <v-spacer />
                <v-btn color="grey" variant="outlined" @click="opdsSourceDialogVisible = false">取消</v-btn>
                <v-btn color="primary" variant="elevated" @click="saveOpdsSource">保存</v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>

    <!-- 确认导入对话框 -->
    <v-dialog v-model="confirmDialogVisible" max-width="420">
        <v-card>
            <v-card-title>确认导入</v-card-title>
            <v-card-text>
                确定要导入选中的 {{ selectedOpdsBooks.length }} 本书籍吗？<br>
                导入的书籍将添加到"待处理"列表，需要手动扫描后才能完成入库。
            </v-card-text>
            <v-card-actions>
                <v-spacer />
                <v-btn variant="outlined" @click="onConfirmCancel">取消</v-btn>
                <v-btn color="primary" @click="onConfirmImport">确认</v-btn>
            </v-card-actions>
        </v-card>
    </v-dialog>
</template>

<script setup>
import BookCover from '@/components/BookCover.vue';
import { ref, computed, onBeforeUnmount } from 'vue';

const props = defineProps({
    dialogVisible: {
        type: Boolean,
        default: false
    }
});

const emit = defineEmits(['update:dialogVisible', 'refresh-data']);

const { $backend, $alert } = useNuxtApp();

// ==================== 状态 ====================
const opdsUrl = ref('');
const opdsImportState = ref('source_list'); // source_list, browsing, importing, completed, error
const currentOpdsPath = ref('');
const originalOpdsPath = ref('');
const opdsItems = ref([]);
const selectedOpdsBooks = ref([]);
const opdsConnection = ref(null);
const opdsLoading = ref(false);
const opdsError = ref('');
const navigationHistory = ref([]);

const opdsImportProgress = ref({ done: 0, total: 0, percent: 0 });
const opdsImportResult = ref({ done: 0, fail: 0 });

// 轮询定时器（提升为组件级，便于清理）
const pollTimer = ref(null);

// OPDS 源管理
const opdsSources = ref([]);
const opdsSourceDialogVisible = ref(false);
const opdsSourceDialogTitle = ref('');
const currentOpdsSource = ref({ id: null, name: '', url: '', description: '', active: true });

// 确认对话框
const confirmDialogVisible = ref(false);
let pendingImportResolve = null;

// ==================== 计算属性 ====================
const leftButtonText = computed(() => {
    if (opdsImportState.value === 'importing') return '正在导入...';
    if (opdsImportState.value === 'completed' || opdsImportState.value === 'error') return '返回';
    return '关闭';
});

const rightButtonText = computed(() => {
    switch (opdsImportState.value) {
        case 'source_list': return '手动连接';
        case 'browsing': return '导入选中书籍';
        case 'importing': return '正在导入...';
        case 'completed': return '完成';
        case 'error': return '重试';
        default: return '操作';
    }
});

const isLeftButtonDisabled = computed(() => opdsImportState.value === 'importing');

const isRightButtonDisabled = computed(() => {
    if (opdsImportState.value === 'source_list') return !opdsUrl.value.trim();
    if (opdsImportState.value === 'browsing') return selectedOpdsBooks.value.length === 0;
    if (opdsImportState.value === 'importing') return true;
    return false;
});

const isRightButtonLoading = computed(() => opdsLoading.value || opdsImportState.value === 'importing');

const breadcrumbs = computed(() => {
    const crumbs = [{ title: '根目录', disabled: navigationHistory.value.length === 0, index: -1 }];
    navigationHistory.value.forEach((item, index) => {
        crumbs.push({
            title: item.title,
            disabled: index === navigationHistory.value.length - 1,
            index
        });
    });
    return crumbs;
});

// ==================== 工具函数 ====================
const getOrigin = () => {
    try {
        return new URL(opdsUrl.value).origin;
    } catch {
        return '';
    }
};

const processItems = (items) => items.map(item => {
    if (!item.id && item.href) {
        let hash = 0;
        for (let i = 0; i < item.href.length; i++) {
            hash = Math.imul(31, hash) + item.href.charCodeAt(i) | 0;
        }
        item.id = Math.abs(hash);
    }
    if (item.href) {
        try {
            item.path = new URL(item.href).pathname;
        } catch {
            item.path = item.href;
        }
    }
    return item;
});

const browsePath = async (path) => {
    const origin = getOrigin();
    if (!origin) {
        $alert('error', '无效的 OPDS 地址');
        return null;
    }
    const targetUrl = path ? origin + (path.startsWith('/') ? path : '/' + path) : origin;
    const response = await $backend('/admin/opds/browse', {
        method: 'POST',
        body: JSON.stringify({ url: targetUrl }),
    });
    return response;
};

// ==================== 对话框生命周期 ====================
const onDialogUpdate = (value) => {
    emit('update:dialogVisible', value);
    if (!value) {
        stopPollTimer();
    }
};

onBeforeUnmount(() => {
    stopPollTimer();
});

const stopPollTimer = () => {
    if (pollTimer.value) {
        clearInterval(pollTimer.value);
        pollTimer.value = null;
    }
};

// ==================== 按钮逻辑 ====================
const handleLeftButtonClick = () => {
    if (opdsImportState.value === 'completed' || opdsImportState.value === 'error') {
        resetOpdsImportState();
    } else {
        emit('update:dialogVisible', false);
        resetOpdsImportState();
    }
};

const handleRightButtonClick = () => {
    switch (opdsImportState.value) {
        case 'source_list': connectToOpds(); break;
        case 'browsing': importSelectedOpdsBooks(); break;
        case 'completed': handleOpdsImportComplete(); break;
        case 'error': resetOpdsImportState(); break;
    }
};

const handleBreadcrumbClick = (crumb) => {
    if (crumb.disabled) return;
    if (crumb.index === -1) {
        navigateToOriginalPath();
    } else {
        navigateToHistoryIndex(crumb.index);
    }
};

// ==================== 连接与导航 ====================
const connectToOpds = async () => {
    const raw = opdsUrl.value.trim();
    if (!raw) {
        $alert('error', '请输入 OPDS 地址');
        return;
    }
    if (!raw.startsWith('http://') && !raw.startsWith('https://')) {
        $alert('error', '地址需以 http:// 或 https:// 开头');
        return;
    }

    let parsedUrl;
    try {
        parsedUrl = new URL(raw);
    } catch {
        $alert('error', '地址格式无效，请检查输入');
        return;
    }

    opdsLoading.value = true;
    opdsError.value = '';

    try {
        originalOpdsPath.value = parsedUrl.pathname;
        navigationHistory.value = [];

        const response = await $backend('/admin/opds/browse', {
            method: 'POST',
            body: JSON.stringify({ url: raw }),
        });

        if (response.err === 'ok') {
            opdsConnection.value = response.current_path;
            currentOpdsPath.value = parsedUrl.pathname;
            opdsItems.value = processItems(response.items);
            opdsImportState.value = 'browsing';
        } else {
            opdsError.value = response.msg || '连接失败，请检查地址是否正确';
            opdsImportState.value = 'error';
            $alert('error', opdsError.value);
        }
    } catch (error) {
        opdsError.value = '连接失败：' + (error.message || '请检查地址是否正确');
        opdsImportState.value = 'error';
        $alert('error', opdsError.value);
    } finally {
        opdsLoading.value = false;
    }
};

const navigateToOpdsFolder = async (folder) => {
    if (!folder.href) {
        $alert('error', '无法导航：缺少链接地址');
        return;
    }

    opdsLoading.value = true;
    try {
        const path = new URL(folder.href).pathname;
        const response = await browsePath(path);

        if (response && response.err === 'ok') {
            currentOpdsPath.value = path;
            navigationHistory.value.push({ title: folder.title, path });
            selectedOpdsBooks.value = [];
            opdsItems.value = processItems(response.items);
        } else {
            $alert('error', response?.msg || '导航失败');
        }
    } catch (error) {
        $alert('error', '导航失败：' + error.message);
    } finally {
        opdsLoading.value = false;
    }
};

const navigateToOriginalPath = async () => {
    opdsLoading.value = true;
    try {
        const response = await $backend('/admin/opds/browse', {
            method: 'POST',
            body: JSON.stringify({ url: opdsUrl.value }),
        });

        if (response.err === 'ok') {
            currentOpdsPath.value = originalOpdsPath.value;
            navigationHistory.value = [];
            selectedOpdsBooks.value = [];
            opdsItems.value = processItems(response.items);
        } else {
            $alert('error', response.msg || '返回根目录失败');
        }
    } catch (error) {
        $alert('error', '返回根目录失败：' + error.message);
    } finally {
        opdsLoading.value = false;
    }
};

const navigateToHistoryIndex = async (targetIndex) => {
    const target = navigationHistory.value[targetIndex];
    if (!target) return;

    opdsLoading.value = true;
    try {
        const response = await browsePath(target.path);

        if (response && response.err === 'ok') {
            currentOpdsPath.value = target.path;
            // 截断历史到目标层级（不包含目标项本身，因为目标项是当前要进入的）
            navigationHistory.value = navigationHistory.value.slice(0, targetIndex);
            selectedOpdsBooks.value = [];
            opdsItems.value = processItems(response.items);
        } else {
            $alert('error', response?.msg || '导航失败');
        }
    } catch (error) {
        $alert('error', '导航失败：' + error.message);
    } finally {
        opdsLoading.value = false;
    }
};

const goBackInOpdsPath = async () => {
    if (navigationHistory.value.length === 0) return;

    const history = navigationHistory.value;
    if (history.length === 1) {
        // 回到根目录
        await navigateToOriginalPath();
        return;
    }

    // 回到上一级（倒数第二个历史项）
    const parent = history[history.length - 2];
    opdsLoading.value = true;
    try {
        const response = await browsePath(parent.path);

        if (response && response.err === 'ok') {
            currentOpdsPath.value = parent.path;
            navigationHistory.value = history.slice(0, -1);
            selectedOpdsBooks.value = [];
            opdsItems.value = processItems(response.items);
        } else {
            $alert('error', response?.msg || '返回上级目录失败');
        }
    } catch (error) {
        $alert('error', '返回上级目录失败：' + error.message);
    } finally {
        opdsLoading.value = false;
    }
};

// ==================== 导入逻辑 ====================
const showConfirmDialog = () => new Promise(resolve => {
    pendingImportResolve = resolve;
    confirmDialogVisible.value = true;
});

const onConfirmImport = () => {
    confirmDialogVisible.value = false;
    if (pendingImportResolve) {
        pendingImportResolve(true);
        pendingImportResolve = null;
    }
};

const onConfirmCancel = () => {
    confirmDialogVisible.value = false;
    if (pendingImportResolve) {
        pendingImportResolve(false);
        pendingImportResolve = null;
    }
};

const importSelectedOpdsBooks = async () => {
    if (selectedOpdsBooks.value.length === 0) {
        $alert('error', '请至少选择一本书籍');
        return;
    }

    const confirmed = await showConfirmDialog();
    if (!confirmed) return;

    opdsLoading.value = true;
    opdsImportState.value = 'importing';
    opdsImportProgress.value = { done: 0, total: selectedOpdsBooks.value.length, percent: 0 };

    const pollImportStatus = async () => {
        try {
            const statusResp = await $backend('/admin/opds/import/status');
            if (statusResp.err === 'ok' && statusResp.status) {
                const { total, done, skip, fail } = statusResp.status;
                opdsImportProgress.value.total = total || selectedOpdsBooks.value.length;
                opdsImportProgress.value.done = done || 0;
                opdsImportProgress.value.percent = total > 0 ? Math.round((done / total) * 100) : 0;

                const completed = total > 0 && (done + skip + fail >= total);
                if (completed) {
                    stopPollTimer();
                    opdsImportResult.value = { done, fail };
                    opdsImportState.value = 'completed';
                    opdsImportProgress.value.percent = 100;
                    emit('refresh-data');
                    opdsLoading.value = false;
                }
            }
        } catch (error) {
            console.warn('轮询导入状态失败:', error);
        }
    };

    try {
        const response = await $backend('/admin/opds/import', {
            method: 'POST',
            body: JSON.stringify({
                opds_url: opdsConnection.value,
                books: selectedOpdsBooks.value.map(book => ({
                    id: book.id,
                    title: book.title,
                    author: book.author,
                    href: book.href
                }))
            }),
        });

        if (response.err === 'ok') {
            pollTimer.value = setInterval(pollImportStatus, 2000);
            setTimeout(pollImportStatus, 500);
            emit('refresh-data');
        } else {
            $alert('error', response.msg || '导入失败');
            opdsImportState.value = 'browsing';
            opdsLoading.value = false;
        }
    } catch (error) {
        $alert('error', '导入失败：' + error.message);
        opdsImportState.value = 'error';
        opdsError.value = '导入失败：' + error.message;
        opdsLoading.value = false;
        stopPollTimer();
    }
};

const handleOpdsImportComplete = () => {
    emit('update:dialogVisible', false);
    $alert('info', 'OPDS书籍已添加到扫描目录，请点击"扫描书籍"按钮进行识别。');
    resetOpdsImportState();
    emit('refresh-data');
};

const resetOpdsImportState = () => {
    stopPollTimer();
    opdsImportState.value = 'source_list';
    currentOpdsPath.value = '';
    originalOpdsPath.value = '';
    opdsItems.value = [];
    selectedOpdsBooks.value = [];
    opdsConnection.value = null;
    opdsUrl.value = '';
    opdsError.value = '';
    opdsLoading.value = false;
    navigationHistory.value = [];
    opdsImportProgress.value = { done: 0, total: 0, percent: 0 };
    opdsImportResult.value = { done: 0, fail: 0 };
};

const backToSourceList = () => {
    stopPollTimer();
    opdsImportState.value = 'source_list';
    opdsItems.value = [];
    selectedOpdsBooks.value = [];
    navigationHistory.value = [];
    currentOpdsPath.value = '';
};

// ==================== OPDS 源管理 ====================
const loadOpdsSources = async () => {
    try {
        const response = await $backend('/admin/opds/sources');
        if (response.err === 'ok') {
            opdsSources.value = response.items || [];
        }
    } catch (error) {
        console.error('加载 OPDS 源列表失败:', error);
    }
};

const showAddOpdsSourceDialog = () => {
    opdsSourceDialogTitle.value = '添加 OPDS 源';
    currentOpdsSource.value = { id: null, name: '', url: '', description: '', active: true };
    opdsSourceDialogVisible.value = true;
};

const editOpdsSource = (source) => {
    opdsSourceDialogTitle.value = '编辑 OPDS 源';
    currentOpdsSource.value = {
        id: source.id,
        name: source.name,
        url: source.url,
        description: source.description || '',
        active: source.active
    };
    opdsSourceDialogVisible.value = true;
};

const saveOpdsSource = async () => {
    if (!currentOpdsSource.value.name || !currentOpdsSource.value.url) {
        $alert('error', '名称和 OPDS URL 为必填项');
        return;
    }

    const data = {
        name: currentOpdsSource.value.name,
        url: currentOpdsSource.value.url,
        description: currentOpdsSource.value.description,
        active: currentOpdsSource.value.active
    };

    try {
        let response;
        if (currentOpdsSource.value.id) {
            data.id = currentOpdsSource.value.id;
            response = await $backend('/admin/opds/sources', { method: 'PUT', body: JSON.stringify(data) });
        } else {
            response = await $backend('/admin/opds/sources', { method: 'POST', body: JSON.stringify(data) });
        }

        if (response.err === 'ok') {
            $alert('success', response.msg);
            opdsSourceDialogVisible.value = false;
            await loadOpdsSources();
        } else {
            $alert('error', response.msg);
        }
    } catch (error) {
        $alert('error', '保存 OPDS 源失败');
    }
};

const deleteOpdsSource = async (source) => {
    if (!confirm('确定要删除此 OPDS 源配置吗？')) return;

    try {
        const response = await $backend('/admin/opds/sources', {
            method: 'DELETE',
            body: JSON.stringify({ id: source.id })
        });

        if (response.err === 'ok') {
            $alert('success', response.msg);
            await loadOpdsSources();
        } else {
            $alert('error', response.msg);
        }
    } catch (error) {
        $alert('error', '删除 OPDS 源失败');
    }
};

const selectOpdsSource = async (source) => {
    try {
        opdsUrl.value = source.url;
        await connectToOpds();
    } catch (error) {
        $alert('error', `无法连接到 OPDS 源: ${source.url}`);
    }
};

// 初始化时加载源列表
loadOpdsSources();
</script>

<style scoped>
.cursor-pointer {
    cursor: pointer;
}
.min-w-0 {
    min-width: 0;
}
.border::-webkit-scrollbar {
    width: 8px;
}
.border::-webkit-scrollbar-track {
    background: #f1f1f1;
    border-radius: 4px;
}
.border::-webkit-scrollbar-thumb {
    background: #888;
    border-radius: 4px;
}
.border::-webkit-scrollbar-thumb:hover {
    background: #555;
}
</style>
