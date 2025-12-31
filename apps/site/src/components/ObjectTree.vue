<script setup lang="ts">
import { ColumnKeys, ObjectEntity } from '@site/models';
import { Entity } from '@site/components/ObjectTree';
import { breakpointsTailwind } from '@vueuse/core';
import { useSelectModeStore } from '@site/stores/select-mode';

const breakpoints = useBreakpoints(breakpointsTailwind);
const isMobile = breakpoints.smaller('md');
const selectModeStore = useSelectModeStore();

const props = withDefaults(
  defineProps<{
    tree?: Entity[];
    limit?: number;
    indent?: boolean;
    depth?: number;
    columns?: { label: string; key: ColumnKeys }[];
    columnWidths?: Record<string, number>;
    expandedKeys?: string[];
    showCheckbox?: boolean;
    selectedKeys?: string[];
    indeterminateKeys?: string[];
  }>(),
  {
    tree: () => [],
    limit: 10,
    indent: false,
    depth: 0,
    columns: () => [],
    columnWidths: () => ({}),
    showCheckbox: false,
    selectedKeys: () => [],
    indeterminateKeys: () => [],
  }
);

const emits = defineEmits<{
  (e: 'nodeToggle', node: Entity, newValue: boolean): void;
  (e: 'nodeClick', node: Entity): void;
  (e: 'showMoreClick'): void;
  (e: 'update:expandedKeys', keys: string[]): void;
  (e: 'toggleSelection', node: Entity): void;
}>();

const internalExpandedKeys = ref<Array<string>>([]);

const actualExpandedKeys = computed({
  get: () => props.expandedKeys ?? internalExpandedKeys.value,
  set: (val) => {
    internalExpandedKeys.value = val;
    emits('update:expandedKeys', val);
  },
});

function isExpanded(node: Entity) {
  return actualExpandedKeys.value.includes(node.key);
}

function isChecked(node: Entity): boolean {
  return props.selectedKeys.includes(node.key);
}

function isIndeterminate(node: Entity): boolean {
  return props.indeterminateKeys.includes(node.key);
}

function handleToggleSelection(node: Entity) {
  // 點擊 checkbox 時自動進入選擇模式
  if (!selectModeStore.selectMode) {
    selectModeStore.enterSelectMode();
  }
  emits('toggleSelection', node);
}

function angleIcon(node: Entity) {
  if (node.loading) {
    return 'pi-spin pi-spinner';
  }
  if (isExpanded(node)) {
    return 'pi-angle-down';
  }
  return 'pi-angle-right';
}
function mimeIcon(node: Entity) {
  if (node.leaf) {
    if (isExpanded(node)) {
      return 'pi-folder-open';
    } else {
      return 'pi-folder';
    }
  } else {
    return 'pi-file';
  }
}

function nodeToggle(node: Entity) {
  if (isExpanded(node)) {
    actualExpandedKeys.value = actualExpandedKeys.value.filter(
      (key) => key !== node.key
    );
    emits('nodeToggle', node, false);
  } else {
    actualExpandedKeys.value = [...actualExpandedKeys.value, node.key];
    emits('nodeToggle', node, true);
  }
}

function getCellValue(item: Entity, key: ColumnKeys) {
  const entity = ObjectEntity.new({
    path: item.path,
    pathOnDrive: item.pathOnDrive,
    mimeType: item.mimeType,
    sizeBytes: item.sizeBytes,
    uploadedAtISO: item.uploadedAtISO,
    latestUpdatedAtISO: item.latestUpdatedAtISO,
    md5Hash: item.md5Hash,
    crc32c: item.crc32c,
    xxHash64: item.xxHash64,
    deletedAtISO: item.deletedAtISO,
  });
  switch (key) {
    case ColumnKeys.name:
      return entity.name;
    case ColumnKeys.mimeType:
      return entity.mimeType || '-';
    case ColumnKeys.sizeBytes:
      return entity.sizeFormatted || '-';
    case ColumnKeys.createdAt:
      return entity.createdAt?.toLocaleString() || '-';
    case ColumnKeys.latestUpdatedAt:
      return entity.modifiedAtFormatted || '-';
    default:
      return (entity as any)[key] || '-';
  }
}
</script>

<template>
  <ul>
    <li v-for="node in take(props.tree, props.limit)" :key="node.key">
      <div
        class="group flex items-center border-b border-gray-100 bg-white hover:bg-gray-50 transition-colors"
      >
        <template v-for="col in props.columns" :key="col.key">
          <template v-if="col.key === ColumnKeys.name">
            <div
              class="flex items-center flex-shrink-0 min-w-0 w-full p-1 overflow-hidden gap-2"
              :style="{ width: props.columnWidths[col.key] + 'px' }"
            >
              <!-- Checkbox (手機版始終顯示，桌面版 hover 或選擇模式下顯示) -->
              <div
                class="size-8 flex items-center justify-center flex-shrink-0"
              >
                <ColoredCheckbox
                  :model-value="isChecked(node)"
                  :indeterminate="isIndeterminate(node)"
                  :class="[
                    'flex-shrink-0 transition-opacity',
                    isMobile || props.showCheckbox
                      ? 'opacity-100'
                      : 'opacity-0 group-hover:opacity-100',
                  ]"
                  @update:model-value="handleToggleSelection(node)"
                />
              </div>

              <!-- Indentation spacer -->
              <div
                v-if="props.depth > 0"
                class="flex-shrink-0"
                :style="{ width: `${props.depth * 32}px` }"
              />

              <!-- Angle button -->
              <template v-if="node.leaf">
                <div class="flex-shrink-0">
                  <Hover
                    class="w-6"
                    :icon="angleIcon(node)"
                    :fluid="false"
                    rounded="none"
                    @click="(e) => nodeToggle(node)"
                  />
                </div>
              </template>
              <template v-else>
                <div class="w-6 h-8 flex-shrink-0" />
              </template>

              <!-- Prefix icon -->
              <PrimeIcon :fullname="mimeIcon(node)" />

              <!-- Text -->
              <Hover
                class="min-w-0"
                :label="node.label"
                severity="link"
                :fluid="false"
                padding-size="none"
                @click="(e) => emits('nodeClick', node)"
              />
            </div>
          </template>

          <!-- Generic columns -->
          <div
            v-else
            class="px-2 text-sm text-gray-600 flex-shrink-0 truncate"
            :class="{ 'text-right': col.key === ColumnKeys.sizeBytes }"
            :style="{ width: props.columnWidths[col.key] + 'px' }"
          >
            {{ getCellValue(node, col.key) }}
          </div>
        </template>
      </div>

      <!-- Nested children -->
      <template v-if="node.leaf && isExpanded(node) && node.children">
        <ObjectTree
          :indent="true"
          :depth="props.depth + 1"
          :tree="node.children"
          :limit="props.limit"
          :columns="props.columns"
          :column-widths="props.columnWidths"
          :show-checkbox="props.showCheckbox"
          :selected-keys="props.selectedKeys"
          :expanded-keys="actualExpandedKeys"
          :indeterminate-keys="props.indeterminateKeys"
          @node-click="(node) => emits('nodeClick', node)"
          @node-toggle="(node) => nodeToggle(node)"
          @toggle-selection="(key) => emits('toggleSelection', key)"
        />
      </template>
    </li>
    <li
      v-if="props.tree.length > props.limit"
      class="pl-2 text-gray-500 italic border-b border-gray-100 bg-white"
    >
      <Hover
        :label="`... and ${props.tree.length - props.limit} more`"
        severity="link"
        :fluid="false"
        @click="(e) => emits('showMoreClick')"
      />
    </li>
  </ul>
</template>
