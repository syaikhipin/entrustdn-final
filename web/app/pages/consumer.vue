<script setup lang="ts">
// Data Consumer home: the catalog and Requests arrive in later tickets. For
// now: identity, the provable TOS record, and the credits placeholder.
import { useSession } from "~/composables/useSession";

const { me, account, restore } = useSession();

const ready = ref(false);

onMounted(async () => {
  await restore();
  ready.value = true;
});

// The guard waits for restore(): before it finishes, a refreshing user has
// a token but no parsed identity, and bouncing then would strand them at
// /login. Only a completed restore with no account is truly signed out.
watch(ready, (isReady) => {
  if (isReady && !account.value) navigateTo("/login");
});
</script>

<template>
  <div>
    <section v-if="!ready" class="card">
      <p class="hint">Loading…</p>
    </section>

    <template v-else-if="me">
      <section class="card">
        <h2>Data Consumer home</h2>
        <p>
          Welcome, <strong>{{ me.account.displayName }}</strong
          >. The catalog, Requests, and your spend dashboard arrive in the next
          slices — this is your role's home.
        </p>
        <p class="hint">Credits: 0 (top-ups land with the Payment Gateway ticket)</p>
      </section>

      <section v-if="me.tos" class="card">
        <h3>Your agreement</h3>
        <p class="hint">
          You accepted Terms of Service version
          <strong>{{ me.tos.acceptedVersion }}</strong> on
          {{ new Date(me.tos.acceptedAt).toLocaleString() }}.
        </p>
      </section>
    </template>
  </div>
</template>
