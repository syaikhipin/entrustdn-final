<script setup lang="ts">
// Registration: pick a role, accept the current TOS version, create the
// account. The verification link lands in the backend's dev log sink.
import { errFrom, fetchCurrentTos, register, type PublicAccount } from "~/auth";

const backendURL = useBackendURL();

const email = ref("");
const password = ref("");
const displayName = ref("");
const role = ref("data_consumer");
const tos = ref<{ version: string; body: string } | null>(null);
const accepted = ref(false);
const error = ref<string | null>(null);
const created = ref<PublicAccount | null>(null);
const busy = ref(false);

onMounted(async () => {
  try {
    tos.value = await fetchCurrentTos(backendURL);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
});

async function submit() {
  if (!tos.value || !accepted.value) {
    error.value = "You must accept the Terms of Service to register.";
    return;
  }
  busy.value = true;
  error.value = null;
  try {
    created.value = await register(backendURL, {
      email: email.value,
      password: password.value,
      displayName: displayName.value,
      role: role.value,
      tosVersion: tos.value.version,
    });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section v-if="created" class="card">
    <h2>Check the server log</h2>
    <p>
      Account created for <strong>{{ created.email }}</strong
      >. In this pilot the verification email is not sent — the link lands in
      the backend's server log (dev mail sink).
    </p>
    <p v-if="created.role === 'farmer_organization'" class="hint">
      As a Farmer Organization, your application is also awaiting Platform
      Admin approval after you verify.
    </p>
    <p class="hint">
      Copy the <code>/verify?token=…</code> link from
      <code>/tmp/thresh-backend.log</code>, open it, then log in.
    </p>
    <NuxtLink to="/login">Go to log in →</NuxtLink>
  </section>

  <section v-else class="card">
    <h2>Create your account</h2>
    <form @submit.prevent="submit">
      <label>
        I am a…
        <select v-model="role">
          <option value="data_consumer">Data Consumer — I want to find or commission data</option>
          <option value="farmer_organization">Farmer Organization — I want to share my members' data</option>
        </select>
      </label>
      <label>
        Display name
        <input v-model="displayName" type="text" required placeholder="e.g. Irish Dairy Co-op" />
      </label>
      <label>
        Email
        <input v-model="email" type="email" required placeholder="you@example.org" />
      </label>
      <label>
        Password <span class="hint">(at least 8 characters)</span>
        <input v-model="password" type="password" required minlength="8" />
      </label>

      <div v-if="tos" class="tos">
        <h3>Terms of Service — version {{ tos.version }}</h3>
        <pre class="tos-body">{{ tos.body }}</pre>
        <label class="accept">
          <input v-model="accepted" type="checkbox" />
          I accept version {{ tos.version }} of the Terms of Service. The
          accepted version and time are recorded on my account.
        </label>
      </div>
      <p v-else-if="!error" class="hint">Loading Terms of Service…</p>

      <p v-if="error" class="error-text">{{ error }}</p>
      <button class="primary" type="submit" :disabled="busy || !tos">
        {{ busy ? "Creating…" : "Create account" }}
      </button>
    </form>
    <p class="hint">
      Already registered? <NuxtLink to="/login">Log in</NuxtLink>
    </p>
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
  max-height: 10rem;
  overflow-y: auto;
  margin: 0.5rem 0;
}
.accept {
  display: flex;
  gap: 0.5rem;
  align-items: flex-start;
}
.accept input {
  width: auto;
  margin-top: 0.2rem;
}
</style>
