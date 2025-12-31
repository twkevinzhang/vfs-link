<script setup lang="ts">

const props = defineProps<{
  id: string;
  size: number;
  minSize?: number;
}>();

const splitter = inject<any>('splitterPx');

if (!splitter) {
  throw new Error('SplitterPxPanel must be used inside SplitterPx');
}

// Register panel with its minSize on mount
onMounted(() => {
  splitter.registerPanel(props.id, props.minSize);
});

// Re-register if minSize changes
watch(() => props.minSize, (newMinSize) => {
  splitter.registerPanel(props.id, newMinSize);
});

const handleMouseDown = (event: MouseEvent) => {
  splitter.startResize(props.id, event);
};
</script>

<template>
  <div 
    class="relative flex-shrink-0 overflow-hidden h-full" 
    :style="{ width: size + 'px' }"
  >
    <slot></slot>
    
    <!-- Resize Handle -->
    <div
      class="absolute right-0 top-0 bottom-0 cursor-col-resize hover:bg-blue-400 active:bg-blue-600 transition-colors z-20"
      @mousedown="handleMouseDown"
    >
    <slot name="handle"><div class="w-1 h-full hover:bg-gray-700 bg-gray-200 duration-200"></div></slot>
    </div>
  </div>
</template>
