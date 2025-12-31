import { ObjectEntity, SessionEntity, EntityPath } from '@site/models';
import { useIDBKeyval } from '@vueuse/integrations/useIDBKeyval';
import { INJECT_KEYS } from '@site/services';
import { useToast } from 'primevue/usetoast';
import { SessionService } from '@site/services';

export const useMetadataStore = defineStore('metadata', () => {
  /**
   * =====
   * Data fetching and listener
   * =====
   */

  const sessionApi = inject<SessionService>(INJECT_KEYS.SessionService)!;
  const sessionId = ref<string | null>(null);
  const entities = ref<ObjectEntity[]>([]);
  const isLoading = ref(false);
  const toast = useToast();

  const idbKey = computed(() => `metadata_${sessionId.value || 'none'}`);
  const { data: cachedData, isFinished } = useIDBKeyval<{
    entities: any[];
    updatedAt: string;
  } | null>(idbKey.value, null, {
    deep: true,
  });

  const lastUpdated = computed(() => cachedData.value?.updatedAt || null);

  watch(
    [sessionId, cachedData, isFinished],
    ([sessionId, newData, finished]) => {
      if (sessionId && finished && newData) {
        entities.value = newData.entities.map((e) =>
          ObjectEntity.fromJson(e, sessionId)
        );
      } else if (finished && !newData && !sessionId) {
        entities.value = [];
      }
    },
    { immediate: true }
  );

  async function load(session: SessionEntity, force = false) {
    sessionId.value = session.id;

    // Wait for IDB to finish loading if it's the first time
    if (!isFinished.value) {
      await new Promise((resolve) => {
        const stop = watch(isFinished, (finished) => {
          if (finished) {
            stop();
            resolve(true);
          }
        });
      });
    }

    if (!force && isEmpty(entities.value)) {
      return;
    }

    isLoading.value = true;

    try {
      console.log('Fetching entities from GCS...');
      const list = await sessionApi.fetchEntities(session);
      const updatedAt = new Date().toISOString();

      // Update IDB directly
      cachedData.value = {
        entities: list.map((e) => JSON.parse(e.toJson())),
        updatedAt,
      };
    } catch (error: any) {
      console.error('Failed to load entities:', error);
      toast.add({
        severity: 'error',
        summary: 'Load Failed',
        detail: error.message || 'Failed to fetch entities file',
        life: 3000,
      });
    } finally {
      isLoading.value = false;
    }
  }

  async function refresh(session: SessionEntity) {
    try {
      await load(session, true);
      toast.add({
        severity: 'success',
        summary: 'Refresh Success',
        detail: 'Metadata has been reloaded from GCS',
        life: 3000,
      });
    } catch (error: any) {
      // Error already handled in load()
    }
  }

  function clear() {
    sessionId.value = null;
    entities.value = [];
  }

  async function moveFolder(
    session: SessionEntity,
    source: EntityPath,
    targetParent: EntityPath
  ) {
    isLoading.value = true;
    const targetPrefix = source.moveTo(targetParent);

    try {
      const newEntities = entities.value.map((e) => {
        const isSelf = e.path.equals(source);
        const isDescendant = e.path.isDescendantOf(source);

        if (!isSelf && !isDescendant) return e;

        let updatedPath: EntityPath;
        if (isSelf) {
          updatedPath = targetPrefix;
        } else {
          const itemSegments = e.path.segments;
          const remainingSegments = itemSegments.slice(source.depth);
          updatedPath = EntityPath.fromString(
            joinPath(
              'sessions',
              session.id,
              'mount',
              ...targetPrefix.segments,
              ...remainingSegments
            )
          );
        }

        return ObjectEntity.new({
          ...JSON.parse(e.toJson()),
          path: updatedPath,
        });
      });

      await sessionApi.saveEntities(session, newEntities);

      const updatedAt = new Date().toISOString();
      cachedData.value = {
        entities: newEntities.map((e) => JSON.parse(e.toJson())),
        updatedAt,
      };

      toast.add({
        severity: 'success',
        summary: 'Move Success',
        detail: `Moved to ${targetPrefix.toString()}`,
        life: 3000,
      });
    } catch (error: any) {
      console.error('Failed to move folder:', error);
      toast.add({
        severity: 'error',
        summary: 'Move Failed',
        detail: error.message || 'Failed to move folder',
        life: 3000,
      });
      throw error;
    } finally {
      isLoading.value = false;
    }
  }

  function copyFolder() {}

  function lockFolder() {}

  function starFolder() {}

  function deleteFolder() {}

  async function renameFolder(
    session: SessionEntity,
    oldEntityPath: EntityPath,
    newName: string
  ) {
    isLoading.value = true;
    const targetPrefix = oldEntityPath.renameTo(newName);

    try {
      const newEntities = entities.value.map((e) => {
        const isSelf = e.path.equals(oldEntityPath);
        const isDescendant = e.path.isDescendantOf(oldEntityPath);
        if (!isSelf && !isDescendant) return e;

        let updatedPath: EntityPath;
        if (isSelf) {
          updatedPath = targetPrefix;
        } else {
          const itemSegments = e.path.segments;
          const remainingSegments = itemSegments.slice(oldEntityPath.depth);
          updatedPath = EntityPath.fromString(
            joinPath(
              'sessions',
              session.id,
              'mount',
              ...targetPrefix.segments,
              ...remainingSegments
            )
          );
        }

        return ObjectEntity.new({
          ...JSON.parse(e.toJson()),
          path: updatedPath,
        });
      });

      await sessionApi.saveEntities(session, newEntities);

      const updatedAt = new Date().toISOString();
      cachedData.value = {
        entities: newEntities.map((e) => JSON.parse(e.toJson())),
        updatedAt,
      };

      toast.add({
        severity: 'success',
        summary: 'Rename Success',
        detail: `Renamed to ${newName}`,
        life: 3000,
      });
    } catch (error: any) {
      console.error('Failed to rename folder:', error);
      toast.add({
        severity: 'error',
        summary: 'Rename Failed',
        detail: error.message || 'Failed to update metadata',
        life: 3000,
      });
      throw error;
    } finally {
      isLoading.value = false;
    }
  }

  async function addEntity(session: SessionEntity, entity: ObjectEntity) {
    isLoading.value = true;

    try {
      const newEntities = [...entities.value, entity];

      await sessionApi.saveEntities(session, newEntities);

      cachedData.value = {
        entities: newEntities.map((e) => JSON.parse(e.toJson())),
        updatedAt: new Date().toISOString(),
      };

      toast.add({
        severity: 'success',
        summary: 'Upload Success',
        detail: `${entity.name} added to metadata`,
        life: 3000,
      });
    } catch (error: any) {
      console.error('Failed to add entity:', error);
      toast.add({
        severity: 'error',
        summary: 'Metadata Update Failed',
        detail: error.message || 'Failed to update metadata',
        life: 3000,
      });
      throw error;
    } finally {
      isLoading.value = false;
    }
  }

  return {
    allObjects: entities,
    isLoading,
    lastUpdated,
    loadObjects: load,
    renameFolder,
    moveFolder,
    addEntity,
    refresh,
    clear,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useMetadataStore, import.meta.hot));
}
