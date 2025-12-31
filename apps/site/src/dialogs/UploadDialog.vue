<script setup lang="ts">
import { useListViewStore } from '@site/stores/list-view';
import { useMetadataStore } from '@site/stores';
import { useSessionStore } from '@site/stores';
import { useUploadStore } from '@site/stores';
import type {
  FileUploadSelectEvent,
  FileUploadRemoveEvent,
} from 'primevue/fileupload';

const props = defineProps<{
  visible: boolean;
}>();

const emits = defineEmits<{
  (e: 'update:visible', value: boolean): void;
}>();

/**
 * Stores & Refs
 */
const sessionStore = useSessionStore();
const metadataStore = useMetadataStore();
const listViewStore = useListViewStore();
const uploadStore = useUploadStore();
const metadataStoreRefs = storeToRefs(metadataStore);
const route = useRoute();

const sessionId = computed(() => (route.params as any).sessionId as string);
const session = computed(() => sessionStore.get(sessionId.value));
const currentPath = computed(() => listViewStore.path);
const currentPathSegments = computed(() =>
  currentPath.value.toSegmentsString()
);

/**
 * State
 */
const selectedFiles = ref<File[]>([]);
const fileUploadRef = ref();
const isProcessing = ref(false);

/**
 * File Conflict Detection
 */
interface FileWithConflict {
  file: File;
  hasConflict: boolean;
  finalName: string;
}

const filesWithConflicts = computed<FileWithConflict[]>(() => {
  const existingNames = new Set(
    metadataStoreRefs.allObjects.value
      .filter((obj) => obj.path.parent?.equals(currentPath.value))
      .map((obj) => obj.name)
  );

  return selectedFiles.value.map((file) => {
    let finalName = file.name;
    let hasConflict = false;

    if (existingNames.has(finalName)) {
      hasConflict = true;
      finalName = generateUniqueName(finalName, existingNames);
    }

    return { file, hasConflict, finalName };
  });
});

function generateUniqueName(
  originalName: string,
  existingNames: Set<string>
): string {
  const lastDotIndex = originalName.lastIndexOf('.');
  const baseName =
    lastDotIndex > 0 ? originalName.slice(0, lastDotIndex) : originalName;
  const extension = lastDotIndex > 0 ? originalName.slice(lastDotIndex) : '';

  let counter = 1;
  let newName = originalName;

  while (existingNames.has(newName)) {
    newName = `${baseName} (${counter})${extension}`;
    counter++;
  }

  return newName;
}

/**
 * File Selection Handlers
 */
function handleFileSelect(event: FileUploadSelectEvent) {
  selectedFiles.value = event.files as File[];
}

function handleFileRemove(event: FileUploadRemoveEvent) {
  const removedFile = event.file as File;
  selectedFiles.value = selectedFiles.value.filter((f) => f !== removedFile);
}

function clearAllFiles() {
  selectedFiles.value = [];
  fileUploadRef.value?.clear();
}

/**
 * File Icon Helper
 */
function getFileIcon(file: File): string {
  const type = file.type;
  if (type.startsWith('image/')) return 'pi pi-image';
  if (type.startsWith('video/')) return 'pi pi-video';
  if (type.startsWith('audio/')) return 'pi pi-volume-up';
  if (type.includes('pdf')) return 'pi pi-file-pdf';
  if (type.includes('zip') || type.includes('compressed'))
    return 'pi pi-file-export';
  if (type.includes('text')) return 'pi pi-file-edit';
  return 'pi pi-file';
}

function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  let size = bytes;
  let i = 0;
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024;
    i++;
  }
  return `${size.toFixed(1)} ${units[i]}`;
}

/**
 * Upload Confirmation
 */
async function handleConfirm() {
  if (!session.value || isEmpty(selectedFiles.value)) return;

  isProcessing.value = true;
  try {
    // Set session for upload store
    uploadStore.setSession(session.value);

    // Create upload tasks for each file
    filesWithConflicts.value.forEach(({ file, finalName }) => {
      uploadStore.createTask({
        sessionId: sessionId.value,
        file: file,
        path:
          currentPathSegments.value === '/'
            ? finalName
            : joinPath(currentPathSegments.value, finalName),
      });
    });

    // Close dialog
    emits('update:visible', false);
    clearAllFiles();
  } catch (error) {
    console.error('Failed to create upload tasks:', error);
  } finally {
    isProcessing.value = false;
  }
}

function handleCancel() {
  emits('update:visible', false);
  clearAllFiles();
}

/**
 * Reset on dialog open/close
 */
watch(
  () => props.visible,
  (val) => {
    if (!val) {
      clearAllFiles();
    }
  }
);
</script>

<template>
  <Dialog
    :visible="visible"
    @update:visible="(val: boolean) => emits('update:visible', val)"
    header="Upload Files"
    modal
    class="w-[600px]"
  >
    <div class="flex flex-col gap-4 pt-2">
      <!-- Current Path Display -->
      <div
        class="flex items-center gap-2 text-sm text-surface-600 dark:text-surface-400"
      >
        <i class="pi pi-folder"></i>
        <span
          >Upload to: <strong>{{ currentPathSegments || '/' }}</strong></span
        >
      </div>

      <!-- FileUpload Component -->
      <FileUpload
        ref="fileUploadRef"
        mode="advanced"
        :multiple="true"
        :auto="false"
        :showUploadButton="false"
        :showCancelButton="false"
        chooseLabel="Choose Files"
        @select="handleFileSelect"
        @remove="handleFileRemove"
      >
        <template #empty>
          <div class="flex flex-col items-center gap-3 py-8">
            <i class="pi pi-cloud-upload text-4xl text-surface-400"></i>
            <div class="text-center">
              <p class="text-lg font-semibold mb-1">Drag & drop files here</p>
              <p class="text-sm text-surface-500">
                or click "Choose Files" button above
              </p>
            </div>
          </div>
        </template>

        <template #content>
          <div v-if="selectedFiles.length > 0" class="flex flex-col gap-2">
            <div class="flex items-center justify-between mb-2">
              <label
                class="font-semibold text-surface-600 dark:text-surface-400"
              >
                Selected Files ({{ selectedFiles.length }})
              </label>
              <Button
                label="Clear All"
                severity="secondary"
                variant="text"
                size="small"
                @click="clearAllFiles"
              />
            </div>

            <div class="max-h-60 overflow-y-auto">
              <div
                v-for="(item, index) in filesWithConflicts"
                :key="index"
                class="flex items-center gap-3 p-3 hover:bg-surface-50 border-b border-surface-200 last:border-b-0"
              >
                <i
                  :class="getFileIcon(item.file)"
                  class="text-xl text-surface-500"
                ></i>

                <div class="flex-1 min-w-0">
                  <div class="flex items-center gap-2">
                    <p class="text-sm font-medium truncate">
                      {{ item.finalName }}
                    </p>
                    <Tag
                      v-if="item.hasConflict"
                      severity="warn"
                      value="Renamed"
                      class="text-xs"
                    />
                  </div>
                  <p class="text-xs text-surface-500">
                    {{ formatFileSize(item.file.size) }}
                  </p>
                  <p
                    v-if="item.hasConflict"
                    class="text-xs text-orange-600 mt-1"
                  >
                    Original: {{ item.file.name }}
                  </p>
                </div>

                <Button
                  icon="pi pi-times"
                  severity="secondary"
                  variant="text"
                  size="small"
                  @click="
                    () => {
                      const files = selectedFiles.filter((_, i) => i !== index);
                      selectedFiles = files;
                    }
                  "
                />
              </div>
            </div>
          </div>
        </template>
      </FileUpload>

      <!-- Actions -->
      <div class="flex justify-end gap-2 pt-2">
        <Button
          label="Cancel"
          severity="secondary"
          variant="text"
          @click="handleCancel"
        />
        <Button
          label="Upload"
          :loading="isProcessing"
          :disabled="isEmpty(selectedFiles)"
          @click="handleConfirm"
        />
      </div>
    </div>
  </Dialog>
</template>
