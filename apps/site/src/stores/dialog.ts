export const useDialogStore = defineStore('dialog', () => {
  type DialogKey =
    | 'filter'
    | 'sort'
    | 'order'
    | 'setting'
    | 'add'
    | 'info'
    | 'menu'
    | 'rename'
    | 'move'
    | 'new-session'
    | 'upload'
    | 'create-folder'
    | 'view-mode';
  const state = ref<Partial<Record<DialogKey, boolean>>>({});

  return {
    visible: (key: DialogKey) => state.value[key] ?? false,
    toggle: (key: DialogKey) =>
      (state.value[key] = !(state.value[key] ?? false)),
    open: (key: DialogKey) => (state.value[key] = true),
    replaceWith: (key: DialogKey) => {
      state.value = { [key]: true };
    },
    close: (key: DialogKey) => (state.value[key] = false),
  };
});

if (import.meta.hot) {
  import.meta.hot.accept(acceptHMRUpdate(useDialogStore, import.meta.hot));
}
