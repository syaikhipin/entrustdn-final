<script setup lang="ts">
// Farmer Organization home. The shared-data dashboard arrives with ticket
// 04; a pending or rejected application shows its state instead.
import { useSession } from "~/composables/useSession";

const { me, account, restore } = useSession();

onMounted(() => restore());

watch(
  () => account.value,
  (acct) => {
    if (!acct) navigateTo("/login");
  },
  { immediate: true },
);
</script>

<template>
  <div>
    <section v-if="account?.status === 'pending_approval'" class="card">
      <h2>Application pending</h2>
      <p>
        <strong>{{ account.displayName }}</strong> is registered, but a
        Platform Admin has not yet approved your organization. You'll see the
        shared-data dashboard here once an admin approves the application.
      </p>
      <p class="hint">
        Approvals happen out-of-band for now — the Platform Admin sees your
        application in their queue.
      </p>
    </section>

    <section v-else-if="account?.status === 'rejected'" class="card">
      <h2>Application declined</h2>
      <p class="error-text">
        A Platform Admin declined this organization's application. Contact
        the platform operators if you believe this is a mistake.
      </p>
    </section>

    <template v-else-if="me">
      <section class="card">
        <h2>Farmer Organization home</h2>
        <p>
          Welcome, <strong>{{ me.account.displayName }}</strong
          >. The shared-data dashboard — upload, anonymization status,
          update/delete — arrives in the next slice.
        </p>
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
