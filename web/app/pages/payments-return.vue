<script setup lang="ts">
// The payment gateway's return leg (ticket 09): after checkout — paid or
// cancelled — the gateway redirects here with ?status=. The settlement
// itself happens server-side through the signed webhook; this page only
// tells the consumer what likely happened and walks them back to their
// wallet, where a refresh shows the truth from the Ledger. It never claims
// settlement: the webhook is the only authority.
const status = computed(() => {
  const s = useRoute().query.status;
  return s === "cancelled" ? "cancelled" : "success";
});
</script>

<template>
  <div>
    <section class="card">
      <h3>Payment {{ status === "cancelled" ? "cancelled" : "received" }}</h3>
      <p v-if="status === 'cancelled'" class="hint">
        The checkout was cancelled — nothing was charged and no credits were
        added. You can start again from your wallet whenever you like.
      </p>
      <p v-else class="hint">
        Thanks — your payment is on its way. The credits appear in your
        balance once the gateway confirms the payment (usually moments).
      </p>
      <button class="primary" type="button" @click="navigateTo('/consumer')">
        Back to your wallet
      </button>
    </section>
  </div>
</template>
