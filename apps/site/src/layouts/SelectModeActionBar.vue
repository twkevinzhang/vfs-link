<script setup lang="ts">
import { useSelectModeStore } from '@site/stores/select-mode';
import { useDownloadStore } from '@site/stores';
import { DownloadTaskFile, ObjectEntity, ObjectMimeType } from '@site/models';
import { breakpointsTailwind, useBreakpoints } from '@vueuse/core';
import { useSessionStore } from '@site/stores';
import { Entity } from '@site/components/ObjectTree';

const sessionStore = useSessionStore();
const selectModeStore = useSelectModeStore();
const selectModeStoreRefs = storeToRefs(selectModeStore);
const downloadStore = useDownloadStore();
const breakpoints = useBreakpoints(breakpointsTailwind);
const isMobile = breakpoints.smaller('md');
const route = useRoute();
const sessionId = computed(() => (route.params as any).sessionId as string);
const session = computed(() => sessionStore.get(sessionId.value));

function handleMove() {
  // TODO: 實作移動功能
}

function handleDownload() {
  downloadStore.setSession(session.value!);

  if (isEmpty(selectModeStoreRefs.downloadableObjects.value)) {
    return;
  }

  // Create download tasks
  const tasks: DownloadTaskFile[] =
    selectModeStoreRefs.downloadableObjects.value.map((e) => ({
      name: e.name,
      size: e.sizeBytes || 0,
      pathOnDrive: e.pathOnDrive,
    }));

  tasks.forEach((file) => downloadStore.addTask(file));
  selectModeStore.exitSelectMode();
}

function handleAddToFavorites() {
  // TODO: 實作新增到最愛
}

function handleLock() {
  // TODO: 實作上鎖
}

function handleDelete() {
  // TODO: 實作刪除
}

function handleCancel() {
  selectModeStore.exitSelectMode();
}

// 共用的操作按鈕定義
const safeActionButtons = [
  {
    icon: 'pi pi-download',
    label: '下載',
    handler: handleDownload,
  },
  {
    icon: 'pi pi-folder-open',
    label: '移動',
    handler: handleMove,
  },
  {
    icon: 'pi pi-star',
    label: '新增到最愛',
    handler: handleAddToFavorites,
  },
  {
    icon: 'pi pi-lock',
    label: '上鎖',
    handler: handleLock,
  },
];
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition-transform duration-300 ease-out"
      leave-active-class="transition-transform duration-300 ease-in"
      enter-from-class="translate-y-full"
      leave-to-class="translate-y-full"
    >
      <div
        v-if="
          selectModeStoreRefs.selectMode.value &&
          selectModeStoreRefs.selectionsCount.value > 0
        "
        class="fixed bottom-0 left-0 right-0 text-white z-50 bg-sky-500"
        :class="isMobile ? 'py-3' : 'py-4'"
      >
        <div class="container mx-auto px-4">
          <DesktopSelectModeActionBar
            v-if="!isMobile"
            :selections-count="selectModeStoreRefs.selectionsCount.value"
            :safe-action-buttons="safeActionButtons"
            :on-delete="handleDelete"
            :on-cancel="handleCancel"
          />

          <MobileSelectModeActionBar
            v-else
            :selections-count="selectModeStoreRefs.selectionsCount.value"
            :safe-action-buttons="safeActionButtons"
            :on-delete="handleDelete"
            :on-cancel="handleCancel"
          />
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
