<script setup lang="ts">
// Ledger: credit balance, movement history with double-entry bookkeeping,
// revenue share breakdown. Shows exactly how credits flow.
import { fetchMyCredits, formatCredits, type CreditsView } from "~/credits";
import { useSession } from "~/composables/useSession";

const backendURL = useBackendURL();
const { token, restore } = useSession();

const ready = ref(false);
const credits = ref<CreditsView | null>(null);
const error = ref<string | null>(null);

onMounted(async () => {
  await restore();
  ready.value = true;
  if (token.value) await load();
});

async function load() {
  if (!token.value) return;
  try {
    credits.value = await fetchMyCredits(backendURL, token.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : "Failed to load ledger";
  }
}
</script>

<template>
  <div class="mx-auto max-w-4xl px-4 py-8">
    <h1 class="text-3xl font-bold mb-2">Credits & Ledger</h1>
    <p class="text-gray-600 mb-6">Every credit movement is recorded with double-entry bookkeeping. Revenue share: Farmer Organizations receive 70%, platform retains 30%.</p>

    <div v-if="!ready" class="text-gray-500">Loading…</div>
    <div v-else>
      <div v-if="error" class="text-red-600 mb-4">{{ error }}</div>

      <!-- Balance card -->
      <div v-if="credits" class="bg-gradient-to-r from-green-500 to-emerald-600 text-white rounded-xl p-6 mb-6">
        <div class="text-sm opacity-80">Current Balance</div>
        <div class="text-4xl font-bold">{{ formatCredits(credits.balance_micros) }}</div>
        <div class="text-sm opacity-80 mt-1">credits</div>
      </div>

      <!-- Revenue share explainer -->
      <div class="border rounded-lg p-4 mb-6 bg-blue-50">
        <h3 class="font-semibold mb-2">💰 How Revenue Share Works</h3>
        <div class="text-sm space-y-1">
          <p><strong>Example:</strong> Dataset priced at 150 credits</p>
          <p>→ Farmer Organization receives: <strong class="text-green-700">105 credits (70%)</strong></p>
          <p>→ Platform retains: <strong class="text-blue-700">45 credits (30%)</strong></p>
          <p class="text-gray-600">Agent inference costs are metered separately in micro-credits per Request.</p>
        </div>
      </div>

      <!-- Movement history -->
      <h2 class="text-xl font-semibold mb-3">Movement History</h2>
      <div v-if="credits && credits.movements.length > 0" class="space-y-2">
        <div v-for="m in credits.movements" :key="m.id" class="border rounded p-3 flex justify-between items-center">
          <div>
            <div class="font-medium">{{ m.kind }}</div>
            <div class="text-xs text-gray-500 font-mono" :title="m.id">ID: {{ m.id.slice(0, 8) }}…</div>
          </div>
          <div :class="m.amount_micros >= 0 ? 'text-green-700 font-bold' : 'text-red-700 font-bold'">
            {{ m.amount_micros >= 0 ? '+' : '' }}{{ formatCredits(m.amount_micros) }}
          </div>
        </div>
      </div>
      <div v-else class="text-gray-500">No movements yet.</div>
    </div>
  </div>
</template>
