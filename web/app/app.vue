<script setup lang="ts">
const config = useRuntimeConfig();
const backendURL =
  config.public.backendBaseURL || "http://localhost:8080";

const status = ref<import("./status").BackendStatus | null>(null);
const error = ref<string | null>(null);

onMounted(async () => {
  try {
    const { fetchStatus } = await import("./status");
    status.value = await fetchStatus(backendURL);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});
</script>

<template>
  <main class="wrap">
    <h1>Thresh</h1>
    <p class="tagline">
      Agentic agricultural data sharing — platform status
    </p>

    <section v-if="error" class="card error">
      <h2>Backend unreachable</h2>
      <p>{{ error }}</p>
      <p class="hint">Is the backend running on {{ backendURL }}?</p>
    </section>

    <section v-else-if="status" class="card" :class="status.status">
      <h2>
        Backend:
        <span class="pill" :class="status.status">{{ status.status }}</span>
      </h2>
      <dl>
        <dt>Service</dt>
        <dd>{{ status.service }}</dd>
        <dt>Backend version</dt>
        <dd>{{ status.version }}</dd>
        <dt>Agent</dt>
        <dd>
          <span v-if="status.agentReachable">
            reachable (v{{ status.agentVersion }})
          </span>
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
  </main>
</template>

<style scoped>
.wrap {
  max-width: 40rem;
  margin: 3rem auto;
  padding: 0 1rem;
  font-family: system-ui, -apple-system, sans-serif;
}
.tagline {
  color: #5b6350;
  margin-bottom: 2rem;
}
.card {
  border: 1px solid #d8dcd2;
  border-radius: 0.5rem;
  padding: 1.25rem 1.5rem;
}
.card.error {
  border-color: #c44536;
}
.pill {
  display: inline-block;
  padding: 0.1rem 0.6rem;
  border-radius: 999px;
  font-size: 0.85rem;
  font-weight: 600;
}
.pill.ok {
  background: #e4efe0;
  color: #2f5d28;
}
.pill.degraded {
  background: #fdf0d8;
  color: #8a5a00;
}
dl {
  display: grid;
  grid-template-columns: 9rem 1fr;
  row-gap: 0.4rem;
  margin: 1rem 0;
}
dt {
  color: #5b6350;
}
.hint {
  color: #7a8171;
  font-size: 0.85rem;
}
</style>
