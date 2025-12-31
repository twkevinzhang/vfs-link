<script setup lang="ts">
import { useUiStore } from '@site/stores/ui';
import { useDialogStore } from '@site/stores/dialog';

const uiStore = useUiStore();
const dialogStore = useDialogStore();

const viewModeOptions = [
  { icon: 'th-large', name: 'Grid', value: 'grid' as const },
  { icon: 'list', name: 'List', value: 'list' as const },
  { svgIcon: 'three-columns', name: 'Column', value: 'column' as const },
];

function selectViewMode(mode: 'grid' | 'list' | 'column') {
  uiStore.setViewMode(mode);
  dialogStore.close('view-mode');
}
</script>

<template>
  <Dialog
    :visible="dialogStore.visible('view-mode')"
    @update:visible="() => dialogStore.close('view-mode')"
    header="選擇視圖模式"
    modal
    :style="{ width: '90vw', maxWidth: '400px' }"
  >
    <div class="flex gap-3 justify-center p-4">
      <button
        v-for="option in viewModeOptions"
        :key="option.value"
        :class="[
          'flex flex-col items-center gap-2 px-6 py-4 rounded-lg border-2 transition-all',
          uiStore.viewMode === option.value
            ? 'border-primary-500 bg-primary-50'
            : 'border-gray-200 bg-white hover:border-gray-300',
        ]"
        @click="selectViewMode(option.value)"
      >
        <i v-if="option.icon" :class="`pi pi-${option.icon} text-2xl`" />
        <SvgIcon v-else :name="option.svgIcon" class="w-6 h-6" />
        <span class="text-sm font-medium">{{ option.name }}</span>
      </button>
    </div>
  </Dialog>
</template>
