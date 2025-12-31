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

const sort = ref<{ key: string; order: 'asc' | 'desc' }[]>([]);

watch(
  () => props.visible,
  (newVal) => {
    if (newVal) {
      sort.value = [...listViewStore.sortRules];
    }
  },
  { immediate: true }
);

const availableOptions = computed(() => {
  return Columns.filter(
    (column) => !sort.value.find((s) => s.key === column.key)
  );
});

const isSortEmpty = computed(() => isEmpty(sort.value));

function handleAddSortRule() {
  if (isNotEmpty(availableOptions.value)) {
    sort.value.push({
      key: availableOptions.value[0].key,
      order: 'asc',
    });
  }
}

function handleRemoveSortRule(index: number) {
  sort.value.splice(index, 1);
}

function handleReset() {
  sort.value = [];
}

function handleSubmit() {
  listViewStore.setSortRules(sort.value);
  emits('update:visible', false);
}

function getOptionsForSelection(currentKey: string) {
  return availableOptions.value
    .concat(Columns.filter((c) => c.key === currentKey))
    .sort((a, b) => {
      // Keep consistent order in selection
      const idxA = Columns.findIndex((c) => c.key === a.key);
      const idxB = Columns.findIndex((c) => c.key === b.key);
      return idxA - idxB;
    });
}
</script>

<template>
  <Dialog
    :visible="visible"
    @update:visible="(val: boolean) => emits('update:visible', val)"
    header="Sort Rules"
    modal
    pt:root:class="w-md h-3/4"
  >
    <div class="flex flex-col gap-4 min-h-0 overflow-y-auto">
      <template v-if="isSortEmpty">
        <template v-if="isNotEmpty(availableOptions)">
          <Hover
            label="Add Sort Rule..."
            severity="list-item"
            icon="pi-plus"
            @click="handleAddSortRule"
          />
          <div class="text-center py-8 text-surface-500 italic">
            No sort rules active.
          </div>
        </template>

        <div v-else class="text-center py-8 text-surface-500 italic">
          No columns available for sorting.
        </div>
      </template>

      <template v-else>
        <Fieldset
          :legend="`Priority ${index + 1}`"
          v-for="(s, index) in sort"
          :key="index"
        >
          <div class="flex flex-col gap-3">
            <div class="flex flex-col gap-1">
              <label class="text-xs font-semibold text-surface-500 uppercase"
                >Column</label
              >
              <Select
                :options="getOptionsForSelection(s.key)"
                optionLabel="label"
                optionValue="key"
                v-model="s.key"
                fluid
              />
            </div>

            <div class="flex flex-col gap-1">
              <label class="text-xs font-semibold text-surface-500 uppercase"
                >Direction</label
              >
              <SelectButton
                :options="[
                  { icon: 'sort-alpha-down', value: 'asc' },
                  { icon: 'sort-alpha-up', value: 'desc' },
                ]"
                optionLabel="label"
                optionValue="value"
                v-model="s.order"
                fluid
              >
                <template #option="{ option }">
                  <div class="flex items-center gap-2">
                    <PrimeIcon :name="option.icon" />
                    <span>{{ option.label }}</span>
                  </div>
                </template>
              </SelectButton>
            </div>

            <div class="flex justify-end pt-2">
              <Button
                icon="pi pi-trash"
                severity="danger"
                variant="text"
                label="Remove Rule"
                @click="handleRemoveSortRule(index)"
              />
            </div>
          </div>
        </Fieldset>

        <Hover
          v-if="isNotEmpty(availableOptions)"
          label="Add Another Sort Rule..."
          severity="list-item"
          icon="pi-plus"
          @click="handleAddSortRule"
        />
      </template>
    </div>

    <template #footer>
      <Button
        severity="secondary"
        label="Clear All"
        variant="text"
        @click="handleReset"
      />
      <Button label="Apply Sort" variant="text" @click="handleSubmit" />
    </template>
  </Dialog>
</template>
