<script setup lang="ts">
import { Form } from '@primevue/forms';
import { SessionEntity, Driver, BucketEntity, EntityPath } from '@site/models';
import { useSessionStore } from '@site/stores';
import type { Auth } from '@site/services';

const sessionStore = useSessionStore();

const RemoteDriver = [Driver.gcs, Driver.s3];

const { visible } = defineProps<{
  visible: boolean;
}>();

const emits = defineEmits<{
  (e: 'update:visible', value: boolean): void;
}>();

const initialValues = reactive({
  name: '',
  description: '',
  driver: Driver.gcs,
  mount: '/', // bucket
  metadataPath: '/metadata.json',
  // GCS fields
  serviceAccountJson: '',
  // S3 fields
  accessKey: '',
  secretKey: '',
});

const router = useRouter();
const route = useRoute();

const isLoading = ref(false);
const buckets = ref<BucketEntity[]>([]);
const jsonError = ref<string>('');

async function handleStep1Next(activateCallback: (step: string) => void) {
  if (RemoteDriver.includes(initialValues.driver)) {
    isLoading.value = true;
    jsonError.value = '';

    try {
      let auth: Auth;

      if (initialValues.driver === Driver.gcs) {
        // Parse and validate service account JSON
        try {
          const serviceAccount = JSON.parse(initialValues.serviceAccountJson);

          if (!serviceAccount.client_email || !serviceAccount.private_key) {
            jsonError.value =
              'Invalid service account JSON: missing client_email or private_key';
            return;
          }

          // Use the entire service account JSON as GcsAuth (already in correct format)
          auth = serviceAccount;
        } catch (error: any) {
          jsonError.value = 'Invalid JSON format: ' + error.message;
          return;
        }
      } else if (initialValues.driver === Driver.s3) {
        auth = {
          accessKey: initialValues.accessKey,
          secretKey: initialValues.secretKey,
        };
      } else {
        throw new Error('Unsupported driver');
      }

      const result = await sessionStore.listBuckets(initialValues.driver, auth);
      buckets.value = result;
      activateCallback('2');
    } catch (error) {
      // Error handled by store toast
    } finally {
      isLoading.value = false;
    }
  }
}

function handleFinish() {
  let auth: Auth;

  if (initialValues.driver === Driver.gcs) {
    // Parse service account JSON and use it directly
    auth = JSON.parse(initialValues.serviceAccountJson);
  } else if (initialValues.driver === Driver.s3) {
    auth = {
      accessKey: initialValues.accessKey,
      secretKey: initialValues.secretKey,
    };
  } else {
    throw new Error('Unsupported driver');
  }

  const session = SessionEntity.new({
    name: initialValues.name,
    description: initialValues.description,
    driver: initialValues.driver,
    mount: initialValues.mount,
    auth,
    metadataPath: initialValues.metadataPath,
  });

  sessionStore.add(session);
  emits('update:visible', false);

  router.push({
    path: EntityPath.fromRoute({ sessionId: session.id, mount: '/' }).toRoute(),
  });
}
</script>

<template>
  <Dialog
    :visible="visible"
    @update:visible="(val: boolean) => {
        if (!isLoading) emits('update:visible', val)
      }"
    header="New Session"
    modal
    pt:root:class="w-md"
  >
    <Form :initialValues="initialValues">
      <Stepper value="1">
        <StepList>
          <Step :disabled="isLoading" value="1">Credentials</Step>
          <Step :disabled="isLoading" value="2">Bucket</Step>
          <Step :disabled="isLoading" value="3">Details</Step>
        </StepList>
        <StepPanels>
          <StepPanel v-slot="{ activateCallback }" value="1">
            <div class="flex flex-col gap-4">
              <Select
                v-model="initialValues.driver"
                name="session"
                :options="[
                  { label: 'Google Cloud Storage', value: 'gcs' },
                  { label: 'AWS S3', value: 's3' },
                ]"
                optionLabel="label"
                optionValue="value"
                placeholder="Select Driver..."
                :disabled="isLoading"
                fluid
              />

              <!-- GCS: Service Account JSON -->
              <div
                v-if="initialValues.driver === 'gcs'"
                class="flex flex-col gap-4"
              >
                <FloatLabel variant="on">
                  <Textarea
                    id="serviceAccountJson"
                    v-model="initialValues.serviceAccountJson"
                    name="serviceAccountJson"
                    fluid
                    rows="10"
                    :class="{ 'border-red-500': jsonError }"
                  />
                  <label for="serviceAccountJson">Service Account JSON</label>
                </FloatLabel>
                <Message v-if="jsonError" severity="error" :closable="false">
                  {{ jsonError }}
                </Message>
              </div>

              <!-- S3: Access Key + Secret Key -->
              <div
                v-else-if="initialValues.driver === 's3'"
                class="flex flex-col gap-4"
              >
                <FloatLabel variant="on">
                  <InputText
                    v-model="initialValues.accessKey"
                    name="accessKey"
                    fluid
                    id="accessKey"
                  />
                  <label for="accessKey">Access Key</label>
                </FloatLabel>

                <FloatLabel variant="on">
                  <InputText
                    v-model="initialValues.secretKey"
                    name="secretKey"
                    type="password"
                    fluid
                    id="secretKey"
                  />
                  <label for="secretKey">Secret Key</label>
                </FloatLabel>
              </div>

              <div class="flex pt-2 justify-end">
                <Button
                  :loading="isLoading"
                  :disabled="!initialValues.driver"
                  label="Next"
                  icon="pi pi-arrow-right"
                  iconPos="right"
                  @click="handleStep1Next(activateCallback)"
                />
              </div>
            </div>
          </StepPanel>
          <StepPanel v-slot="{ activateCallback }" value="2">
            <div class="flex flex-col gap-4">
              <Listbox
                v-model="initialValues.mount"
                name="bucket"
                :options="buckets"
                optionLabel="name"
                optionValue="name"
                class="w-full"
                :disabled="isLoading"
              />
            </div>
            <div class="flex pt-6 justify-between">
              <Button
                label="Back"
                severity="secondary"
                icon="pi pi-arrow-left"
                @click="activateCallback('1')"
              />
              <Button
                label="Next"
                :disabled="!initialValues.mount"
                icon="pi pi-arrow-right"
                iconPos="right"
                @click="activateCallback('3')"
              />
            </div>
          </StepPanel>
          <StepPanel v-slot="{ activateCallback }" value="3">
            <div class="flex flex-col gap-4">
              <FloatLabel variant="on">
                <InputText
                  v-model="initialValues.name"
                  name="name"
                  fluid
                  id="sessionName"
                />
                <label for="sessionName">Session Name</label>
              </FloatLabel>
              <FloatLabel variant="on">
                <InputText
                  v-model="initialValues.metadataPath"
                  name="metadataPath"
                  fluid
                  id="metadataPath"
                />
                <label for="metadataPath">Metadata Path</label>
              </FloatLabel>
              <FloatLabel variant="on">
                <Textarea
                  v-model="initialValues.description"
                  name="description"
                  fluid
                  id="sessionDesc"
                  rows="3"
                />
                <label for="sessionDesc">Description (Optional)</label>
              </FloatLabel>
            </div>
            <div class="flex pt-6 justify-between">
              <Button
                label="Back"
                severity="secondary"
                icon="pi pi-arrow-left"
                @click="activateCallback('2')"
              />
              <Button
                label="Create Session"
                severity="success"
                icon="pi pi-check"
                @click="handleFinish"
              />
            </div>
          </StepPanel>
        </StepPanels>
      </Stepper>
    </Form>
  </Dialog>
</template>
