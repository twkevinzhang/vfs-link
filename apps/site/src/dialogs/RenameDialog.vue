<script setup lang="ts">
import { useListViewStore } from '@site/stores/list-view';
import { useMetadataStore } from '@site/stores';
import { useSessionStore } from '@site/stores';

const props = defineProps<{
  visible: boolean;
}>();

const emits = defineEmits<{
  (e: 'update:visible', value: boolean): void;
}>();

const sessionStore = useSessionStore();
const metadataStore = useMetadataStore();
const listViewStore = useListViewStore();
const listViewStoreRefs = storeToRefs(listViewStore);
const route = useRoute();
const router = useRouter();

const sessionId = computed(() => (route.params as any).sessionId as string);
const session = computed(() => sessionStore.get(sessionId.value));

const newName = ref('');

watch(
  () => props.visible,
  (val) => {
    if (val) {
      newName.value = listViewStoreRefs.name.value;
    }
  }
);

const isLoading = ref(false);

async function handleConfirm() {
  if (!session.value || !listViewStoreRefs.name.value || !newName.value) return;

  isLoading.value = true;
  try {
    await metadataStore.renameFolder(
      session.value,
      listViewStoreRefs.path.value,
      newName.value
    );
    navigateToNewPage();
    emits('update:visible', false);
  } catch (e) {
    // Error handled by store
  } finally {
    isLoading.value = false;
  }
}

function navigateToNewPage() {
  const newPath = listViewStore.path.renameTo(newName.value);
  router.replace({
    path: newPath.toRoute(),
  });
}

function handleCancel() {
  emits('update:visible', false);
}
</script>

<template>
  <Dialog
    :visible="visible"
    @update:visible="(val: boolean) => emits('update:visible', val)"
    header="Rename"
    modal
    class="w-80"
  >
    <div class="flex flex-col gap-4 pt-2">
      <div class="flex flex-col gap-2">
        <label
          for="newName"
          class="font-semibold text-surface-600 dark:text-surface-400"
          >New Name</label
        >
        <InputText
          id="newName"
          v-model="newName"
          class="flex-1"
          autocomplete="off"
          autofocus
          @keyup.enter="handleConfirm"
        />
      </div>
      <div class="flex justify-end gap-2">
        <Button
          label="Cancel"
          severity="secondary"
          variant="text"
          @click="handleCancel"
        />
        <Button
          label="Rename"
          :loading="isLoading"
          :disabled="!newName"
          @click="handleConfirm"
        />
      </div>
    </div>
  </Dialog>
</template>
