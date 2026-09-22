<script setup lang="ts">
// Platform Admin home: the Farmer Organization application queue
// (approve/reject) and TOS publishing.
import { decideApplication, errFrom, listApplications, publishTos, type Application } from "~/auth";
import { useSession } from "~/composables/useSession";

const backendURL = useBackendURL();
const { token, account, restore } = useSession();

const applications = ref<Application[]>([]);
const error = ref<string | null>(null);
const notice = ref<string | null>(null);
const busyId = ref<string | null>(null);

// TOS publishing form.
const tosVersion = ref("");
const tosBody = ref("");
const tosNotice = ref<string | null>(null);
const tosError = ref<string | null>(null);

onMounted(() => restore());

watch(
  () => account.value,
  (acct) => {
    if (!acct) navigateTo("/login");
    else if (acct.role === "platform_admin") load();
  },
  { immediate: true },
);

async function load() {
  if (!token.value) return;
  try {
    applications.value = await listApplications(backendURL, token.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

async function decide(app: Application, decision: "approve" | "reject") {
  if (!token.value) return;
  busyId.value = app.id;
  error.value = null;
  notice.value = null;
  try {
    await decideApplication(backendURL, token.value, app.id, decision);
    notice.value =
      decision === "approve"
        ? `Approved ${app.displayName} — they can now trade on the platform.`
        : `Declined ${app.displayName}.`;
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  } finally {
    busyId.value = null;
  }
}

async function publish() {
  if (!token.value) return;
  tosError.value = null;
  tosNotice.value = null;
  try {
    await publishTos(backendURL, token.value, tosVersion.value.trim(), tosBody.value.trim());
    tosNotice.value = `Version ${tosVersion.value.trim()} published — every account re-accepts at next login.`;
    tosVersion.value = "";
    tosBody.value = "";
  } catch (e) {
    tosError.value = e instanceof Error ? e.message : errFrom(e);
  }
}
</script>

<template>
  <div>
    <section class="card">
      <h2>Platform Admin</h2>
      <p class="hint">
        Applications and contract versions today; taxonomy, modules, and
        platform stats arrive in later slices.
      </p>
      <p v-if="error" class="error-text">{{ error }}</p>
      <p v-if="notice" class="ok-text">{{ notice }}</p>
    </section>

    <section class="card">
      <h3>Farmer Organization applications</h3>
      <p v-if="applications.length === 0" class="hint">No pending applications.</p>
      <ul v-else class="queue">
        <li v-for="app in applications" :key="app.id" class="row">
          <div>
            <strong>{{ app.displayName }}</strong>
            <div class="hint">{{ app.email }} · applied {{ new Date(app.createdAt).toLocaleDateString() }}</div>
          </div>
          <div class="row-actions">
            <button class="primary" :disabled="busyId === app.id" @click="decide(app, 'approve')">Approve</button>
            <button class="danger" :disabled="busyId === app.id" @click="decide(app, 'reject')">Reject</button>
          </div>
        </li>
      </ul>
    </section>

    <section class="card">
      <h3>Publish a new Terms of Service version</h3>
      <p class="hint">
        Publishing makes the version current: every account that accepted an
        older one must re-accept at next login. Versions are immutable once
        published.
      </p>
      <form @submit.prevent="publish">
        <label>
          Version
          <input v-model="tosVersion" type="text" required placeholder="e.g. 1.1" />
        </label>
        <label>
          Terms text
          <textarea v-model="tosBody" rows="5" required placeholder="The full contract text…"></textarea>
        </label>
        <p v-if="tosError" class="error-text">{{ tosError }}</p>
        <p v-if="tosNotice" class="ok-text">{{ tosNotice }}</p>
        <button class="primary" type="submit">Publish version</button>
      </form>
    </section>
  </div>
</template>

<style scoped>
.queue {
  list-style: none;
  margin: 0;
  padding: 0;
}
.row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 1rem;
  padding: 0.75rem 0;
  border-bottom: 1px solid var(--line);
}
.row:last-child {
  border-bottom: 0;
}
.row-actions {
  display: flex;
  gap: 0.5rem;
  flex-shrink: 0;
}
</style>
