<script setup lang="ts">
import { Driver, SessionEntity } from '@site/models';
import { useSessionStore } from '@site/stores';
import { useDialogStore } from '@site/stores';

const dialogStore = useDialogStore();
const sessionStore = useSessionStore();
const { sessions } = storeToRefs(sessionStore);

const router = useRouter();

function image(driver: Driver) {
  return {
    [Driver.gcs]: 'gcs_512x512',
    [Driver.s3]: 's3_64x64',
    [Driver.azure]: 's3_64x64',
    [Driver.localFilesystem]: 's3_64x64',
  }[driver];
}

function handleClick(session: SessionEntity) {
  router.push({ path: joinPath('/sessions', session.id) });
}

function handleCreateSession() {
  dialogStore.open('new-session');
}
</script>

<template>
  <div class="p-4 sm:p-6 lg:p-8">
    <header class="mb-6">
      <h1 class="text-3xl font-bold text-gray-900 dark:text-white">
        Choose a Session
      </h1>
      <p class="mt-2 text-base text-gray-600 dark:text-gray-300">
        Select a cloud storage provider to connect to.
      </p>
    </header>

    <div
      class="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4"
    >
      <Card v-for="session in sessions" :key="session.id" class="h-full">
        <template #header>
          <div
            class="w-full h-36 bg-gray-50 dark:bg-gray-800 cursor-pointer transition-all duration-300 ease-in-out hover:shadow-xl hover:scale-105"
            @click="(e) => handleClick(session)"
          >
            <SvgIcon :name="image(session.driver)" class="w-full" />
          </div>
        </template>
        <template #title>
          <h2 class="text-xl font-semibold">
            {{ session.name }}
          </h2>
        </template>
        <template #content>
          <p class="text-gray-600 dark:text-gray-400">
            {{ session.description }}
          </p>
          <p>mount: {{ session.mount }}</p>
        </template>
      </Card>
      <Card
        class="h-full transition-all duration-300 ease-in-out cursor-pointer hover:shadow-xl hover:scale-105"
        @click="(e) => handleCreateSession()"
      >
        <template #header>
          <div class="w-full h-36">
            <PrimeIcon name="plus" size="large" class="size-full" />
          </div>
        </template>
        <template #title>
          <h2 class="text-xl font-semibold">新建連線</h2>
        </template>
        <template #content>
          <p class="text-gray-600 dark:text-gray-400">建立一個新的連線吧！</p>
        </template>
      </Card>
    </div>
  </div>
</template>
