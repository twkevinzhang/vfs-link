<script setup lang="ts">
import { Entity } from '@site/components/ObjectTree';
import { ColumnKeys } from '@site/models';
import { useListViewStore } from '@site/stores/list-view';
import { useSelectModeStore } from '@site/stores/select-mode';
import { breakpointsTailwind } from '@vueuse/core';

const selectModeStore = useSelectModeStore();

const props = defineProps<{
  class?: any;
  isRoot: boolean;
  tree: Entity[];
  pt?: {
    root?: any;
  };
  showCheckbox?: boolean;
  selectedKeys?: string[];
  indeterminateKeys?: string[];
}>();

const emits = defineEmits<{
  (e: 'nodeToggle', node: Entity, newValue: boolean): void;
  (e: 'nodeClick', node: Entity): void;
  (e: 'showMoreClick'): void;
  (e: 'up'): void;
  (e: 'toggleSelection', node: Entity): void;
}>();

const listViewStore = useListViewStore();
const { activeColumns } = storeToRefs(listViewStore);

const columnWidths = ref<Record<string, number>>({
  [ColumnKeys.name]: 200,
  [ColumnKeys.mimeType]: 128,
  [ColumnKeys.sizeBytes]: 96,
  [ColumnKeys.createdAt]: 150,
  [ColumnKeys.latestUpdatedAt]: 176,
});

// 全選狀態
const isAllSelected = computed(() => {
  if (props.tree.length === 0) return false;
  return props.tree.every((item) => props.selectedKeys?.includes(item.key));
});

const isIndeterminate = computed(() => {
  if (props.tree.length === 0) return false;
  const selectedCount = props.tree.filter((item) =>
    props.selectedKeys?.includes(item.key)
  ).length;
  return selectedCount > 0 && selectedCount < props.tree.length;
});

// 全選/取消全選
function handleSelectAll() {
  // 點擊時自動進入選擇模式
  if (!selectModeStore.selectMode) {
    selectModeStore.enterSelectMode();
  }

  if (isAllSelected.value) {
    // 取消全選：移除所有當前顯示的項目
    props.tree.forEach((item) => {
      if (props.selectedKeys?.includes(item.key)) {
        emits('toggleSelection', item);
      }
    });
  } else {
    // 全選：添加所有當前顯示的項目
    props.tree.forEach((item) => {
      if (!props.selectedKeys?.includes(item.key)) {
        emits('toggleSelection', item);
      }
    });
  }
}
</script>
<template>
  <div v-bind="pt?.root" class="overflow-x-auto">
    <div class="min-w-max">
      <!-- Headers -->
      <SplitterPx
        v-model:widths="columnWidths"
        class="group bg-gray-50 border-b border-gray-300 font-semibold text-sm text-gray-700"
      >
        <template v-for="col in activeColumns">
          <template v-if="col.key === ColumnKeys.name">
            <SplitterPxPanel
              :key="col.key"
              :id="col.key"
              :size="columnWidths[col.key]"
              :min-size="200"
              class="p-1"
            >
              <template #default>
                <div class="flex gap-2 items-center">
                  <div class="size-8 flex items-center justify-center">
                    <ColoredCheckbox
                      :model-value="isAllSelected"
                      :indeterminate="isIndeterminate"
                      class="flex-shrink-0"
                      @update:model-value="handleSelectAll"
                    />
                  </div>
                  <div class="w-6 h-8 flex-shrink-0" />
                  <span>{{ col.label }}</span>
                </div>
              </template>
              <template #handle>
                <div
                  class="w-1 h-full opacity-0 hover:opacity-100 bg-blue-400"
                ></div>
              </template>
            </SplitterPxPanel>
          </template>
          <template v-else>
            <SplitterPxPanel
              :key="col.key"
              :id="col.key"
              :size="columnWidths[col.key]"
              :min-size="60"
              class="p-2"
            >
              <template #default>{{ col.label }}</template>
              <template #handle>
                <div
                  class="w-1 h-full opacity-0 hover:opacity-100 bg-blue-400"
                ></div>
              </template>
            </SplitterPxPanel>
          </template>
        </template>
      </SplitterPx>

      <ul>
        <!-- Parent directory row -->
        <li v-if="!isRoot">
          <div
            class="flex items-center border-b border-gray-100 bg-white hover:bg-gray-50 overflow-hidden"
          >
            <div class="size-8 flex-shrink-0"></div>
            <div class="w-6 h-8 flex-shrink-0" />
            <template v-for="col in activeColumns" :key="col.key">
              <div
                v-if="col.key === ColumnKeys.name"
                class="flex items-center flex-shrink-0 min-w-0"
                :style="{ width: columnWidths[ColumnKeys.name] + 'px' }"
              >
                <div
                  :class="[
                    'w-full p-1 overflow-hidden flex gap-2 items-center',
                  ]"
                >
                  <PrimeIcon fullname="pi pi-folder" />
                  <Hover
                    class="min-w-0"
                    label=".."
                    severity="link"
                    :fluid="false"
                    padding-size="none"
                    @click="(e) => emits('up')"
                  />
                </div>
              </div>
              <div
                v-else
                class="px-2 text-sm text-gray-600 flex-shrink-0 truncate"
                :class="{ 'text-right': col.key === ColumnKeys.sizeBytes }"
                :style="{ width: columnWidths[col.key] + 'px' }"
              >
                -
              </div>
            </template>
          </div>
        </li>

        <!-- Children -->
        <ObjectTree
          :tree="tree"
          :limit="10"
          :columns="activeColumns"
          :column-widths="columnWidths"
          :show-checkbox="showCheckbox"
          :selected-keys="selectedKeys"
          :indeterminate-keys="indeterminateKeys"
          @node-click="(e) => emits('nodeClick', e)"
          @node-toggle="(e, b) => emits('nodeToggle', e, b)"
          @toggle-selection="(e) => emits('toggleSelection', e)"
        />
      </ul>
    </div>
  </div>
</template>
