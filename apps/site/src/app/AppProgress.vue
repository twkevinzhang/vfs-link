<script setup lang="ts">
import { DownloadStatus, DownloadTask } from '@site/models';
import { UploadStatus, UploadTask } from '@site/models';
import { useDownloadStore } from '@site/stores';
import { useUploadStore } from '@site/stores';
import { breakpointsTailwind } from '@vueuse/core';
import TaskProgress from '@site/components/TaskProgress.vue';

const breakpoints = useBreakpoints(breakpointsTailwind);
const isMobile = breakpoints.smaller('md');

/**
 * Upload state
 */
const uploadStore = useUploadStore();
const uploadStoreRefs = storeToRefs(uploadStore);

/**
 * beforeunload 警告 - 防止用戶在上傳時意外關閉頁面
 */
function handleBeforeUnload(event: BeforeUnloadEvent) {
  if (uploadStore.hasActiveUploads) {
    event.preventDefault();
    // 現代瀏覽器會忽略自訂訊息，顯示標準警告
    event.returnValue = '';
    return '';
  }
}

onMounted(() => {
  window.addEventListener('beforeunload', handleBeforeUnload);
});

onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', handleBeforeUnload);
});

/**
 * 請求通知權限（首次進入時）
 */
onMounted(async () => {
  // 延遲 2 秒後請求通知權限，避免過於突兀
  setTimeout(async () => {
    if (uploadStore.notificationPermission === 'default') {
      await uploadStore.requestNotificationPermission();
    }
  }, 2000);
});

/**
 * Download state
 */
const downloadStore = useDownloadStore();
const downloadStoreRefs = storeToRefs(downloadStore);
</script>
<template>
  <div
    :class="[
      'fixed bg-white dark:bg-surface-900 shadow-2xl rounded-lg border border-surface-200 dark:border-surface-700 z-30',
      isMobile
        ? 'right-2 bottom-2 w-full max-w-[calc(100vw-1rem)]'
        : 'right-4 bottom-4 w-[600px] max-w-[calc(100vw-2rem)]',
    ]"
  >
    <!-- Upload progress -->
    <TaskProgress
      :tasks="uploadStoreRefs.tasks.value"
      :active-tasks="uploadStoreRefs.activeTasks.value"
      :completed-tasks="uploadStoreRefs.completedTasks.value"
      :total-progress="uploadStoreRefs.totalProgress.value"
      :is-collapsed="uploadStoreRefs.isCollapsed.value"
      :is-mobile="isMobile"
      :config="{
        // Header 配置
        icon: 'pi-cloud-upload',
        iconColor: 'text-primary-500',
        title: '上傳任務',

        // 狀態映射
        statusMap: {
          getIcon: (status: UploadStatus): string => {
            switch (status) {
              case UploadStatus.PENDING:
                return 'pi pi-clock';
              case UploadStatus.CALCULATING:
                return 'pi pi-spin pi-spinner';
              case UploadStatus.UPLOADING:
                return 'pi pi-spin pi-spinner';
              case UploadStatus.PAUSED:
                return 'pi pi-pause';
              case UploadStatus.COMPLETED:
                return 'pi pi-check-circle';
              case UploadStatus.FAILED:
                return 'pi pi-times-circle';
              case UploadStatus.EXPIRED:
                return 'pi pi-exclamation-circle';
              case UploadStatus.VERIFICATION_FAILED:
                return 'pi pi-exclamation-triangle';
              case UploadStatus.CANCELLED:
                return 'pi pi-ban';
              default:
                return 'pi pi-file';
            }
          },
          getColor: (status: UploadStatus): string => {
            switch (status) {
              case UploadStatus.PENDING:
                return 'text-gray-500';
              case UploadStatus.CALCULATING:
              case UploadStatus.UPLOADING:
                return 'text-blue-500';
              case UploadStatus.PAUSED:
              case UploadStatus.EXPIRED:
                return 'text-orange-500';
              case UploadStatus.COMPLETED:
                return 'text-green-500';
              case UploadStatus.FAILED:
              case UploadStatus.VERIFICATION_FAILED:
                return 'text-red-500';
              case UploadStatus.CANCELLED:
                return 'text-gray-500';
              default:
                return 'text-gray-500';
            }
          },
          getLabel: (status: UploadStatus): string => {
            switch (status) {
              case UploadStatus.PENDING:
                return '等待中';
              case UploadStatus.CALCULATING:
                return '計算中';
              case UploadStatus.UPLOADING:
                return '上傳中';
              case UploadStatus.PAUSED:
                return '已暫停';
              case UploadStatus.COMPLETED:
                return '已完成';
              case UploadStatus.FAILED:
                return '失敗';
              case UploadStatus.EXPIRED:
                return '已過期';
              case UploadStatus.VERIFICATION_FAILED:
                return '驗證失敗';
              case UploadStatus.CANCELLED:
                return '已取消';
              default:
                return status;
            }
          },
        },

        // 任務特定邏輯
        getFileName: (task: UploadTask): string => {
          const parts = task.path.split('/');
          return parts[parts.length - 1] ?? '';
        },
        getProgress: (task: UploadTask): number => {
          if (task.status === UploadStatus.COMPLETED) return 100;
          if (task.file.size === 0) return 0;
          return Math.round((task.uploadedBytes / task.file.size) * 100);
        },
        getUploadedOrDownloadedBytes: (task: UploadTask): number => task.uploadedBytes,

        // 狀態判斷
        isCompleted: (status: UploadStatus): boolean => status === UploadStatus.COMPLETED,
        isCancelled: (status: UploadStatus): boolean => status === UploadStatus.CANCELLED,
        isActive: (status: UploadStatus): boolean => status === UploadStatus.UPLOADING,
        isPaused: (status: UploadStatus): boolean => status === UploadStatus.PAUSED,
        isFailed: (status: UploadStatus): boolean =>
          status === UploadStatus.FAILED ||
          status === UploadStatus.EXPIRED ||
          status === UploadStatus.VERIFICATION_FAILED,
        canRetry: (status: UploadStatus): boolean =>
          status === UploadStatus.FAILED ||
          status === UploadStatus.EXPIRED ||
          status === UploadStatus.VERIFICATION_FAILED,
        canPause: (status: UploadStatus): boolean => status === UploadStatus.UPLOADING,
        canCancel: (status: UploadStatus): boolean =>
          status !== UploadStatus.COMPLETED &&
          status !== UploadStatus.CANCELLED &&
          status !== UploadStatus.FAILED,
      }"
      :show-clear-completed="true"
      :show-priority-controls="true"
      @pause="uploadStore.pauseTask"
      @resume="uploadStore.resumeTask"
      @retry="uploadStore.retryTask"
      @remove="uploadStore.removeTask"
      @cancel="uploadStore.cancelTask"
      @clear-completed="uploadStore.clearCompletedTasks"
      @toggle-collapse="uploadStore.toggleCollapse"
      @increase-priority="uploadStore.increasePriority"
      @decrease-priority="uploadStore.decreasePriority"
    />

    <!-- Download progress -->
    <TaskProgress
      :tasks="downloadStoreRefs.tasks.value"
      :active-tasks="downloadStoreRefs.downloadingTasks.value"
      :completed-tasks="downloadStoreRefs.completedTasks.value"
      :failed-tasks="downloadStoreRefs.failedTasks.value"
      :total-progress="downloadStoreRefs.totalProgress.value"
      :is-collapsed="downloadStoreRefs.isCollapsed.value"
      :is-mobile="isMobile"
      :config="{
        // Header 配置
        icon: 'pi-download',
        iconColor: 'text-blue-500',
        title: '下載任務',

        // 狀態映射
        statusMap: {
          getIcon: (status: DownloadStatus): string => {
            switch (status) {
              case DownloadStatus.PENDING:
                return 'pi pi-clock';
              case DownloadStatus.FETCHING_URL:
                return 'pi pi-spin pi-spinner';
              case DownloadStatus.DOWNLOADING:
                return 'pi pi-spin pi-spinner';
              case DownloadStatus.PAUSED:
                return 'pi pi-pause';
              case DownloadStatus.COMPLETED:
                return 'pi pi-check-circle';
              case DownloadStatus.FAILED:
                return 'pi pi-times-circle';
              case DownloadStatus.EXPIRED:
                return 'pi pi-exclamation-circle';
              case DownloadStatus.CANCELLED:
                return 'pi pi-ban';
              default:
                return 'pi pi-file';
            }
          },
          getColor: (status: DownloadStatus): string => {
            switch (status) {
              case DownloadStatus.PENDING:
                return 'text-gray-500';
              case DownloadStatus.FETCHING_URL:
              case DownloadStatus.DOWNLOADING:
                return 'text-blue-500';
              case DownloadStatus.PAUSED:
              case DownloadStatus.EXPIRED:
                return 'text-orange-500';
              case DownloadStatus.COMPLETED:
                return 'text-green-500';
              case DownloadStatus.FAILED:
                return 'text-red-500';
              case DownloadStatus.CANCELLED:
                return 'text-gray-500';
              default:
                return 'text-gray-500';
            }
          },
          getLabel: (status: DownloadStatus): string => {
            switch (status) {
              case DownloadStatus.PENDING:
                return '等待中';
              case DownloadStatus.FETCHING_URL:
                return '取得下載網址';
              case DownloadStatus.DOWNLOADING:
                return '下載中';
              case DownloadStatus.PAUSED:
                return '已暫停';
              case DownloadStatus.COMPLETED:
                return '已完成';
              case DownloadStatus.FAILED:
                return '失敗';
              case DownloadStatus.EXPIRED:
                return '已過期';
              case DownloadStatus.CANCELLED:
                return '已取消';
              default:
                return status;
            }
          },
        },

        // 任務特定邏輯
        getFileName: (task: DownloadTask): string => task.file.relativePath,
        getProgress: (task: DownloadTask): number => {
          if (task.status === DownloadStatus.COMPLETED) return 100;
          if (task.file.size === 0) return 0;
          return Math.round((task.downloadedBytes / task.file.size) * 100);
        },
        getUploadedOrDownloadedBytes: (task: DownloadTask): number => task.downloadedBytes,

        // 狀態判斷
        isCompleted: (status: DownloadStatus): boolean => status === DownloadStatus.COMPLETED,
        isCancelled: (status: DownloadStatus): boolean => status === DownloadStatus.CANCELLED,
        isActive: (status: DownloadStatus): boolean => status === DownloadStatus.DOWNLOADING,
        isPaused: (status: DownloadStatus): boolean => status === DownloadStatus.PAUSED,
        isFailed: (status: DownloadStatus): boolean =>
          status === DownloadStatus.FAILED || status === DownloadStatus.EXPIRED,
        canRetry: (status: DownloadStatus): boolean =>
          status === DownloadStatus.FAILED || status === DownloadStatus.EXPIRED,
        canPause: (status: DownloadStatus): boolean => status === DownloadStatus.DOWNLOADING,
        canCancel: (status: DownloadStatus): boolean => false, // Download 不支援取消
      }"
      :show-clear-completed="true"
      :show-retry-all="true"
      @pause="downloadStore.pauseTask"
      @resume="downloadStore.resumeTask"
      @retry="downloadStore.retryTask"
      @remove="downloadStore.removeTask"
      @clearCompleted="downloadStore.clearCompleted"
      @retryAll="downloadStore.retryAll"
      @toggleCollapse="downloadStore.toggleCollapse"
    />
  </div>
</template>
