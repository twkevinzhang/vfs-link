<script setup lang="ts">
const props = withDefaults(
  defineProps<{
    name: string;
    class?: any;
    size?: 'small' | 'medium' | 'large';
  }>(),
  {
    class: undefined,
    size: 'medium',
  }
);

const svgPath = computed(() => `/svg/${props.name}.svg`);

const sizeClass = computed(() => {
  switch (props.size) {
    case 'small':
      return 'w-4 h-4'; // 1rem = 16px
    case 'large':
      return 'w-6 h-6'; // 1.5rem = 24px
    case 'medium':
    default:
      return 'w-5 h-5'; // 1.25rem = 20px (PrimeIcon default size)
  }
});

const mergedClass = computed(() => {
  // 添加 inline-block 和 flex-shrink-0 以匹配 PrimeIcon 行為
  return ['inline-block flex-shrink-0', sizeClass.value, props.class]
    .filter(Boolean)
    .join(' ');
});

const maskStyle = computed(() => ({
  maskImage: `url(${svgPath.value})`,
  WebkitMaskImage: `url(${svgPath.value})`,
  maskSize: 'contain',
  WebkitMaskSize: 'contain',
  maskRepeat: 'no-repeat',
  WebkitMaskRepeat: 'no-repeat',
  maskPosition: 'center',
  WebkitMaskPosition: 'center',
  backgroundColor: 'currentColor',
}));
</script>

<template>
  <span
    :class="mergedClass"
    :style="maskStyle"
    aria-hidden="true"
  />
</template>
