<script setup lang="ts">
import { useListViewStore } from '@site/stores/list-view';
import { useMetadataStore } from '@site/stores/metadata';
import { useSessionStore } from '@site/stores/session';
import { EntityPath, ObjectEntity, ObjectMimeType } from '@site/models';

const props = defineProps<{
  visible: boolean;
}>();

const emits = defineEmits<{
  (e: 'update:visible', value: boolean): void;
}>();

const sessionStore = useSessionStore();
const metadataStore = useMetadataStore();
const listViewStore = useListViewStore();
const route = useRoute();

const sessionId = computed(() => (route.params as any).sessionId as string);
const session = computed(() => sessionStore.get(sessionId.value));

const folderName = ref('');
const isLoading = ref(false);

// 重置表單
watch(
  () => props.visible,
  (val) => {
    if (val) {
      folderName.value = '';
    }
  }
);

async function handleConfirm() {
  if (!session.value || !folderName.value.trim()) return;

  const trimmedName = folderName.value.trim();

  // 驗證資料夾名稱
  if (trimmedName.includes('/')) {
    return;
  }

  isLoading.value = true;
  try {
    // 建立新資料夾的路徑
    const currentPath = listViewStore.path;
    const newFolderPath = EntityPath.fromRoute({
      sessionId: sessionId.value,
      mount: currentPath.isRootLevel
        ? trimmedName
        : `${currentPath.toSegmentsString()}/${trimmedName}`,
    });

    // 建立新的資料夾 ObjectEntity
    const newFolder = ObjectEntity.new({
      path: newFolderPath,
      mimeType: ObjectMimeType.folder,
      uploadedAtISO: new Date().toISOString(),
      latestUpdatedAtISO: new Date().toISOString(),
    });

    // 新增到 metadata
    await metadataStore.addEntity(session.value, newFolder);

    emits('update:visible', false);
  } catch (e) {
    // Error handled by store
  } finally {
    isLoading.value = false;
  }
}

function handleCancel() {
  emits('update:visible', false);
}
</script>

<template>
  <Dialog
    :visible="visible"
    @update:visible="(val: boolean) => emits('update:visible', val)"
    header="Create Folder"
    modal
    class="w-80"
  >
    <div class="flex flex-col gap-4 pt-2">
      <div class="flex flex-col gap-2">
        <label
          for="folderName"
          class="font-semibold text-surface-600 dark:text-surface-400"
        >
          Folder Name
        </label>
        <InputText
          id="folderName"
          v-model="folderName"
          class="flex-1"
          autocomplete="off"
          autofocus
          placeholder="Enter folder name"
          @keyup.enter="handleConfirm"
        />
        <small v-if="folderName.includes('/')" class="text-red-500">
          Folder name cannot contain "/"
        </small>
      </div>
      <div class="flex justify-end gap-2">
        <Button
          label="Cancel"
          severity="secondary"
          variant="text"
          @click="handleCancel"
        />
        <Button
          label="Create"
          :loading="isLoading"
          :disabled="!folderName.trim() || folderName.includes('/')"
          @click="handleConfirm"
        />
      </div>
    </div>
  </Dialog>
</template>
