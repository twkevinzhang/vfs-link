<script setup lang="ts">
import { Columns } from '@site/models';
import { useListViewStore } from '@site/stores/list-view';

const props = defineProps<{
  visible: boolean;
}>();

const emits = defineEmits<{
  (e: 'update:visible', value: boolean): void;
}>();

const listViewStore = useListViewStore();

const localColumns = ref<any[]>([]);
const selectedIndex = ref<number | null>(null);

watch(
  () => props.visible,
  (newVal) => {
    if (newVal) {
      localColumns.value = [...listViewStore.visibleColumns];
      selectedIndex.value = null;
    }
  },
  { immediate: true }
);

function handleSelectItem(index: number) {
  selectedIndex.value = index;
}
function handleToggleVisibility(index: number) {
  localColumns.value[index].visible = !localColumns.value[index].visible;
}
const handleMoveUp = () => reorder(selectedIndex.value! - 1);
const handleMoveDown = () => reorder(selectedIndex.value! + 1);
const handleMoveToTop = () => reorder(0);
const handleMoveToBottom = () => reorder(latestIndex(localColumns.value));
function handleReset() {
  localColumns.value = Columns.map((c) => ({ ...c, visible: true }));
}
function handleSubmit() {
  const newOrder = localColumns.value.map((c) => c.key);
  listViewStore.setOrder(newOrder);
  listViewStore.setVisibleColumns(
    localColumns.value.filter((c) => c.visible).map((c) => c.key)
  );

  emits('update:visible', false);
}

function reorder(targetIndex: number) {
  if (selectedIndex.value === null || targetIndex === selectedIndex.value)
    return;
  if (targetIndex < 0 || targetIndex > latestIndex(localColumns.value)) return;

  const items = clone(localColumns.value);
  const [removed] = items.splice(selectedIndex.value, 1);
  items.splice(targetIndex, 0, removed);

  localColumns.value = items;
  selectedIndex.value = targetIndex;
}
</script>

<template>
  <Dialog
    :visible="visible"
    @update:visible="(val: boolean) => emits('update:visible', val)"
    header="Display Columns"
    modal
    pt:root:class="w-md h-3/4"
  >
    <div class="flex flex-col h-full gap-4 py-4">
      <div class="flex gap-4 flex-1 min-h-0">
        <div
          class="flex-1 overflow-y-auto border border-surface-200 dark:border-surface-700 rounded-lg"
        >
          <ul class="list-none p-0 m-0">
            <li
              v-for="(item, index) in localColumns"
              :key="item.key"
              class="flex items-center p-3 cursor-pointer transition-colors group"
              :class="[
                selectedIndex === index
                  ? 'bg-primary-50 dark:bg-primary-900/20 text-primary-600 dark:text-primary-400'
                  : 'hover:bg-surface-100 dark:hover:bg-surface-800',
                !item.visible ? 'opacity-50' : '',
              ]"
              @click="handleSelectItem(index)"
            >
              <div class="flex items-center gap-3 flex-1 overflow-hidden">
                <Hover
                  :fluid="false"
                  :icon="item.visible ? 'pi pi-eye' : 'pi pi-eye-slash'"
                  @click="handleToggleVisibility(index)"
                />
                <span class="truncate">{{ item.label }}</span>
              </div>
              <PrimeIcon
                v-if="selectedIndex === index"
                name="check"
                class="text-primary-500"
              />
            </li>
          </ul>
        </div>

        <div class="flex flex-col gap-2 shrink-0">
          <Button
            severity="secondary"
            variant="outlined"
            :disabled="selectedIndex === null || selectedIndex === 0"
            @click="handleMoveToTop"
            v-tooltip.left="'Move to Top'"
          >
            <PrimeIcon name="angle-double-up" />
          </Button>
          <Button
            severity="secondary"
            variant="outlined"
            :disabled="selectedIndex === null || selectedIndex === 0"
            @click="handleMoveUp"
            v-tooltip.left="'Move Up'"
          >
            <PrimeIcon name="angle-up" />
          </Button>
          <Button
            severity="secondary"
            variant="outlined"
            :disabled="
              selectedIndex === null ||
              selectedIndex === latestIndex(localColumns)
            "
            @click="handleMoveDown"
            v-tooltip.left="'Move Down'"
          >
            <PrimeIcon name="angle-down" />
          </Button>
          <Button
            severity="secondary"
            variant="outlined"
            :disabled="
              selectedIndex === null ||
              selectedIndex === latestIndex(localColumns)
            "
            @click="handleMoveToBottom"
            v-tooltip.left="'Move to Bottom'"
          >
            <PrimeIcon name="angle-double-down" />
          </Button>
        </div>
      </div>
    </div>

    <template #footer>
      <Button
        severity="secondary"
        label="Reset Defaults"
        variant="text"
        @click="handleReset"
      />
      <Button label="Save Changes" variant="text" @click="handleSubmit" />
    </template>
  </Dialog>
</template>
