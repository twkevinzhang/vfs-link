<script setup lang="ts">
import { Entity } from '@site/components/ObjectTree';
import { EntityPath } from '@site/models';
import { breakpointsTailwind } from '@vueuse/core';
import { useSelectModeStore } from '@site/stores/select-mode';

const breakpoints = useBreakpoints(breakpointsTailwind);
const isMobile = breakpoints.smaller('md');
const selectModeStore = useSelectModeStore();

const props = defineProps<{
  path: EntityPath;
  entities: Entity[];
  selectedKey: string | null;
  active: boolean;
  showCheckbox?: boolean;
  selectedKeys?: string[];
  indeterminateKeys?: string[];
}>();

const emits = defineEmits<{
  (e: 'itemClick', item: Entity): void;
  (e: 'toggleSelection', item: Entity): void;
}>();

function isChecked(item: Entity): boolean {
  return props.selectedKeys?.includes(item.key) || false;
}

function isIndeterminate(item: Entity): boolean {
  return props.indeterminateKeys?.includes(item.key) || false;
}

function handleToggleSelection(item: Entity, event: Event) {
  event.stopPropagation();
  // 點擊 checkbox 時自動進入選擇模式
  if (!selectModeStore.selectMode) {
    selectModeStore.enterSelectMode();
  }
  emits('toggleSelection', item);
}
</script>

<template>
  <div
    class="w-[300px] md:w-[300px] sm:w-[80vw] flex-shrink-0 border-r border-slate-200 overflow-y-auto h-full bg-white"
  >
    <div
      v-for="item in entities"
      :key="item.key"
      class="group flex items-center gap-2 px-3 py-2 cursor-pointer transition-colors duration-150 hover:bg-slate-50 border-b border-slate-50"
      :class="{
        'bg-blue-50': item.key === selectedKey && !active,
        'bg-blue-500 text-white': item.key === selectedKey && active,
      }"
      @click="emits('itemClick', item)"
    >
      <!-- Checkbox (手機版始終顯示，桌面版 hover 或選擇模式下顯示) -->
      <ColoredCheckbox
        :model-value="isChecked(item)"
        :indeterminate="isIndeterminate(item)"
        :class="[
          'flex-shrink-0 transition-opacity',
          isMobile || showCheckbox
            ? 'opacity-100'
            : 'opacity-0 group-hover:opacity-100',
        ]"
        @update:model-value="handleToggleSelection(item, $event)"
      />

      <!-- Icon -->
      <PrimeIcon
        :fullname="item.leaf ? 'pi-folder' : 'pi-file'"
        class="flex-shrink-0"
      />

      <!-- Name -->
      <span class="flex-1 truncate text-sm">{{ item.label }}</span>

      <!-- Arrow for folders -->
      <PrimeIcon
        v-if="item.leaf"
        name="angle-right"
        class="flex-shrink-0"
        :class="
          item.key === selectedKey && active
            ? 'text-blue-200'
            : 'text-slate-400'
        "
      />
    </div>

    <!-- Empty state -->
    <div
      v-if="entities.length === 0"
      class="p-4 text-center text-slate-400 text-sm"
    >
      此資料夾是空的
    </div>
  </div>
</template>
