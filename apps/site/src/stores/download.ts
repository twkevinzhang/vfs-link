import { defineStore } from 'pinia';
import { computed, ref, watch } from 'vue';
import { DownloadTask, DownloadStatus, DownloadTaskFile } from '@site/models';
import { useIDBKeyval } from '@vueuse/integrations/useIDBKeyval';
import { SessionEntity } from '@site/models';
import { proxyBucket } from '@site/services';

const MAX_CONCURRENT = 3;

class ProgressTracker {
  private lastBytes = 0;
  private lastTime = Date.now();
  private speeds: number[] = [];
  private readonly SPEED_SAMPLE_SIZE = 5;

  update(downloadedBytes: number) {
    const now = Date.now();
    const timeDiff = (now - this.lastTime) / 1000;
    const bytesDiff = downloadedBytes - this.lastBytes;

    if (timeDiff > 0) {
      const speed = bytesDiff / timeDiff;
      this.speeds.push(speed);
      if (this.speeds.length > this.SPEED_SAMPLE_SIZE) {
        this.speeds.shift();
      }
    }

    this.lastBytes = downloadedBytes;
    this.lastTime = now;
  }

  getSpeed(): number {
    if (isEmpty(this.speeds)) return 0;
    return this.speeds.reduce((a, b) => a + b, 0) / this.speeds.length;
  }

  getETA(remainingBytes: number): number {
    const speed = this.getSpeed();
    return speed > 0 ? remainingBytes / speed : 0;
  }
}

export const useDownloadStore = defineStore('download', () => {
  const session = ref<SessionEntity | null>(null);
  const abortControllers = new Map<string, AbortController>();
  const progressTrackers = new Map<string, ProgressTracker>();

  const { data: persistedTasks, isFinished } = useIDBKeyval<string[]>(
    'download-tasks',
    [],
    { shallow: false }
  );

  const tasks = computed({
    get: () => persistedTasks.value.map((json) => DownloadTask.fromJson(json)),
    set: (value: DownloadTask[]) => {
      persistedTasks.value = value.map((t) => t.toJson());
    },
  });

  const pendingTasks = computed(() =>
    tasks.value
      .filter((t) => t.status === DownloadStatus.PENDING)
      .sort((a, b) => a.priority - b.priority)
  );

  const downloadingTasks = computed(() =>
    tasks.value.filter((t) => t.status === DownloadStatus.DOWNLOADING)
  );

  const completedTasks = computed(() =>
    tasks.value.filter((t) => t.status === DownloadStatus.COMPLETED)
  );

  const failedTasks = computed(() =>
    tasks.value.filter(
      (t) =>
        t.status === DownloadStatus.FAILED ||
        t.status === DownloadStatus.EXPIRED
    )
  );

  const totalProgress = computed(() => {
    const total = tasks.value.reduce((sum, t) => sum + t.file.size, 0);
    const downloaded = tasks.value.reduce(
      (sum, t) => sum + t.downloadedBytes,
      0
    );
    return total > 0 ? (downloaded / total) * 100 : 0;
  });

  const averageSpeed = computed(() => {
    const speeds = downloadingTasks.value.map((t) => t.speed || 0);
    return speeds.length > 0
      ? speeds.reduce((a, b) => a + b, 0) / speeds.length
      : 0;
  });

  watch([downloadingTasks, pendingTasks], ([downloading, pending]) => {
    const available = MAX_CONCURRENT - downloading.length;
    if (available > 0 && pending.length > 0) {
      const toStart = pending.slice(0, available);
      toStart.forEach((task) => startDownload(task.id));
    }
  });

  async function startDownload(taskId: string) {
    const task = tasks.value.find((t) => t.id === taskId);
    if (!task || !session.value) return;

    try {
      let signedUrl = task.signedUrl;

      if (!signedUrl) {
        updateTask(taskId, {
          status: DownloadStatus.FETCHING_URL,
          error: undefined,
        });

        const bucket = proxyBucket(session.value);
        const file = bucket.file(task.file.pathOnDrive);

        signedUrl = await file.getSignedUrl({
          action: 'read',
          expires: Date.now() + 3600000, // 1 hour
        });

        updateTask(taskId, { signedUrl });
      }

      updateTask(taskId, { status: DownloadStatus.DOWNLOADING });

      const controller = new AbortController();
      abortControllers.set(taskId, controller);

      let tracker = progressTrackers.get(taskId);
      if (!tracker) {
        tracker = new ProgressTracker();
        progressTrackers.set(taskId, tracker);
      }

      const currentTask = tasks.value.find((t) => t.id === taskId);
      const startByte = currentTask?.downloadedBytes || 0;

      await downloadFile(
        signedUrl,
        task.file.name, // 使用檔案名稱作為下載的預設檔名
        (downloadedBytes) => {
          tracker.update(downloadedBytes);
          const speed = tracker.getSpeed();
          const eta = tracker.getETA(task.file.size - downloadedBytes);

          updateTask(taskId, {
            downloadedBytes,
            speed,
            eta,
          });
        },
        controller.signal,
        startByte
      );

      // Mark as completed
      const finalTask = tasks.value.find((t) => t.id === taskId);
      if (finalTask) {
        updateTask(taskId, {
          status: DownloadStatus.COMPLETED,
          downloadedBytes: finalTask.file.size,
        });
      }
    } catch (error: any) {
      if (error.name === 'AbortError' || error.name === 'CanceledError') {
        // Don't mark as CANCELLED, keep as PAUSED for resume
        // The pauseTask function will set the correct status
        return;
      }

      updateTask(taskId, {
        status: DownloadStatus.FAILED,
        error: error.message,
      });
    } finally {
      abortControllers.delete(taskId);
    }
  }

  /**
   * 工具函數
   */

  function updateTask(taskId: string, updates: Partial<DownloadTask>) {
    tasks.value = tasks.value.map((t) =>
      t.id === taskId ? DownloadTask.merge(t, updates) : t
    );
  }

  /**
   * UI 狀態
   */
  const isCollapsed = ref(false);

  return {
    tasks,
    pendingTasks,
    downloadingTasks,
    completedTasks,
    failedTasks,
    totalProgress,
    averageSpeed,
    isCollapsed,
    addTask: (file: DownloadTaskFile) => {
      if (!session.value) {
        throw new Error('No active session');
      }

      const task = DownloadTask.new({
        sessionId: session.value.id,
        file,
      });

      tasks.value = [...tasks.value, task];
    },
    pauseTask: (taskId: string) => {
      const controller = abortControllers.get(taskId);
      if (controller) {
        controller.abort();
      }
      updateTask(taskId, { status: DownloadStatus.PAUSED });
    },
    resumeTask: (taskId: string) => {
      updateTask(taskId, { status: DownloadStatus.PENDING });
    },
    retryTask: (taskId: string) => {
      const task = tasks.value.find((t) => t.id === taskId);
      if (!task) return;

      updateTask(taskId, {
        status: DownloadStatus.PENDING,
        downloadedBytes: 0,
        error: undefined,
        signedUrl: undefined,
      });
    },
    cancelTask: (taskId: string) => {
      const controller = abortControllers.get(taskId);
      if (controller) {
        controller.abort();
      }
      updateTask(taskId, { status: DownloadStatus.CANCELLED });
    },
    removeTask: (taskId: string) => {
      const controller = abortControllers.get(taskId);
      if (controller) {
        controller.abort();
        abortControllers.delete(taskId);
      }
      progressTrackers.delete(taskId);
      tasks.value = tasks.value.filter((t) => t.id !== taskId);
    },
    clearCompleted: () => {
      // 清除所有已終止的任務（COMPLETED 和 CANCELLED）
      tasks.value = tasks.value.filter(
        (t) =>
          t.status !== DownloadStatus.COMPLETED &&
          t.status !== DownloadStatus.CANCELLED
      );
    },
    clearAll: () => {
      abortControllers.forEach((controller) => controller.abort());
      abortControllers.clear();
      progressTrackers.clear();
      tasks.value = [];
    },
    toggleCollapse: () => {
      isCollapsed.value = !isCollapsed.value;
    },
    setSession: (s: SessionEntity) => {
      session.value = s;
    },
  };
});
