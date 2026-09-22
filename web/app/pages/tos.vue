<script setup lang="ts">
// TOS re-acceptance interstitial: a session flagged at login lands here and
// stays until it accepts the current version (story 8).
import { acceptTos, errFrom, fetchCurrentTos, fetchMe } from "~/auth";

const backendURL = useBackendURL();
const { token, me, refresh, restore } = useSession();

const tos = ref<{ version: string; body: string } | null>(null);
const error = ref<string | null>(null);
const busy = ref(false);

onMounted(async () => {
  await restore();
  if (!token.value) {
    navigateTo("/login");
    return;
  }
  try {
    tos.value = await fetchCurrentTos(backendURL);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});

async function accept() {
  if (!tos.value || !token.value) return;
  busy.value = true;
  error.value = null;
  try {
    await acceptTos(backendURL, token.value, tos.value.version);
    // Re-pull identity: the flag is now cleared server-side.
    refresh(await fetchMe(backendURL, token.value));
    navigateTo(`/${useSession().home.value}`);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="card">
    <h2>Terms of Service have changed</h2>
    <p v-if="me?.tos" class="hint">
      You accepted version {{ me.tos.acceptedVersion }} on
      {{ new Date(me.tos.acceptedAt).toLocaleString() }}. The platform now
      operates under a newer version — please review and accept it to
      continue.
    </p>

    <div v-if="tos" class="tos">
      <h3>Version {{ tos.version }}</h3>
      <pre class="tos-body">{{ tos.body }}</pre>
    </div>

    <p v-if="error" class="error-text">{{ error }}</p>
    <button class="primary" :disabled="busy || !tos" @click="accept">
      {{ busy ? "Recording…" : `Accept version ${tos?.version ?? ""}` }}
    </button>
  </section>
</template>

<style scoped>
.tos {
  border: 1px solid var(--line);
  border-radius: 0.35rem;
  padding: 0.9rem;
  margin: 1rem 0;
  background: var(--bg);
}
.tos-body {
  white-space: pre-wrap;
  font: inherit;
  color: var(--muted);
  max-height: 14rem;
  overflow-y: auto;
  margin: 0.5rem 0 0;
}
</style>
