<script setup lang="ts">
const props = defineProps<{
  widths: Record<string, number>;
}>();

const emit = defineEmits(['update:widths']);

const resizingPanelId = ref<string | null>(null);
const startMouseX = ref(0);
const startPanelWidth = ref(0);
const { x: mouseX } = useMouse();
const { pressed: mousePressed } = useMousePressed();

// Store minSize for each panel
const panelMinSizes = ref<Record<string, number>>({});

const registerPanel = (id: string, minSize?: number) => {
  panelMinSizes.value[id] = minSize ?? 20;
};

const startResize = (id: string, event: MouseEvent) => {
  resizingPanelId.value = id;
  startMouseX.value = event.clientX;
  startPanelWidth.value = props.widths[id];
  event.preventDefault();
};

provide('splitterPx', {
  startResize,
  resizingPanelId,
  registerPanel,
});

watch(mousePressed, (val) => {
  if (!val) resizingPanelId.value = null;
});

watch(mouseX, (currentX) => {
  if (resizingPanelId.value !== null) {
    const delta = currentX - startMouseX.value;
    const minSize = panelMinSizes.value[resizingPanelId.value] ?? 20;
    const newWidth = Math.max(minSize, startPanelWidth.value + delta);
    emit('update:widths', {
      ...props.widths,
      [resizingPanelId.value]: newWidth,
    });
  }
});
</script>

<template>
  <div class="flex items-stretch overflow-hidden w-full h-full">
    <slot></slot>
  </div>
</template>
