<script setup lang="ts">
import { useListViewStore } from '@site/stores/list-view';
import { useMetadataStore } from '@site/stores';
import { useSessionStore } from '@site/stores';
import {
  ObjectEntity,
  ColumnKeys,
  ObjectMimeType,
  EntityPath,
} from '@site/models';
import { Entity } from '@site/components/ObjectTree';

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
const metadataStoreRefs = storeToRefs(metadataStore);
const route = useRoute();
const router = useRouter();

const sessionId = computed(() => (route.params as any).sessionId as string);
const session = computed(() => sessionStore.get(sessionId.value));

/**
 * State
 */
const isLoading = ref(false);
const destinationPath = ref('/');
const targetPath = computed(() =>
  EntityPath.fromRoute({
    sessionId: sessionId.value,
    mount: destinationPath.value,
  })
);
const expandedKeys = ref<string[]>([]);

/**
 * Tree Helpers
 */

function buildTreeNodes(entities: ObjectEntity[]): Entity[] {
  const rootEntities: Entity[] = [];
  const nodeMap = new Map<string, Entity>();
  const folders = entities.filter((e) => e.mimeType === ObjectMimeType.folder);

  folders.forEach((f) => {
    const node: Entity = {
      ...f,
      key: f.path.toSegmentsString(),
      label: f.name,
      leaf: true,
      loading: false,
      children: [],
    } as any;

    nodeMap.set(f.path.toSegmentsString(), node);
  });

  folders.forEach((f) => {
    const pathStr = f.path.toSegmentsString();
    const node = nodeMap.get(pathStr);
    if (!node) return;

    const depth = f.path.depth;
    if (depth === 1) {
      rootEntities.push(node);
    } else {
      const parentPath = f.path.parent.toSegmentsString();
      const parent = nodeMap.get(parentPath);
      if (parent) {
        if (!parent.children) parent.children = [];
        parent.children.push(node);
      } else {
        rootEntities.push(node);
      }
    }
  });

  return rootEntities;
}

const treeNodes = computed(() => {
  return buildTreeNodes(metadataStoreRefs.allObjects.value);
});

const columns = [{ label: 'Name', key: ColumnKeys.name }];
const columnWidths = {
  [ColumnKeys.name]: 300,
};

/**
 * Watchers
 */
watch(destinationPath, (newPath) => {
  if (!newPath || newPath === '/') return;
  const segments = newPath.split('/').filter(Boolean);
  const keysToExpand = ['/'];

  segments.forEach((_, index) => {
    if (index < latestIndex(segments)) {
      const p = segments.slice(0, index + 1).join('/');
      keysToExpand.push(p);
    }
  });

  const uniqueKeys = new Set([...expandedKeys.value, ...keysToExpand]);
  expandedKeys.value = Array.from(uniqueKeys);
});

// Reset on open
watch(
  () => props.visible,
  (val) => {
    if (val) {
      destinationPath.value = '/'; // Default to root
      expandedKeys.value = [];
    }
  }
);

/**
 * Actions
 */
function handleNodeClick(node: Entity) {
  destinationPath.value = node.path.toSegmentsString();
}

async function handleConfirm() {
  if (!session.value || !destinationPath.value) return;

  const sourcePath = listViewStore.path;

  if (
    targetPath.value.equals(sourcePath) ||
    targetPath.value.isDescendantOf(sourcePath)
  ) {
    return;
  }

  isLoading.value = true;
  try {
    await metadataStore.moveFolder(session.value, sourcePath, targetPath.value);
    navigateToNewPage();
    emits('update:visible', false);
  } catch (e) {
    // Error handled by store
  } finally {
    isLoading.value = false;
  }
}

function navigateToNewPage() {
  const sourcePath = listViewStore.path;
  const finalPath = sourcePath.moveTo(targetPath.value);
  router.replace({
    path: finalPath.toRoute(),
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
    header="Move to"
    modal
    class="w-[500px]"
  >
    <div class="flex flex-col gap-4 pt-2 h-[400px]">
      <div class="flex flex-col gap-2">
        <label
          for="destination"
          class="font-semibold text-surface-600 dark:text-surface-400"
          >Destination</label
        >
        <InputText
          id="destination"
          v-model="destinationPath"
          class="w-full"
          placeholder="/path/to/destination"
        />
      </div>

      <div
        class="flex-1 overflow-y-auto border border-surface-200 rounded-md p-2"
      >
        <div
          class="cursor-pointer p-2 hover:bg-gray-100 rounded mb-1"
          :class="{
            'bg-primary-50': targetPath.isRootLevel,
          }"
          @click="destinationPath = '/'"
        >
          <div class="flex items-center gap-2">
            <i class="pi pi-folder text-yellow-500"></i>
            <span>/ (Root)</span>
          </div>
        </div>

        <ObjectTree
          :tree="treeNodes"
          :limit="1000"
          :columns="columns"
          :column-widths="columnWidths"
          :selected-key="destinationPath"
          :expanded-keys="expandedKeys"
          @node-click="handleNodeClick"
        />
      </div>

      <div class="flex justify-end gap-2 pt-2">
        <Button
          label="Cancel"
          severity="secondary"
          variant="text"
          @click="handleCancel"
        />
        <Button
          label="Move"
          :loading="isLoading"
          :disabled="!destinationPath"
          @click="handleConfirm"
        />
      </div>
    </div>
  </Dialog>
</template>
