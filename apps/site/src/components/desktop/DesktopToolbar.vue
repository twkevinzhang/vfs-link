<script setup lang="ts">
interface ToolbarButton {
  icon: string;
  label: string;
  handler: () => void;
  badge?: () => number | string | undefined;
}

interface ViewModeOption {
  icon?: string;
  svgIcon?: string;
  name: string;
  value: 'grid' | 'list' | 'column';
}

interface Props {
  viewMode: 'grid' | 'list' | 'column';
  viewModeOptions: ViewModeOption[];
  selectModeDisabledClass: any;
  toolbarButtons: {
    refresh: ToolbarButton;
    filter: ToolbarButton;
    sort: ToolbarButton;
    columnOrder: ToolbarButton;
  };
  uploadMenuModel: any[];
}

const props = defineProps<Props>();

const emit = defineEmits<{
  'update:viewMode': [value: 'grid' | 'list' | 'column'];
  upload: [];
}>();

function handleUpload() {
  emit('upload');
}
</script>

<template>
  <!-- 工具列容器 -->
  <div class="flex justify-between">
    <!-- 視圖模式選擇器（桌面版） -->
    <div class="flex items-center gap-3 relative">
      <SplitButton
        label="Upload"
        severity="primary"
        :model="props.uploadMenuModel"
        @click="handleUpload()"
      />

      <!-- 桌面版：篩選排序按鈕組 -->
      <ButtonGroup>
        <Button
          :icon="props.toolbarButtons.filter.icon"
          severity="secondary"
          badge-severity="contrast"
          :aria-label="props.toolbarButtons.filter.label"
          :badge="props.toolbarButtons.filter.badge()"
          @click="props.toolbarButtons.filter.handler"
        />
        <Button
          :icon="props.toolbarButtons.sort.icon"
          severity="secondary"
          badge-severity="contrast"
          :aria-label="props.toolbarButtons.sort.label"
          :badge="props.toolbarButtons.sort.badge()"
          @click="props.toolbarButtons.sort.handler"
        />
      </ButtonGroup>
    </div>

    <!-- 桌面版：主要操作 -->
    <div class="flex items-center gap-2" :class="props.selectModeDisabledClass">
      <SelectButton
        :model-value="props.viewMode"
        size="small"
        option-label="name"
        option-value="value"
        :options="props.viewModeOptions"
        @update:model-value="(value) => emit('update:viewMode', value)"
      >
        <template #option="{ option }">
          <PrimeIcon v-if="option.icon" :name="option.icon" />
          <SvgIcon v-else :name="option.svgIcon" class="text-slate-500" />
        </template>
      </SelectButton>

      <Button
        :icon="props.toolbarButtons.columnOrder.icon"
        severity="secondary"
        variant="outlined"
        :aria-label="props.toolbarButtons.columnOrder.label"
        @click="props.toolbarButtons.columnOrder.handler"
      />
      <Button
        :icon="props.toolbarButtons.refresh.icon"
        severity="secondary"
        variant="outlined"
        :aria-label="props.toolbarButtons.refresh.label"
        @click="props.toolbarButtons.refresh.handler"
      />
    </div>
  </div>
</template>
