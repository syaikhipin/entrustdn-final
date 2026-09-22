<script setup lang="ts">
// Landing page: signed-out visitors see the pitch and entry points; a
// signed-in session is forwarded to its role-appropriate home.
import { fetchStatus } from "~/status";

const { home, restore } = useSession();

onMounted(async () => {
  await restore();
  if (home.value) navigateTo(`/${home.value}`);
});

const backendURL = useBackendURL();
const status = ref<import("~/status").BackendStatus | null>(null);
const statusError = ref<string | null>(null);

onMounted(async () => {
  try {
    status.value = await fetchStatus(backendURL);
  } catch (e) {
    statusError.value = e instanceof Error ? e.message : String(e);
  }
});

watch(home, (h) => {
  if (h) navigateTo(`/${h}`);
});
</script>

<template>
  <div>
    <section v-if="home" class="card">
      <p class="hint">Taking you to your home…</p>
    </section>

    <section v-else class="card hero">
      <h1>Thresh</h1>
      <p class="tagline">
        Agentic agricultural data sharing. Farmer Organizations share data
        under clear terms; Data Consumers find it or commission new
        collections — gathered by an agent, anonymized by design.
      </p>
      <div class="actions">
        <NuxtLink to="/register" class="btn primary">Create an account</NuxtLink>
        <NuxtLink to="/login" class="btn">Log in</NuxtLink>
      </div>
    </section>

    <section class="card">
      <h2>Platform status</h2>
      <p v-if="statusError" class="error-text">{{ statusError }}</p>
      <template v-else-if="status">
        <p>
          Backend:
          <span class="pill" :class="status.status === 'ok' ? 'ok' : 'warn'">{{ status.status }}</span>
          <span v-if="status.agentReachable"> · agent v{{ status.agentVersion }}</span>
          <span v-else> · agent unreachable</span>
        </p>
        <NuxtLink to="/status" class="hint">Full status detail →</NuxtLink>
      </template>
      <p v-else class="hint">Checking…</p>
    </section>
  </div>
</template>

<style scoped>
.tagline {
  color: var(--muted);
  max-width: 34rem;
}
.actions {
  display: flex;
  gap: 0.75rem;
  margin-top: 1.25rem;
}
.btn {
  display: inline-block;
  padding: 0.5rem 1.2rem;
  border-radius: 0.35rem;
  border: 1px solid var(--line);
  background: #fff;
  color: var(--ink);
  text-decoration: none;
  font-weight: 600;
}
.btn.primary {
  background: var(--accent);
  border-color: var(--accent);
  color: #fff;
}
</style>
