<script setup lang="ts">
// Memory: long-term agent memory status. Shows configured memory
// providers and explains how the agent recalls/stores context.
import { fetchMemoryProviders, type MemoryProvider } from "~/memory";
import { useSession } from "~/composables/useSession";

const backendURL = useBackendURL();
const { token, restore } = useSession();

const ready = ref(false);
const providers = ref<MemoryProvider[]>([]);
const error = ref<string | null>(null);

onMounted(async () => {
  await restore();
  ready.value = true;
  if (token.value) await load();
});

async function load() {
  if (!token.value) return;
  try {
    providers.value = await fetchMemoryProviders(backendURL, token.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : "Failed to load memory providers";
  }
}
</script>

<template>
  <div class="mx-auto max-w-4xl px-4 py-8">
    <h1 class="text-3xl font-bold mb-2">🧠 Agent Memory</h1>
    <p class="text-gray-600 mb-6">The agent retains conversation context via pluggable MCP Memory Providers. </p>

    <div v-if="!ready" class="text-gray-500">Loading…</div>
    <div v-else>
      <div v-if="error" class="text-red-600 mb-4">{{ error }}</div>

      <!-- How it works -->
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4 mb-6">
        <div class="border rounded-lg p-4 bg-purple-50">
          <h3 class="font-semibold mb-2">📥 Recall (before responding)</h3>
          <p class="text-sm">On every clarify turn, the agent queries long-term memory with your message. Relevant memories are injected into the model's context — so it remembers your preferences across sessions.</p>
          <code class="text-xs bg-gray-100 p-1 rounded mt-2 block">recall(query, limit=5) → [{text, score}]</code>
        </div>
        <div class="border rounded-lg p-4 bg-green-50">
          <h3 class="font-semibold mb-2">📤 Retain (after your message)</h3>
          <p class="text-sm">Your new facts are stored automatically alongside the read — no extra latency. The write never blocks the conversation.</p>
          <code class="text-xs bg-gray-100 p-1 rounded mt-2 block">retain(content) → {status: accepted}</code>
        </div>
      </div>

      <!-- Configured providers -->
      <h2 class="text-xl font-semibold mb-3">Configured Providers</h2>
      <div v-if="providers.length > 0" class="space-y-2">
        <div v-for="p in providers" :key="p.id" class="border rounded-lg p-4 flex justify-between items-center bg-white">
          <div>
            <div class="font-medium">🧠 {{ p.name }}</div>
            <div class="text-sm text-gray-500 font-mono">{{ p.endpoint }}</div>
          </div>
          <span class="text-xs bg-green-100 text-green-800 px-3 py-1 rounded-full">Active</span>
        </div>
      </div>
      <div v-else class="text-gray-500 border rounded-lg p-4">
        No memory providers configured. An admin can register one via the admin API.
      </div>

      <!-- Degradation note -->
      <div class="mt-6 text-sm text-gray-600 border-l-4 border-yellow-400 pl-4">
        <strong>Graceful degradation:</strong> If the memory service is down or slow, the agent continues without memory — recall returns empty, never breaks the turn.
      </div>
    </div>
  </div>
</template>
