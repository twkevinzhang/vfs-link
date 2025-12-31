<script setup lang="ts">
interface ToolbarButton {
  icon: string;
  label: string;
  handler: () => void;
  badge?: () => number | string | undefined;
}

interface Props {
  moreMenuItems: any[];
  selectModeDisabledClass: any;
  toolbarButtons: {
    refresh: ToolbarButton;
    filter: ToolbarButton;
    sort: ToolbarButton;
    columnOrder: ToolbarButton;
  };
  uploadMenuModel: any[];
}

const props = defineProps<Props>();

const emit = defineEmits<{
  upload: [];
}>();

const moreMenu = ref();

function handleUpload() {
  emit('upload');
}
</script>

<template>
  <!-- 工具列容器 -->
  <div class="flex gap-2 flex-col">
    <!-- 手機版：更多選單 + 操作按鈕 -->
    <div class="flex items-center gap-2 w-full relative">
      <Menu
        ref="moreMenu"
        :model="props.moreMenuItems"
        :popup="true"
      />
      <!-- 更多選項按鈕 -->
      <Button
        icon="pi pi-sliders-h"
        severity="secondary"
        variant="outlined"
        aria-label="更多選項"
        class="relative z-50"
        @click="(e) => moreMenu?.toggle(e)"
      />
      <div
        class="flex items-center gap-2 flex-1"
        :class="props.selectModeDisabledClass"
      >
        <Button
          :icon="props.toolbarButtons.refresh.icon"
          severity="secondary"
          variant="outlined"
          :aria-label="props.toolbarButtons.refresh.label"
          @click="props.toolbarButtons.refresh.handler"
        />
        <SplitButton
          label="Upload"
          severity="primary"
          :model="props.uploadMenuModel"
          @click="handleUpload()"
        />
      </div>
    </div>
  </div>
</template>
