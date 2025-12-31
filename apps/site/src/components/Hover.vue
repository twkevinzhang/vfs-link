<script setup lang="ts">
const props = withDefaults(
  defineProps<{
    severity?: 'list-item' | 'button' | 'link' | undefined;
    rounded?: 'full' | 'l' | 'r' | 'none' | undefined;
    label?: string | undefined;
    icon?: string | undefined;
    pt?: {
      primeIcon?: any;
      span?: any;
    };
    suffixIcon?: string | undefined;
    position?: 'left' | 'right' | 'center' | undefined;
    active?: boolean | undefined;
    class?: any;
    fluid?: boolean | undefined;
    paddingSize?: 'none' | 'sm' | 'lg' | undefined;
  }>(),
  {
    severity: 'list-item',
    rounded: 'full',
    position: 'center',
    active: false,
    fluid: true,
    paddingSize: 'lg',
  }
);
const emits = defineEmits(['click']);

const internalClasses = computed(() => {
  let base = 'cursor-pointer transition-all duration-150 flex gap-2';

  if (props.severity === 'link') {
    base += ' hover:text-blue-600 hover:underline';
  } else if (props.severity === 'list-item') {
    base += ' hover:bg-gray-200/50';
  } else if (props.severity === 'button') {
    base += ' hover:bg-gray-200';
    base += ' px-2';
  }
  if (props.rounded === 'l') {
    base += ' gap-1 rounded-l-lg pl-2 pr-[4px] py-2 justify-end';
  } else if (props.rounded === 'r') {
    base += ' gap-1 rounded-r-lg pl-[4px] pr-2 py-2 justify-start';
  } else if (props.rounded === 'full') {
    base += ' rounded-lg';
  } else if (props.rounded === 'none') {
    base += ' rounded-none';
  }

  if (props.paddingSize === 'lg') {
    base += ' gap-4 p-2';
  } else if (props.paddingSize === 'sm') {
    base += ' gap-2 p-1';
  } else if (props.paddingSize === 'none') {
    base += ' p-0';
  }

  if (props.fluid) {
    base += ' w-full';
  }

  if (props.position === 'left') base += ' items-start';
  else if (props.position === 'right') base += ' items-end';
  else if (props.position === 'center') base += ' items-center';

  if (props.active) base += ' bg-gray-700';

  return base;
});

const classes = computed(() => {
  return [internalClasses.value, props.class];
});
</script>
<template>
  <Button v-slot="slotProps" asChild>
    <button
      v-bind="slotProps.a11yAttrs"
      :class="classes"
      @click="(e) => emits('click', e)"
    >
      <PrimeIcon v-if="icon" :fullname="icon" v-bind="pt?.primeIcon" />
      <slot>
        <span
          v-if="isNotEmpty(label)"
          class="text-left break-all whitespace-normal"
          v-bind="pt?.span"
        >
          {{ label }}
        </span>
      </slot>
      <PrimeIcon
        v-if="suffixIcon"
        :fullname="suffixIcon"
        v-bind="pt?.primeIcon"
      />
    </button>
  </Button>
</template>
