<script setup lang="ts">
import { MenuItem } from 'primevue/menuitem';
import { breakpointsTailwind, useBreakpoints } from '@vueuse/core';
import type {
  ResponsiveMenuProps,
  ResponsiveMenuEmits,
} from '@site/types/responsive-menu';

const props = withDefaults(defineProps<ResponsiveMenuProps>(), {
  items: () => [],
  dangerItems: () => [],
  useCustomContent: false,
  appendTo: 'body',
  header: 'Menu',
  dialogClass: 'w-md h-3/4',
  disabled: false,
});

const emit = defineEmits<ResponsiveMenuEmits>();

// 響應式檢測
const breakpoints = useBreakpoints(breakpointsTailwind);
const isMobile = breakpoints.smaller('md');

// Refs
const menuRef = ref();
const internalVisible = ref(false);

// Computed
const computedDangerItems = computed(() => {
  if (props.dangerItems && props.dangerItems.length > 0) {
    return [
      {
        label: 'Danger Zone',
        items: props.dangerItems,
      },
    ];
  }
  return null;
});

// Methods
function toggle(event: Event) {
  if (props.disabled) return;
  menuRef.value?.toggle(event);
}

function openMobile() {
  if (props.disabled) return;
  internalVisible.value = true;
}

function handleShow() {
  internalVisible.value = true;
  emit('open');
  emit('update:visible', true);
}

function handleHide() {
  internalVisible.value = false;
  emit('close');
  emit('update:visible', false);
}

// Expose close method for manual control
defineExpose({
  close: () => {
    if (isMobile.value) {
      internalVisible.value = false;
    } else {
      menuRef.value?.hide();
    }
  },
});
</script>

<template>
  <!-- 桌面版：Popup Menu -->
  <template v-if="!isMobile">
    <Menu
      ref="menuRef"
      :model="useCustomContent ? [] : items"
      :popup="true"
      :append-to="appendTo"
      @show="handleShow"
      @hide="handleHide"
    >
      <!-- MenuItem 格式渲染 -->
      <template v-if="!useCustomContent" #item="{ item, props: itemProps }">
        <Hover
          v-bind="itemProps.action"
          :label="item.label ?? ''"
          severity="list-item"
          :icon="item.icon"
        />
      </template>

      <!-- 自定義內容 slot -->
      <template v-if="useCustomContent">
        <slot name="menu"></slot>
      </template>
    </Menu>

    <!-- 觸發按鈕 -->
    <div @click="toggle">
      <slot></slot>
    </div>
  </template>

  <!-- 手機版：Dialog + Menu -->
  <template v-else>
    <Dialog
      v-model:visible="internalVisible"
      :header="header"
      modal
      :pt:root:class="dialogClass"
      @show="handleShow"
      @hide="handleHide"
    >
      <!-- MenuItem 格式渲染 -->
      <template v-if="!useCustomContent">
        <Menu :model="items" class="!border-none">
          <template #submenulabel="{ item }">
            <span class="font-bold">{{ item.label }}</span>
          </template>
          <template #item="{ item, props: itemProps }">
            <Hover
              v-bind="itemProps.action"
              :label="item.label ?? ''"
              severity="list-item"
              :icon="item.icon"
            />
          </template>
        </Menu>

        <!-- Danger items section -->
        <Menu
          v-if="computedDangerItems"
          :model="computedDangerItems"
          class="!border-none"
        >
          <template #submenulabel="{ item }">
            <span class="text-red-700 font-bold">{{ item.label }}</span>
          </template>
          <template #item="{ item, props: itemProps }">
            <Hover
              v-bind="itemProps.action"
              severity="list-item"
              :icon="item.icon"
              :pt="{ primeIcon: { class: '!text-red-500' } }"
            >
              <span class="text-red-700">{{ item.label }}</span>
            </Hover>
          </template>
        </Menu>
      </template>

      <!-- 自定義內容 slot -->
      <template v-else>
        <slot name="menu"></slot>
      </template>
    </Dialog>

    <!-- 觸發按鈕 -->
    <div @click="openMobile">
      <slot></slot>
    </div>
  </template>
</template>
