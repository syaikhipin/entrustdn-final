<script setup lang="ts">
// Email verification landing page: the dev log sink's link points here with
// the one-shot token; this page consumes it against the backend.
import { errFrom, verify } from "~/auth";

const route = useRoute();
const backendURL = useBackendURL();

const state = ref<"working" | "done" | "error">("working");
const error = ref<string | null>(null);

onMounted(async () => {
  const token = String(route.query.token ?? "");
  if (!token) {
    state.value = "error";
    error.value = "This page needs a verification token — use the link from the server log.";
    return;
  }
  try {
    await verify(backendURL, token);
    state.value = "done";
  } catch (e) {
    state.value = "error";
    error.value = e instanceof Error ? e.message : String(e);
  }
});
</script>

<template>
  <section class="card">
    <template v-if="state === 'working'">
      <h2>Verifying…</h2>
    </template>
    <template v-else-if="state === 'done'">
      <h2>Email verified</h2>
      <p class="ok-text">Your address is confirmed — you can log in now.</p>
      <NuxtLink to="/login">Go to log in →</NuxtLink>
    </template>
    <template v-else>
      <h2>Verification failed</h2>
      <p class="error-text">{{ error }}</p>
      <p class="hint">
        Links are single-use and expire after 48 hours. Register again to
        receive a fresh one (it will land in the server log).
      </p>
      <NuxtLink to="/register">Back to registration →</NuxtLink>
    </template>
  </section>
</template>
