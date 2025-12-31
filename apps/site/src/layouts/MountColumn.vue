<script setup lang="ts">
import { Entity } from '@site/components/ObjectTree';
import ColumnPanel from '@site/components/ColumnPanel.vue';
import { EntityPath, ObjectEntity, ObjectMimeType } from '@site/models';
import { pathIt, useListViewStore } from '@site/stores/list-view';
import { useMetadataStore } from '@site/stores/metadata';
import { useRoute, useRouter } from 'vue-router';

interface ColumnData {
  path: EntityPath;
  entities: Entity[];
  selectedKey: string | null;
}

const props = defineProps<{
  tree: Entity[];
  showCheckbox?: boolean;
  selectedKeys?: string[];
  indeterminateKeys?: string[];
}>();

const emits = defineEmits<{
  (e: 'nodeClick', entity: Entity): void;
  (e: 'toggleSelection', entity: Entity): void;
  (e: 'up'): void;
}>();

const route = useRoute();
const router = useRouter();
const listViewStore = useListViewStore();
const metadataStore = useMetadataStore();
const metadataStoreRefs = storeToRefs(metadataStore);

const columns = ref<ColumnData[]>([]);
const activeColumnIndex = ref<number>(0);
const scrollContainer = ref<HTMLElement>();

// 當前路徑（從路由獲取）
const path = computed(() => {
  const sessionId = (route.params as any).sessionId as string;
  const mount = (route.params as any).mount as string;
  return EntityPath.fromRoute({ sessionId, mount });
});

// 轉換 ObjectEntity 為 Entity
function toLeafNode(v: ObjectEntity): Entity {
  return {
    ...v,
    key: v.path.toString(),
    label: v.name,
    leaf: v.mimeType === ObjectMimeType.folder,
    loading: false,
  } as any;
}

// 獲取過濾和排序後的實體列表
function getFilteredEntities(targetPath: EntityPath): Entity[] {
  const allObjects = metadataStoreRefs.allObjects.value;
  if (!allObjects) return [];

  let result = pathIt(allObjects, targetPath);

  // 應用過濾
  const filter = listViewStore.filter;
  if (filter && !filter.isEmpty) {
    result = filterIt(result, filter);
  }

  // 應用排序
  const sortRules = listViewStore.sortRules;
  if (sortRules && sortRules.length > 0) {
    result = sortIt(result, sortRules);
  }

  return result.map(toLeafNode);
}

// 建立欄位鏈
function buildColumnChain(targetPath: EntityPath): ColumnData[] {
  const chain: ColumnData[] = [];
  const allObjects = metadataStoreRefs.allObjects.value;

  if (!allObjects || allObjects.length === 0) {
    return chain;
  }

  // 第一欄：根目錄
  const rootPath = EntityPath.fromRoute({
    sessionId: targetPath.sessionId,
    mount: '',
  });

  const rootEntities = getFilteredEntities(rootPath);

  chain.push({
    path: rootPath,
    entities: rootEntities,
    selectedKey:
      targetPath.depth > 0 && rootEntities.length > 0
        ? findKeyBySegment(rootEntities, targetPath.segments[0])
        : null,
  });

  // 中間欄位：每個路徑段
  for (let i = 1; i <= targetPath.depth; i++) {
    const pathAtDepth = EntityPath.fromRoute({
      sessionId: targetPath.sessionId,
      mount: targetPath.segments.slice(0, i).join('/'),
    });

    const entities = getFilteredEntities(pathAtDepth);

    chain.push({
      path: pathAtDepth,
      entities: entities,
      selectedKey:
        i < targetPath.depth && entities.length > 0
          ? findKeyBySegment(entities, targetPath.segments[i])
          : null,
    });
  }

  return chain;
}

// 根據路徑段找到對應的 key
function findKeyBySegment(entities: Entity[], segment: string): string | null {
  const found = entities.find((e) => e.path.name === segment);
  return found ? found.key : null;
}

// 滾動到最右側
function scrollToRightmost() {
  if (!scrollContainer.value) return;

  nextTick(() => {
    scrollContainer.value!.scrollTo({
      left: scrollContainer.value!.scrollWidth,
      behavior: 'smooth',
    });
  });
}

// 處理項目點擊
function handleItemClick(column: ColumnData, item: Entity, columnIndex: number) {
  // 更新當前欄位的選中狀態
  columns.value[columnIndex].selectedKey = item.key;
  activeColumnIndex.value = columnIndex;

  if (item.leaf) {
    // 點擊資料夾
    if (columnIndex === columns.value.length - 1) {
      // 情況 1：點擊最右側欄位的資料夾 → 新增欄位到右側
      columns.value.push({
        path: item.path,
        entities: getFilteredEntities(item.path),
        selectedKey: null,
      });
      activeColumnIndex.value = columns.value.length - 1;

      // 更新 URL 到新的深度
      router.push(item.path.toRoute());
      scrollToRightmost();
    } else {
      // 情況 2：點擊中間欄位的資料夾 → 截斷右側欄位
      columns.value = columns.value.slice(0, columnIndex + 1);

      // 新增一個欄位顯示該資料夾內容
      columns.value.push({
        path: item.path,
        entities: getFilteredEntities(item.path),
        selectedKey: null,
      });
      activeColumnIndex.value = columnIndex + 1;

      // 更新 URL
      router.push(item.path.toRoute());
    }
  } else {
    // 點擊檔案 → 發送事件並更新 URL
    emits('nodeClick', item);
  }
}

// 監聽路徑變化，重建欄位鏈
watch(
  path,
  (newPath) => {
    columns.value = buildColumnChain(newPath);
    activeColumnIndex.value = Math.max(0, columns.value.length - 1);
    scrollToRightmost();
  },
  { immediate: true }
);

// 監聽過濾/排序變化，重新計算所有欄位的 entities
watch(
  () => [listViewStore.filter, listViewStore.sortRules],
  () => {
    columns.value = columns.value.map((col) => ({
      ...col,
      entities: getFilteredEntities(col.path),
    }));
  }
);

// 監聽 metadata 變化
watch(
  metadataStoreRefs.allObjects,
  () => {
    if (metadataStoreRefs.allObjects.value) {
      columns.value = buildColumnChain(path.value);
      activeColumnIndex.value = Math.max(0, columns.value.length - 1);
    }
  }
);

// filterIt 和 sortIt 的本地實現（從 list-view.ts 複製）
function filterIt(raw: ObjectEntity[], f: any): ObjectEntity[] {
  if (!f || f.isEmpty) return raw;
  if (!raw || raw.length === 0) return [];

  let result = raw;

  for (const rule of f.rules) {
    const { key, operator, value, start, end } = rule;

    if (operator === 'contains') {
      const condition = (value || '').toLowerCase();
      result = result.filter((obj) =>
        (obj as any)[key]?.toLowerCase().includes(condition)
      );
    } else if (operator === 'notContains') {
      const condition = (value || '').toLowerCase();
      result = result.filter(
        (obj) => !(obj as any)[key]?.toLowerCase().includes(condition)
      );
    } else if (operator === 'equals') {
      if (typeof value === 'string') {
        const condition = value.toLowerCase();
        result = result.filter(
          (obj) => (obj as any)[key]?.toLowerCase() === condition
        );
      } else {
        result = result.filter((obj) => (obj as any)[key] === value);
      }
    } else if (operator === 'gt') {
      const numVal = Number(value);
      if (!isNaN(numVal)) {
        result = result.filter((obj) => (obj as any)[key] > numVal);
      }
    } else if (operator === 'lt') {
      const numVal = Number(value);
      if (!isNaN(numVal)) {
        result = result.filter((obj) => (obj as any)[key] < numVal);
      }
    }

    if (start) {
      result = result.filter((obj) => {
        const val = (obj as any)[key];
        if (!val) return false;
        const d = val instanceof Date ? val : new Date(val);
        return d >= start;
      });
    }
    if (end) {
      result = result.filter((obj) => {
        const val = (obj as any)[key];
        if (!val) return false;
        const d = val instanceof Date ? val : new Date(val);
        return d <= end;
      });
    }
  }

  return result;
}

function sortIt(
  data: ObjectEntity[],
  sortRules: { key: string; order: 'asc' | 'desc' }[]
): ObjectEntity[] {
  if (!sortRules || sortRules.length === 0) return data;

  return [...data].sort((a, b) => {
    for (const rule of sortRules) {
      const { key, order } = rule;
      const aVal = (a as any)[key];
      const bVal = (b as any)[key];

      let comparison = 0;
      if (aVal < bVal) comparison = -1;
      else if (aVal > bVal) comparison = 1;

      if (comparison !== 0) {
        return order === 'asc' ? comparison : -comparison;
      }
    }
    return 0;
  });
}
</script>

<template>
  <div
    ref="scrollContainer"
    class="flex h-full overflow-x-auto overflow-y-hidden"
    style="scroll-behavior: smooth; scrollbar-width: thin"
  >
    <ColumnPanel
      v-for="(column, index) in columns"
      :key="column.path.toString()"
      :path="column.path"
      :entities="column.entities"
      :selected-key="column.selectedKey"
      :active="index === activeColumnIndex"
      :show-checkbox="showCheckbox"
      :selected-keys="selectedKeys"
      :indeterminate-keys="indeterminateKeys"
      @item-click="handleItemClick(column, $event, index)"
      @toggle-selection="emits('toggleSelection', $event)"
    />
  </div>
</template>
