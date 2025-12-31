<script setup lang="ts">
import { useSelectModeStore } from '@site/stores/select-mode';

const selectModeStore = useSelectModeStore();

// ESC 鍵退出
onMounted(() => {
  const handleEsc = (e: KeyboardEvent) => {
    if (e.key === 'Escape' && selectModeStore.selectMode) {
      selectModeStore.exitSelectMode();
    }
  };
  window.addEventListener('keydown', handleEsc);
  onUnmounted(() => window.removeEventListener('keydown', handleEsc));
});
</script>

<template>
  <Teleport to="body">
    <Transition name="overlay">
      <div
        v-if="selectModeStore.selectMode"
        class="fixed inset-0 bg-black/40 z-40"
        @click.stop
      />
    </Transition>
  </Teleport>
</template>

<style scoped>
.overlay-enter-active,
.overlay-leave-active {
  transition: opacity 0.2s ease;
}

.overlay-enter-from,
.overlay-leave-to {
  opacity: 0;
}
</style>
