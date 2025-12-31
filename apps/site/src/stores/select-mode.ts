import type { Entity } from '@site/components/ObjectTree';
import { ObjectEntity } from '@site/models';

export const useSelectModeStore = defineStore('select-mode', () => {
  const selectMode = ref<boolean>(false);
  const items = ref<Entity[]>([]);
  const itemKeys = computed(() => items.value.map((item) => item.key));
  const selectionKeys = ref<Set<string>>(new Set());

  const downloadableObjects = computed(() =>
    items.value
      .filter((item) => selectionKeys.value.has(item.key))
      .map((item) =>
        ObjectEntity.new({
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
        })
      )
      .filter((e) => !e.isFolder)
  );
  const selectionsCount = computed(() => downloadableObjects.value.length);

  /**
   * 從 key 中提取父節點的 key
   * 例如：'sessions/abc/mount/folder1/file.txt' -> 'sessions/abc/mount/folder1'
   */
  function getParentKey(key: string): string | null {
    const lastSlashIndex = key.lastIndexOf('/');
    if (lastSlashIndex === -1) return null;

    const parentKey = key.substring(0, lastSlashIndex);

    // 確保不返回 'sessions/[sessionId]/mount' 之上的層級
    if (!parentKey.includes('/mount')) return null;
    if (parentKey.endsWith('/mount')) return null;

    return parentKey;
  }

  const indeterminateKeys = computed(() => {
    const parentKeys = new Set<string>();

    for (const key of selectionKeys.value) {
      let currentKey = key;
      while (true) {
        const parentKey = removeTrailingPart(currentKey);
        if (!parentKey) break;

        parentKeys.add(parentKey);
        currentKey = parentKey;
      }
    }

    const result = new Set<string>();

    for (const parentKey of parentKeys) {
      // 如果父節點本身已被選中，則不是 indeterminate
      if (selectionKeys.value.has(parentKey)) continue;

      // 檢查是否有任何被選中的項目是這個父節點的子孫
      let hasSelectedDescendant = false;
      for (const selectedKey of selectionKeys.value) {
        if (selectedKey.startsWith(parentKey + '/')) {
          hasSelectedDescendant = true;
          break;
        }
      }

      // 如果有子孫被選中，且父節點本身未被選中，則父節點是 indeterminate
      if (hasSelectedDescendant) {
        result.add(parentKey);
      }
    }

    return Array.from(result);
  });

  /**
   * 取得所有子孫節點的 keys
   */
  function getAllDescendantKeys(target: string, items: string[]): string[] {
    const keys: string[] = [];
    for (const item of items) {
      if (item.startsWith(target + '/')) {
        keys.push(item);
      }
    }
    return keys;
  }

  /**
   * 切換項目選擇狀態
   * - 勾選父節點時，自動勾選所有子節點
   * - 取消勾選父節點時，自動取消所有子節點
   */
  function toggleSelection(entity: Entity) {
    const isCurrentlySelected = selectionKeys.value.has(entity.key);

    if (isCurrentlySelected) {
      // 取消選擇：移除自己和所有子孫
      selectionKeys.value.delete(entity.key);
      const descendants = getAllDescendantKeys(entity.key, itemKeys.value);
      descendants.forEach((key) => selectionKeys.value.delete(key));

      // 遞迴向上檢查並清除沒有子孫被選擇的父親、祖父等
      // 從當前節點開始，向上遍歷所有祖先
      let currentPath = entity.key;
      while (true) {
        const parentPath = getParentKey(currentPath);
        if (!parentPath) break;

        // 檢查父節點的所有子孫是否都未被選中
        const parentDescendants = getAllDescendantKeys(
          parentPath,
          itemKeys.value
        );
        const hasAnySelectedChild = parentDescendants.some((key) =>
          selectionKeys.value.has(key)
        );

        // 如果父節點的所有子孫都未被選中，移除父節點
        if (!hasAnySelectedChild) {
          selectionKeys.value.delete(parentPath);
          currentPath = parentPath; // 繼續向上檢查祖父
        } else {
          // 如果父節點還有其他子孫被選中，就不需要繼續向上檢查了
          break;
        }
      }
    } else {
      // 選擇：加入自己和所有子孫
      selectionKeys.value.add(entity.key);
      const descendants = getAllDescendantKeys(entity.key, itemKeys.value);
      descendants.forEach((key) => selectionKeys.value.add(key));

      // 如果子孫全選，則遞迴向上加入父親、祖父等
      // 從當前節點開始，向上遍歷所有祖先
      let currentPath = entity.key;
      while (true) {
        const parentPath = getParentKey(currentPath);
        if (!parentPath) break;

        // 檢查父節點的所有子孫是否全部被選中
        const parentDescendants = getAllDescendantKeys(
          parentPath,
          itemKeys.value
        );
        const allParentChildrenSelected = parentDescendants.every((key) =>
          selectionKeys.value.has(key)
        );

        // 如果父節點的所有子孫都被選中，加入父節點
        if (allParentChildrenSelected) {
          selectionKeys.value.add(parentPath);
          currentPath = parentPath; // 繼續向上檢查祖父
        } else {
          // 如果父節點的子孫沒有全選，就不需要繼續向上檢查了
          break;
        }
      }
    }
    selectionKeys.value = new Set(selectionKeys.value);

    // 如果沒有任何項目被選中，自動退出選擇模式
    if (selectionKeys.value.size === 0) {
      exitSelectMode();
    }
  }

  function selectAllItems(keys: string[]) {
    selectionKeys.value = new Set(keys);
  }

  function clearSelection() {
    selectionKeys.value.clear();
    // 清空選擇時自動退出選擇模式
    if (selectMode.value) {
      selectMode.value = false;
    }
  }

  function enterSelectMode() {
    selectMode.value = true;
  }

  function setItems(newItems: Entity[]) {
    items.value = newItems;
  }

  function exitSelectMode() {
    selectMode.value = false;
    selectionKeys.value.clear();
  }

  watch(items, (newItems) => {
    for (const item of newItems) {
      // 向上遍歷所有祖先路徑
      let currentPath = item.key;
      while (true) {
        const parentPath = removeTrailingPart(currentPath);
        if (!parentPath) break;

        // 如果找到已選中的祖先，將新路徑加入選擇
        if (selectionKeys.value.has(parentPath)) {
          selectionKeys.value.add(item.key);
          break; // 找到一個已選中的祖先即可
        }

        currentPath = parentPath;
      }
    }

    selectionKeys.value = new Set(selectionKeys.value);
  });

  return {
    selectMode,
    items,
    selectionKeys,
    indeterminateKeys,
    selectionsCount,
    downloadableObjects,
    enterSelectMode,
    exitSelectMode,
    setItems,
    toggleSelection,
    selectAllItems,
    clearSelection,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useSelectModeStore, import.meta.hot));
}
