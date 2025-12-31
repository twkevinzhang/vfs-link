<script setup lang="ts">
interface ActionButton {
  icon: string;
  label: string;
  handler: () => void;
}

interface Props {
  selectionsCount: number;
  safeActionButtons: ActionButton[];
  onDelete: () => void;
  onCancel: () => void;
}

defineProps<Props>();
</script>

<template>
  <div class="flex items-center gap-2">
    <!-- 已選數量（固定在左側） -->
    <span class="font-semibold text-sm whitespace-nowrap">
      {{ selectionsCount }} 項
    </span>

    <!-- 可滾動按鈕區域 -->
    <div
      class="flex-1 overflow-x-auto flex items-center gap-2 [-ms-overflow-style:none] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
    >
      <Button
        v-for="btn in safeActionButtons"
        :key="btn.icon"
        :icon="btn.icon"
        :aria-label="btn.label"
        severity="secondary"
        size="small"
        class="text-white flex-shrink-0"
        @click="btn.handler"
      />
      <div class="h-6 w-px bg-white/30 flex-shrink-0"></div>
      <Button
        icon="pi pi-trash"
        severity="danger"
        size="small"
        aria-label="刪除"
        class="text-red-300 flex-shrink-0"
        @click="onDelete"
      />
    </div>

    <!-- 取消按鈕（固定在右側） -->
    <Button
      icon="pi pi-times"
      severity="secondary"
      size="small"
      aria-label="取消"
      class="text-white flex-shrink-0"
      @click="onCancel"
    />
  </div>
</template>
