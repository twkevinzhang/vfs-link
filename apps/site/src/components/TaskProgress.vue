<script
  setup
  lang="ts"
  generic="TTask extends BaseTask, TStatus extends string"
>
/* eslint-disable */
// @ts-nocheck
/**
 * 通用的任務進度組件
 * 支援 Upload 和 Download 兩種任務類型
 */

// 基礎任務介面（Upload 和 Download Task 都需要實作）
interface BaseTask {
  id: string;
  status: string;
  file: {
    size: number;
  };
  error?: string;
  speed?: number;
  eta?: number;
}

interface TaskProgressConfig<TStatus extends string> {
  // Header 配置
  icon: string;
  iconColor: string;
  title: string;

  // 狀態映射
  statusMap: {
    getIcon: (status: TStatus) => string;
    getColor: (status: TStatus) => string;
    getLabel: (status: TStatus) => string;
  };

  // 任務特定邏輯
  // @ts-expect-error
  getFileName: (task: TTask) => string;
  // @ts-expect-error
  getProgress: (task: TTask) => number;
  // @ts-expect-error
  getUploadedOrDownloadedBytes: (task: TTask) => number;

  // 狀態判斷
  isCompleted: (status: TStatus) => boolean;
  isCancelled: (status: TStatus) => boolean;
  isActive: (status: TStatus) => boolean;
  isPaused: (status: TStatus) => boolean;
  isFailed: (status: TStatus) => boolean;
  canRetry: (status: TStatus) => boolean;
  canPause: (status: TStatus) => boolean;
  canCancel: (status: TStatus) => boolean;
}

const props = defineProps<{
  // 任務資料
  // @ts-expect-error
  tasks: TTask[];
  // @ts-expect-error
  activeTasks: TTask[];
  // @ts-expect-error
  completedTasks: TTask[];
  // @ts-expect-error
  failedTasks?: TTask[];

  // UI 狀態
  totalProgress: number;
  isCollapsed: boolean;
  isMobile: boolean;

  // 配置
  // @ts-expect-error
  config: TaskProgressConfig<TStatus>;

  // 可選功能
  showClearCompleted?: boolean;
  showRetryAll?: boolean;
  showPriorityControls?: boolean;
}>();

// 計算已結束任務的總大小（包含 COMPLETED 和 CANCELLED）
const terminatedTasksSize = computed(() => {
  return props.tasks
    .filter(
      (task) =>
        props.config.isCompleted(task.status) ||
        props.config.isCancelled(task.status)
    )
    .reduce((sum, task) => sum + task.file.size, 0);
});

// 計算已結束任務的數量
const terminatedTasksCount = computed(() => {
  return props.tasks.filter(
    (task) =>
      props.config.isCompleted(task.status) ||
      props.config.isCancelled(task.status)
  ).length;
});

const emit = defineEmits<{
  (e: 'pause', taskId: string): void;
  (e: 'resume', taskId: string): void;
  (e: 'retry', taskId: string): void;
  (e: 'remove', taskId: string): void;
  (e: 'cancel', taskId: string): void;
  (e: 'clearCompleted'): void;
  (e: 'retryAll'): void;
  (e: 'toggleCollapse'): void;
  (e: 'increasePriority', taskId: string): void;
  (e: 'decreasePriority', taskId: string): void;
}>();

/**
 * 工具函數
 */
function formatFileSize(bytes: number): string {
  const numBytes = typeof bytes === 'number' ? bytes : Number(bytes) || 0;
  if (numBytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  let size = numBytes;
  let i = 0;
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024;
    i++;
  }
  return `${size.toFixed(1)} ${units[i]}`;
}

function formatSpeed(bytesPerSecond: number): string {
  return `${formatFileSize(bytesPerSecond)}/s`;
}

function formatTime(seconds: number): string {
  if (seconds < 60) {
    return `${Math.round(seconds)}秒`;
  } else if (seconds < 3600) {
    return `${Math.round(seconds / 60)}分鐘`;
  } else {
    return `${Math.round(seconds / 3600)}小時`;
  }
}

function isTaskFailed(task: BaseTask): boolean {
  return ['failed', 'expired', 'verification_failed'].includes(task.status);
}
</script>

<template>
  <!-- Header -->
  <div
    :class="[
      'flex items-center justify-between border-b border-surface-200 dark:border-surface-700 cursor-pointer hover:bg-surface-50 dark:hover:bg-surface-800',
      isMobile ? 'p-3' : 'p-4',
    ]"
    @click="emit('toggleCollapse')"
  >
    <div
      :class="[
        'flex items-center min-w-0 flex-1',
        isMobile ? 'gap-2' : 'gap-3',
      ]"
    >
      <i
        :class="[
          'pi',
          config.icon,
          config.iconColor,
          'flex-shrink-0',
          isMobile ? 'text-lg' : 'text-xl',
        ]"
      />
      <div class="min-w-0 flex-1">
        <h3
          :class="[
            'font-semibold text-surface-900 dark:text-surface-0',
            isMobile ? 'text-sm' : 'text-base',
          ]"
        >
          {{ config.title }}
        </h3>
        <p class="text-xs text-surface-500 truncate">
          <template v-if="activeTasks.length > 0">
            {{ activeTasks.length }} 個進行中
            <span v-if="completedTasks.length > 0">
              · {{ completedTasks.length }} 個已完成
            </span>
          </template>
          <template v-else-if="completedTasks.length > 0">
            {{ completedTasks.length }} 個已完成
          </template>
          <template v-else> 目前沒有{{ config.title }} </template>
        </p>
      </div>
    </div>

    <div
      :class="['flex items-center flex-shrink-0', isMobile ? 'gap-1' : 'gap-2']"
    >
      <!-- Overall Progress -->
      <div
        :class="[
          'font-medium',
          config.iconColor.replace('text-', 'text-').replace('-500', '-600') +
            ' dark:' +
            config.iconColor.replace('-500', '-400'),
          isMobile ? 'text-xs' : 'text-sm',
        ]"
      >
        {{ Math.round(totalProgress) }}%
      </div>

      <!-- Retry All Button -->
      <button
        v-if="
          !isMobile && showRetryAll && failedTasks && failedTasks.length > 0
        "
        class="p-1.5 hover:bg-surface-100 dark:hover:bg-surface-800 rounded transition-colors"
        title="全部重試"
        @click.stop="emit('retryAll')"
      >
        <i
          class="pi pi-refresh text-sm text-surface-600 dark:text-surface-400"
        />
      </button>

      <!-- Clear Completed Button -->
      <button
        v-if="!isMobile && showClearCompleted && terminatedTasksCount > 0"
        class="p-1.5 hover:bg-surface-100 dark:hover:bg-surface-800 rounded transition-colors"
        :title="`清除已結束的任務 (${terminatedTasksCount} 個，共 ${formatFileSize(
          terminatedTasksSize
        )})`"
        @click.stop="emit('clearCompleted')"
      >
        <SvgIcon
          name="broom"
          class="w-4 h-4 text-surface-600 dark:text-surface-400"
        />
      </button>

      <!-- Collapse/Expand Icon -->
      <i
        :class="[
          'pi transition-transform',
          isMobile ? 'text-base' : 'text-lg',
          isCollapsed ? 'pi-chevron-up' : 'pi-chevron-down',
        ]"
      />
    </div>
  </div>

  <!-- Task List -->
  <div
    v-if="!isCollapsed"
    :class="['overflow-y-auto', isMobile ? 'max-h-[70vh]' : 'max-h-96']"
  >
    <div
      v-for="(task, index) in tasks"
      :key="task.id"
      :class="[
        'border-b border-surface-200 dark:border-surface-700 last:border-b-0 hover:bg-surface-50 dark:hover:bg-surface-800/50 transition-colors',
        isMobile ? 'px-3 py-2' : 'px-4 py-2.5',
      ]"
    >
      <div :class="['flex items-center', isMobile ? 'gap-2' : 'gap-3']">
        <!-- Status Icon -->
        <i
          :class="[
            config.statusMap.getIcon(task.status as TStatus),
            config.statusMap.getColor(task.status as TStatus),
            'flex-shrink-0',
            isMobile ? 'text-base' : 'text-lg',
          ]"
        />

        <!-- File Info & Progress -->
        <div class="flex-1 min-w-0 flex items-center gap-2">
          <!-- File Name & Size -->
          <div class="min-w-0 flex-shrink">
            <p
              :class="[
                'font-medium text-surface-900 dark:text-surface-0 truncate leading-tight',
                isMobile ? 'text-xs' : 'text-sm',
              ]"
            >
              {{ task.file.name }}
            </p>
            <div
              class="flex items-center gap-2 text-xs text-surface-500 mt-0.5"
            >
              <span :class="config.statusMap.getColor(task.status as TStatus)">
                {{ config.statusMap.getLabel(task.status as TStatus) }}
              </span>
              <span>{{ formatFileSize(task.file.size) }}</span>
              <!-- Speed & ETA (only for active tasks on desktop) -->
              <template
                v-if="!isMobile && config.isActive(task.status as TStatus)"
              >
                <span v-if="task.speed"> · {{ formatSpeed(task.speed) }} </span>
                <span v-if="task.eta && task.eta > 0">
                  · {{ formatTime(task.eta) }}
                </span>
              </template>
              <!-- Error Message -->
              <span
                v-if="isTaskFailed(task)"
                class="text-red-500 truncate"
                :title="task.error"
              >
                · {{ task.error }}
              </span>
            </div>
          </div>

          <!-- Progress Bar (inline, flexible width on desktop) -->
          <div
            v-if="
              !isMobile &&
              !config.isCompleted(task.status as TStatus) &&
              !config.isCancelled(task.status as TStatus)
            "
            class="flex-1"
          >
            <ProgressBar
              :value="config.getProgress(task)"
              :show-value="false"
              class="h-1.5"
            />
          </div>

          <!-- Progress Percentage -->
          <div
            v-if="
              !config.isCompleted(task.status as TStatus) &&
              !config.isCancelled(task.status as TStatus)
            "
            class="text-xs font-medium text-surface-600 dark:text-surface-400 flex-shrink-0 w-10 text-right"
          >
            {{ config.getProgress(task) }}%
          </div>
        </div>

        <!-- Actions -->
        <div class="flex items-center gap-0.5 flex-shrink-0">
          <!-- Priority Controls (only for pending tasks, hidden on mobile) -->
          <template v-if="!isMobile && showPriorityControls">
            <Button
              icon="pi pi-angle-up"
              severity="secondary"
              variant="text"
              size="small"
              :disabled="index === 0"
              title="提高優先序"
              @click="emit('increasePriority', task.id)"
            />
            <Button
              icon="pi pi-angle-down"
              severity="secondary"
              variant="text"
              size="small"
              :disabled="index === tasks.length - 1"
              title="降低優先序"
              @click="emit('decreasePriority', task.id)"
            />
          </template>

          <!-- Pause/Resume -->
          <Button
            v-if="config.canPause(task.status as TStatus)"
            icon="pi pi-pause"
            severity="secondary"
            variant="text"
            size="small"
            title="暫停"
            @click="emit('pause', task.id)"
          />
          <Button
            v-else-if="config.isPaused(task.status as TStatus)"
            icon="pi pi-play"
            severity="secondary"
            variant="text"
            size="small"
            title="繼續"
            @click="emit('resume', task.id)"
          />

          <!-- Retry -->
          <Button
            v-if="config.canRetry(task.status as TStatus)"
            icon="pi pi-refresh"
            severity="secondary"
            variant="text"
            size="small"
            title="重試"
            @click="emit('retry', task.id)"
          />

          <!-- Cancel -->
          <Button
            v-if="config.canCancel(task.status as TStatus)"
            icon="pi pi-times"
            severity="secondary"
            variant="text"
            size="small"
            title="取消"
            @click="emit('cancel', task.id)"
          />

          <!-- Remove -->
          <Button
            v-if="
              config.isCompleted(task.status as TStatus) ||
              config.isCancelled(task.status as TStatus) ||
              config.isFailed(task.status as TStatus)
            "
            icon="pi pi-trash"
            severity="secondary"
            variant="text"
            size="small"
            title="移除"
            @click="emit('remove', task.id)"
          />
        </div>
      </div>
    </div>

    <!-- Empty State -->
    <div
      v-if="isEmpty(tasks)"
      :class="['text-surface-500', isMobile ? 'px-3 py-2' : 'px-4 py-2.5']"
    >
      <div :class="['flex items-center', isMobile ? 'gap-2' : 'gap-3']">
        <i :class="['pi', 'pi-inbox', isMobile ? 'text-3xl' : 'text-4xl']" />
        <span class="text-sm">目前沒有{{ config.title }}</span>
        <div class="h-[32px] w-[35px]" />
      </div>
    </div>
  </div>
</template>
