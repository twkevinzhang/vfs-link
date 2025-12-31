<script setup lang="ts">
import { useDialogStore } from '@site/stores';

const { visible, close, replaceWith } = useDialogStore();
</script>

<template>
  <MenuDialog
    :visible="visible('menu')"
    @update:visible="() => close('menu')"
    header="Action"
    :items="[
      {
        label: '重新命名',
        icon: 'pi pi-pencil',
        command: () => {
          replaceWith('rename');
        },
      },
      {
        label: '移動到...',
        icon: 'pi pi-directions',
        command: () => {
          replaceWith('move');
        },
      },
      {
        label: '上鎖',
        icon: 'pi pi-lock',
      },
      {
        label: '新增到最愛',
        icon: 'pi pi-star',
      },
      {
        label: '刪除',
        icon: 'pi pi-trash',
      },

      {
        label: 'Info',
        items: [
          {
            label: '下載',
            icon: 'pi pi-download',
          },
          {
            label: '複製路徑',
            icon: 'pi pi-clone',
          },
          {
            label: '統計',
            icon: 'pi pi-chart-bar',
          },
          {
            label: '操作紀錄',
            icon: 'pi pi-history',
          },
        ],
      },
    ]"
    :danger-items="[
      {
        label: 'Delete',
        icon: 'pi pi-trash',
      },
    ]"
  />

  <MountFilterDialog
    :visible="visible('filter')"
    @update:visible="() => close('filter')"
  />

  <MountSortDialog
    :visible="visible('sort')"
    @update:visible="() => close('sort')"
  />

  <MountOrderDialog
    :visible="visible('order')"
    @update:visible="() => close('order')"
  />

  <CreateSessionDialog
    :visible="visible('new-session')"
    @update:visible="() => close('new-session')"
  />

  <RenameDialog
    :visible="visible('rename')"
    @update:visible="() => close('rename')"
  />

  <MoveDialog
    :visible="visible('move')"
    @update:visible="() => close('move')"
  />

  <UploadDialog
    :visible="visible('upload')"
    @update:visible="() => close('upload')"
  />

  <CreateFolderDialog
    :visible="visible('create-folder')"
    @update:visible="() => close('create-folder')"
  />

  <ViewModeDialog />
</template>
