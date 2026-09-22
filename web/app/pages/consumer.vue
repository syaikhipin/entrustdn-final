<script setup lang="ts">
// Data Consumer home: the catalog and Requests arrive in later tickets. For
// now: identity, the provable TOS record, and the credits placeholder.
import { useSession } from "~/composables/useSession";

const { me, restore } = useSession();

onMounted(() => restore());

// Route guards run after restore; unauthenticated visitors go to login.
watch(
  () => useSession().account.value,
  (acct) => {
    if (!acct) navigateTo("/login");
  },
  { immediate: true },
);
</script>

<template>
  <div>
    <section class="card">
      <h2>Data Consumer home</h2>
      <p v-if="me">
        Welcome, <strong>{{ me.account.displayName }}</strong
        >. The catalog, Requests, and your spend dashboard arrive in the next
        slices — this is your role's home.
      </p>
      <p class="hint">Credits: 0 (top-ups land with the Payment Gateway ticket)</p>
    </section>

    <section v-if="me?.tos" class="card">
      <h3>Your agreement</h3>
      <p class="hint">
        You accepted Terms of Service version
        <strong>{{ me.tos.acceptedVersion }}</strong> on
        {{ new Date(me.tos.acceptedAt).toLocaleString() }}.
      </p>
    </section>
  </div>
</template>
