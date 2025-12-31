<script setup lang="ts">
import { useUiStore } from '@site/stores';
import { useSessionStore } from '@site/stores';
import { breakpointsTailwind } from '@vueuse/core';

/**
 * =====
 * Data fetching and listeners
 * =====
 */

const route = useRoute();
const sessionStore = useSessionStore();
const session = computed(() =>
  sessionStore.get((route.params as any).sessionId as string)
);

const isLoading = ref(true);

watch(
  session,
  async (session) => {
    if (!session) return;
    isLoading.value = true;
    try {
      await sessionStore.ensureMetadata(session);
    } catch (e) {
      // Error handled by store toast
    } finally {
      isLoading.value = false;
    }
  },
  { immediate: true }
);

/**
 * =====
 * UI State
 * =====
 */

const uiStore = useUiStore();
const { isSidebarPinned, sidebarWidth } = storeToRefs(uiStore);

const breakpoints = useBreakpoints(breakpointsTailwind);
const isMobile = breakpoints.smaller('md');

const MIN_WIDTH = 100;

const isDrawerVisible = ref(false);

const showPinnedSidebar = computed(
  () => isSidebarPinned.value && !isMobile.value
);

const sidebarWidths = computed({
  get: () => ({ sidebar: sidebarWidth.value }),
  set: (val) => uiStore.setSidebarWidth(val.sidebar),
});
</script>

<template>
  <div class="flex h-screen text-sm overflow-hidden bg-surface-50">
    <!-- Floating Sidebar (Drawer) -->
    <Drawer v-model:visible="isDrawerVisible" class="!w-72">
      <template #closebutton>
        <span></span>
      </template>
      <template #header>
        <div class="flex items-center justify-between w-full">
          <span class="text-xl font-semibold">
            FLAT <span class="text-primary">STORAGE</span>
          </span>
          <Button
            v-if="!isMobile"
            icon="pi pi-thumbtack"
            variant="text"
            severity="secondary"
            class="w-full"
            @click="
              uiStore.toggleSidebarPin();
              isDrawerVisible = false;
            "
          />
        </div>
      </template>
      <div class="flex flex-col h-full -mx-3">
        <SidebarMenu />
      </div>
    </Drawer>

    <!-- if sidebar is pinned-->
    <template v-if="showPinnedSidebar">
      <SplitterPx v-model:widths="sidebarWidths">
        <SplitterPxPanel
          id="sidebar"
          :size="sidebarWidths.sidebar"
          :min-size="MIN_WIDTH"
          show-handle
        >
          <div
            class="flex flex-col h-screen bg-white shadow-sm border-r border-surface"
          >
            <div class="p-4 flex items-center justify-between w-full">
              <span class="text-xl font-semibold">
                FLAT <span class="text-primary">STORAGE</span>
              </span>
              <Button
                icon="pi pi-thumbtack"
                variant="text"
                severity="secondary"
                rounded
                @click="uiStore.toggleSidebarPin()"
              />
            </div>
            <div class="flex-1 overflow-y-auto">
              <SidebarMenu />
            </div>
          </div>
        </SplitterPxPanel>

        <div class="w-full m-4 overflow-y-hidden">
          <div v-if="isLoading">
            Check Metadata file in {{ session?.metadataPath }}
          </div>
          <RouterView v-else />
        </div>
      </SplitterPx>
    </template>

    <!-- if sidebar is not pinned (or on mobile) -->
    <template v-else>
      <div
        class="w-full md:px-16 m-4 flex flex-col md:flex-row gap-2 overflow-hidden overflow-y-auto"
      >
        <div class="flex-shrink-0">
          <Button
            icon="pi pi-bars"
            variant="outlined"
            severity="secondary"
            @click="isDrawerVisible = true"
          />
        </div>
        <div class="ml-2 flex-1 w-full">
          <div v-if="isLoading">
            Check Metadata file in {{ session?.metadataPath }}
          </div>
          <RouterView v-else />
        </div>
      </div>
    </template>
  </div>
</template>
