<script setup lang="ts">
// The tracer-bullet status view from ticket 01, kept as its own page.
import { fetchStatus } from "~/status";

const config = useRuntimeConfig();
const backendURL = config.public.backendBaseURL || "http://localhost:8080";

const status = ref<import("~/status").BackendStatus | null>(null);
const error = ref<string | null>(null);

onMounted(async () => {
  try {
    status.value = await fetchStatus(backendURL);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});
</script>

<template>
  <section v-if="error" class="card error">
    <h2>Backend unreachable</h2>
    <p>{{ error }}</p>
    <p class="hint">Is the backend running on {{ backendURL }}?</p>
  </section>

  <section v-else-if="status" class="card" :class="status.status">
    <h2>
      Backend:
      <span class="pill" :class="status.status === 'ok' ? 'ok' : 'warn'">{{ status.status }}</span>
    </h2>
    <dl class="grid">
      <dt>Service</dt>
      <dd>{{ status.service }}</dd>
      <dt>Backend version</dt>
      <dd>{{ status.version }}</dd>
      <dt>Agent</dt>
      <dd>
        <span v-if="status.agentReachable">reachable (v{{ status.agentVersion }})</span>
        <span v-else>unreachable</span>
      </dd>
    </dl>
    <p class="hint">
      This page fetched the backend's status live; the agent round trip ran
      through the Go↔Agent contract.
    </p>
  </section>

  <section v-else class="card">
    <h2>Checking backend…</h2>
  </section>
</template>

<style scoped>
dl.grid {
  display: grid;
  grid-template-columns: 10rem 1fr;
  row-gap: 0.4rem;
}
dt {
  color: var(--muted);
}
</style>
