<script setup lang="ts">
// Login: check credentials, store the session, forward to the
// role-appropriate home (the router sends TOS-flagged sessions to /tos).
import { errFrom, fetchMe, homeRoute, login } from "~/auth";

const backendURL = useBackendURL();
const { signIn } = useSession();

const email = ref("");
const password = ref("");
const error = ref<string | null>(null);
const busy = ref(false);

async function submit() {
  busy.value = true;
  error.value = null;
  try {
    const { token, requiresTosAcceptance } = await login(backendURL, email.value, password.value);
    // /me completes the identity (TOS record included) before routing.
    const me = await fetchMe(backendURL, token);
    signIn(token, { ...me, requiresTosAcceptance });
    navigateTo(requiresTosAcceptance ? "/tos" : `/${homeRoute(me.account, false)}`);
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="card">
    <h2>Log in</h2>
    <form @submit.prevent="submit">
      <label>
        Email
        <input v-model="email" type="email" required autocomplete="email" />
      </label>
      <label>
        Password
        <input v-model="password" type="password" required autocomplete="current-password" />
      </label>
      <p v-if="error" class="error-text">{{ error }}</p>
      <button class="primary" type="submit" :disabled="busy">
        {{ busy ? "Checking…" : "Log in" }}
      </button>
    </form>
    <p class="hint">
      New here? <NuxtLink to="/register">Create an account</NuxtLink>
    </p>
  </section>
</template>
