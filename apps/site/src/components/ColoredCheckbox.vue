<script setup lang="ts">
interface Props {
  modelValue?: boolean;
  indeterminate?: boolean;
  binary?: boolean;
  disabled?: boolean;
}

const props = defineProps<Props>();

const emit = defineEmits<{
  'update:modelValue': [value: boolean];
}>();

function handleClick() {
  if (props.disabled) return;
  emit('update:modelValue', !props.modelValue);
}

const checkboxId = `checkbox-${Math.random().toString(36).substring(7)}`;
</script>

<template>
  <div
    :class="[
      'inline-flex items-center justify-center cursor-pointer',
      { 'opacity-50 cursor-not-allowed': disabled },
    ]"
    @click="handleClick"
  >
    <div
      :class="[
        'w-5 h-5 rounded border-2 flex items-center justify-center transition-all',
        modelValue || indeterminate
          ? 'bg-sky-500 border-sky-500'
          : 'bg-white border-gray-300 hover:border-blue-400',
        !disabled && 'hover:shadow-md',
      ]"
      :style="{}"
    >
      <!-- Checkmark icon -->
      <svg
        v-if="modelValue && !indeterminate"
        class="w-3.5 h-3.5 text-white"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="3"
          d="M5 13l4 4L19 7"
        />
      </svg>

      <!-- Indeterminate icon (minus) -->
      <svg
        v-if="indeterminate"
        class="w-3.5 h-3.5 text-white"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="3"
          d="M6 12h12"
        />
      </svg>
    </div>
  </div>
</template>

<style scoped>
/* Focus visible state */
.cursor-pointer:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
  border-radius: 4px;
}
</style>
