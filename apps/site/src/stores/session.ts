import { Driver, SessionEntity } from '@site/models';
import { Auth, INJECT_KEYS } from '@site/services';
import { SessionService } from '@site/services';
import { useToast } from 'primevue/usetoast';

export const useSessionStore = defineStore('session', () => {
  const sessionApi = inject<SessionService>(INJECT_KEYS.SessionService)!;
  const toast = useToast();
  const sessions = useLocalStorage<SessionEntity[]>('sessions', []);

  function add(session: SessionEntity) {
    sessions.value.push(session);
  }

  function remove(id: string) {
    sessions.value = sessions.value.filter((s) => s.id !== id);
  }

  function get(id: string) {
    const found = sessions.value.find((s) => s.id === id);
    if (!found) return null;
    return found;
  }

  function update(id: string, params: Partial<SessionEntity>) {
    const index = sessions.value.findIndex((s) => s.id === id);
    if (index !== -1) {
      const updated = { ...sessions.value[index], ...params };
      sessions.value[index] = updated as SessionEntity;
    }
  }

  async function ensureMetadata(session: SessionEntity) {
    try {
      await sessionApi.ensureMetadata(session);
    } catch (error: any) {
      console.error('Failed to ensure metadata:', error);
      toast.add({
        severity: 'error',
        summary: 'Metadata Integration Error',
        detail: error.message || 'Failed to sync metadata with storage',
        life: 5000,
      });
      throw error;
    }
  }

  async function listBuckets(driver: Driver, auth: Auth) {
    try {
      return await sessionApi.listBuckets(driver, auth);
    } catch (error: any) {
      console.error('Failed to list buckets:', error);
      toast.add({
        severity: 'error',
        summary: 'Connection Failed',
        detail: error.message || 'Could not fetch buckets from remote storage',
        life: 5000,
      });
      throw error;
    }
  }

  return {
    sessions,
    add,
    remove,
    get,
    update,
    ensureMetadata,
    listBuckets,
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useSessionStore, import.meta.hot));
}
