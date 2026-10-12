<script setup lang="ts">
// Catalog: browse all Farmer Organizations' shared data assets with
// taxonomy facets, filtering, and pricing. Data Consumers commission
// assets into Requests from here.
import { listAssets, formatBytes, formatDate, type Asset } from "~/assets";
import { useSession } from "~/composables/useSession";

const backendURL = useBackendURL();
const { token, restore } = useSession();

const ready = ref(false);
const assets = ref<Asset[]>([]);
const error = ref<string | null>(null);
const filterCrop = ref("");
const filterRegion = ref("");
const search = ref("");

onMounted(async () => {
  await restore();
  ready.value = true;
  if (token.value) await load();
});

async function load() {
  if (!token.value) return;
  try {
    assets.value = await listAssets(backendURL, token.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : "Failed to load catalog";
  }
}

const filtered = computed(() => {
  return assets.value.filter((a) => {
    if (filterCrop.value && !(a.taxonomy?.crop || "").toLowerCase().includes(filterCrop.value.toLowerCase())) return false;
    if (filterRegion.value && !(a.taxonomy?.region || "").toLowerCase().includes(filterRegion.value.toLowerCase())) return false;
    if (search.value && !(a.name || "").toLowerCase().includes(search.value.toLowerCase())) return false;
    return true;
  });
});
</script>

<template>
  <div class="mx-auto max-w-6xl px-4 py-8">
    <h1 class="text-3xl font-bold mb-2">Data Catalog</h1>
    <p class="text-gray-600 mb-6">Browse shared agricultural datasets from all Farmer Organizations. Filter by taxonomy, view pricing, commission into Requests.</p>

    <div v-if="!ready" class="text-gray-500">Loading…</div>
    <div v-else>
      <!-- Filters -->
      <div class="flex flex-wrap gap-4 mb-6 p-4 bg-gray-50 rounded-lg">
        <input v-model="search" placeholder="Search datasets…" class="px-3 py-2 border rounded" />
        <input v-model="filterCrop" placeholder="Filter by crop (e.g. wheat)" class="px-3 py-2 border rounded" />
        <input v-model="filterRegion" placeholder="Filter by region" class="px-3 py-2 border rounded" />
        <span class="text-sm text-gray-500 self-center">{{ filtered.length }} of {{ assets.length }} datasets</span>
      </div>

      <div v-if="error" class="text-red-600 mb-4">{{ error }}</div>

      <!-- Asset grid -->
      <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        <div v-for="a in filtered" :key="a.id" class="border rounded-lg p-4 hover:shadow-lg transition">
          <h3 class="font-semibold text-lg mb-1">{{ a.name }}</h3>
          <p class="text-sm text-gray-500 mb-2">{{ formatBytes(a.size_bytes) }} · {{ formatDate(a.created_at) }}</p>
          <div v-if="a.taxonomy" class="flex flex-wrap gap-1 mb-2">
            <span v-if="a.taxonomy.crop" class="text-xs bg-green-100 text-green-800 px-2 py-1 rounded">{{ a.taxonomy.crop }}</span>
            <span v-if="a.taxonomy.region" class="text-xs bg-blue-100 text-blue-800 px-2 py-1 rounded">{{ a.taxonomy.region }}</span>
            <span v-if="a.taxonomy.growth_stage" class="text-xs bg-yellow-100 text-yellow-800 px-2 py-1 rounded">{{ a.taxonomy.growth_stage }}</span>
            <span v-if="a.taxonomy.data_type" class="text-xs bg-purple-100 text-purple-800 px-2 py-1 rounded">{{ a.taxonomy.data_type }}</span>
          </div>
          <div class="flex justify-between items-center mt-3">
            <span class="font-bold text-green-700">{{ a.price_credits }} credits</span>
            <NuxtLink to="/requests" class="text-sm text-blue-600 hover:underline">Commission →</NuxtLink>
          </div>
        </div>
      </div>

      <div v-if="filtered.length === 0 && !error" class="text-center text-gray-500 py-12">
        No datasets match your filters.
      </div>
    </div>
  </div>
</template>
