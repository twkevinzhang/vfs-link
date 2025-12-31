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
  <div class="flex items-center justify-between">
    <!-- 左側：已選數量 + 安全操作 -->
    <div class="flex items-center gap-3">
      <span class="font-semibold">已選擇 {{ selectionsCount }} 項</span>
      <div class="h-6 w-px bg-white/30"></div>
      <Button
        :key="safeActionButtons[0].icon"
        :icon="safeActionButtons[0].icon"
        :label="safeActionButtons[0].label"
        severity="secondary"
        @click="safeActionButtons[0].handler"
      />
      <Button
        v-for="btn in safeActionButtons.slice(1)"
        :key="btn.icon"
        :icon="btn.icon"
        :label="btn.label"
        severity="info"
        @click="btn.handler"
      />
    </div>

    <!-- 右側：危險操作 + 取消 -->
    <div class="flex items-center gap-3">
      <div class="h-6 w-px bg-white/30"></div>
      <Button
        icon="pi pi-trash"
        label="刪除"
        severity="danger"
        class="text-red-300 hover:text-red-100"
        @click="onDelete"
      />
      <div class="h-6 w-px bg-white/30"></div>
      <Button
        icon="pi pi-times"
        label="取消"
        severity="info"
        @click="onCancel"
      />
    </div>
  </div>
</template>
