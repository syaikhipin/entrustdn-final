<script setup lang="ts">
// Data Consumer home: identity, the provable TOS record, and the spend
// view — balance and entry history derived from the Ledger (ticket 03).
// Ticket 06 adds the catalog: every shared asset with its taxonomy
// categories, facet filtering, and the cached price from the current price
// book. Requests (buying) arrive in later tickets.
import { errFrom, fetchMyCredits, formatCredits, formatMicros, type CreditsView } from "~/credits";
import {
  fetchMyTopUps,
  formatMinor,
  initiateTopUp,
  topUpStatusLabel,
  type TopUp,
} from "~/payments";
import { formatBytes, formatDate } from "~/assets";
import {
  downloadDelivery,
  listConsumerCollections,
  statusLabel,
  type Collection,
} from "~/collections";
import {
  fetchCatalog,
  formatConfidence,
  TAXONOMY_CATEGORIES,
  type Catalog,
  type TaxonomyCategory,
} from "~/taxonomy";
import { useSession } from "~/composables/useSession";

const backendURL = useBackendURL();
const { token, me, account, restore } = useSession();

const ready = ref(false);
const credits = ref<CreditsView | null>(null);
const error = ref<string | null>(null);

// Collections state (ticket 12): what's being gathered in the consumer's
// name, and the delivery downloads.
const collections = ref<Collection[]>([]);
const collectionsError = ref<string | null>(null);
const downloading = ref<string | null>(null);

// Top-up state (ticket 09): the wallet refill. The preset amounts are cents;
// settling happens at the gateway — after paying, the consumer returns and
// refresh sees the settled top-up in their ledger.
const topups = ref<TopUp[]>([]);
const topupAmount = ref<number>(2500);
const topupBusy = ref(false);
const topupError = ref<string | null>(null);
const topupNotice = ref<string | null>(null);
const TOPUP_PRESETS = [1000, 2500, 5000, 10000];

// Catalog state (ticket 06). Facet selection is one term per axis; the
// consumer narrows the catalog client-side against the whole-catalog facet
// counts.
const catalog = ref<Catalog | null>(null);
const catalogError = ref<string | null>(null);
const picked = ref<Partial<Record<TaxonomyCategory, string>>>({});
const query = ref("");

onMounted(async () => {
  await restore();
  ready.value = true;
  if (token.value) {
    await load();
    await loadCatalog();
    await loadCollections();
    await loadTopUps();
  }
});

// The guard waits for restore(): before it finishes, a refreshing user has
// a token but no parsed identity, and bouncing then would strand them at
// /login. Only a completed restore with no account is truly signed out.
watch(ready, (isReady) => {
  if (isReady && !account.value) navigateTo("/login");
});

async function load() {
  if (!token.value) return;
  try {
    credits.value = await fetchMyCredits(backendURL, token.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  }
}

async function loadCatalog() {
  if (!token.value) return;
  catalogError.value = null;
  try {
    catalog.value = await fetchCatalog(backendURL, token.value);
  } catch (e) {
    catalogError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// loadCollections brings the consumer's collections — the gathering
// efforts running in their name (ticket 12).
async function loadCollections() {
  if (!token.value) return;
  collectionsError.value = null;
  try {
    collections.value = await listConsumerCollections(backendURL, token.value);
  } catch (e) {
    collectionsError.value = e instanceof Error ? e.message : String(e);
  }
}

// loadTopUps brings the consumer's top-up history — the wallet refill
// attempts, newest first (ticket 09).
async function loadTopUps() {
  if (!token.value) return;
  try {
    topups.value = await fetchMyTopUps(backendURL, token.value);
  } catch (e) {
    topupError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// startTopUp opens a payment session and sends the consumer to the
// gateway's hosted page. Nothing is credited here — the signed callback
// settles it, and refresh() shows the result on return.
async function startTopUp() {
  if (!token.value || topupBusy.value) return;
  if (!Number.isInteger(topupAmount.value) || topupAmount.value <= 0) {
    topupError.value = "Pick an amount in whole cents first.";
    return;
  }
  topupBusy.value = true;
  topupError.value = null;
  topupNotice.value = null;
  try {
    const tu = await initiateTopUp(backendURL, token.value, topupAmount.value);
    topupNotice.value = `Opening the payment page for ${formatMinor(tu.amountMinor, tu.currency)} — you'll come back here after paying.`;
    window.location.href = tu.paymentURL;
  } catch (e) {
    topupError.value = e instanceof Error ? e.message : String(e);
  } finally {
    topupBusy.value = false;
  }
}

// download streams the anonymized delivery for one finalized collection —
// charged to the Ledger at the unique-data rate.
async function download(c: Collection) {
  if (!token.value) return;
  downloading.value = c.id;
  collectionsError.value = null;
  try {
    await downloadDelivery(backendURL, token.value, c.id);
    await load();
  } catch (e) {
    collectionsError.value = e instanceof Error ? e.message : String(e);
  } finally {
    downloading.value = null;
  }
}

// facetsInOrder lists the catalog's active facets along the fixed axes so
// the filter panel renders deterministically.
const facetSections = computed(() => {
  if (!catalog.value) return [];
  return TAXONOMY_CATEGORIES.filter((c) => catalog.value!.facets[c]?.length).map((c) => ({
    category: c,
    values: catalog.value!.facets[c]!,
  }));
});

// matches reports whether one entry passes the current facet picks and the
// free-text query.
function matches(categories: { category: TaxonomyCategory; value: string; label: string }[]): boolean {
  for (const [cat, value] of Object.entries(picked.value)) {
    if (value && !categories.some((a) => a.category === cat && a.value === value)) return false;
  }
  return true;
}

// filteredEntries applies the facet picks and the text query to the catalog.
const filteredEntries = computed(() => {
  if (!catalog.value) return [];
  const q = query.value.trim().toLowerCase();
  return catalog.value.entries.filter((e) => {
    if (!matches(e.categories)) return false;
    if (q && !`${e.name} ${e.description}`.toLowerCase().includes(q)) return false;
    return true;
  });
});

function categoryLabel(category: TaxonomyCategory): string {
  return category.replaceAll("_", " ");
}

// pickedFilterCount tracks whether any filter is active (facet or text) so
// the "clear" affordance only shows when it can do something.
const hasFilters = computed(
  () => query.value.trim() !== "" || Object.values(picked.value).some((v) => v),
);

// maybeUnpick toggles a facet radio off when the consumer clicks the
// selected term again (radios alone can't go back to "all").
function maybeUnpick(category: TaxonomyCategory, value: string) {
  if (picked.value[category] === value) picked.value[category] = undefined;
}

function clearFilters() {
  picked.value = {};
  query.value = "";
}

// kindLabel translates a ledger kind for the history list.
function kindLabel(kind: string): string {
  const labels: Record<string, string> = {
    grant: "Grant",
    adjustment: "Adjustment",
    inference_charge: "Inference charge",
    data_charge: "Data charge",
    top_up: "Top-up",
    revenue_share: "Revenue share",
  };
  return labels[kind] ?? kind;
}

// detailText explains a movement: inference charges break down to tokens
// and applied rates; other movements carry their memo.
function detailText(mov: CreditsView["movements"][number]): string {
  if (mov.inference) {
    const d = mov.inference;
    const fresh = d.inputTokens - d.cachedInputTokens;
    return (
      `model ${d.model}: ${fresh.toLocaleString()} fresh + ${d.cachedInputTokens.toLocaleString()} cached input, ` +
      `${d.outputTokens.toLocaleString()} output @ ${d.inputMicrosPer1K}/${d.cachedInputMicrosPer1K}/${d.outputMicrosPer1K} μcr per 1k`
    );
  }
  return mov.memo || mov.id;
}

// myDelta sums this account's entries within a movement — the signed effect
// on the balance the row shows.
function myDelta(mov: CreditsView["movements"][number]): number {
  if (!account.value) return 0;
  const scope = `acct:${account.value.id}`;
  return mov.entries.filter((e) => e.scope === scope).reduce((sum, e) => sum + e.amountMicros, 0);
}
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
          >. Browse the catalog below — every shared asset carries its
          taxonomy categories and the price the current book quotes. Requests
          (buying) arrive in the next slices.
        </p>
      </section>

      <!-- Top-up (ticket 09): the wallet refill -->
      <section class="card">
        <h3>Top up credits</h3>
        <p class="hint">
          Buy credits through the platform's payment gateway. Picking an
          amount takes you to the hosted payment page — the credits land in
          your balance once the payment settles.
        </p>
        <div class="topup-row">
          <button
            v-for="preset in TOPUP_PRESETS"
            :key="preset"
            class="secondary preset"
            :class="{ picked: topupAmount === preset }"
            type="button"
            @click="topupAmount = preset"
          >
            €{{ preset / 100 }}
          </button>
          <label class="topup-custom">
            or €
            <input v-model.number="topupAmount" type="number" min="1" step="1" />
          </label>
          <button class="primary" type="button" :disabled="topupBusy" @click="startTopUp">
            {{ topupBusy ? "Opening…" : "Top up" }}
          </button>
        </div>
        <p v-if="topupError" class="error-text">{{ topupError }}</p>
        <p v-if="topupNotice" class="ok-text">{{ topupNotice }}</p>
        <template v-if="topups.length">
          <h4>Your top-ups</h4>
          <table class="ledger">
            <thead>
              <tr>
                <th>When</th>
                <th class="num">Amount</th>
                <th class="num">Credits</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="t in topups" :key="t.id">
                <td>{{ new Date(t.createdAt).toLocaleString() }}</td>
                <td class="num">{{ formatMinor(t.amountMinor, t.currency) }}</td>
                <td class="num">{{ formatCredits(t.creditsMicros) }}</td>
                <td>
                  <span
                    class="pill"
                    :class="t.status === 'settled' ? 'ok' : t.status === 'pending' ? 'warn' : ''"
                  >{{ topUpStatusLabel(t.status) }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </template>
      </section>

      <!-- Catalog (ticket 06) -->
      <section class="card">
        <h3>Catalog</h3>
        <p class="hint">
          Filter by the platform's taxonomy facets, or search names and
          descriptions. Prices shown are cached from the current price book —
          entitlement to open the data arrives with Requests.
        </p>
        <p v-if="catalogError" class="error-text">{{ catalogError }}</p>
        <p v-if="!catalog && !catalogError" class="hint">Loading catalog…</p>
        <template v-if="catalog">
          <div class="catalog-toolbar">
            <input v-model="query" type="search" placeholder="Search the catalog…" class="search" />
            <button v-if="hasFilters" class="secondary" @click="clearFilters">Clear filters</button>
          </div>
          <div v-if="facetSections.length" class="facets">
            <div v-for="section in facetSections" :key="section.category" class="facet">
              <h4>{{ categoryLabel(section.category) }}</h4>
              <label v-for="v in section.values" :key="v.value" class="facet-value">
                <input
                  v-model="picked[section.category]"
                  type="radio"
                  name=""
                  :value="v.value"
                  @change="maybeUnpick(section.category, v.value)"
                />
                {{ v.label }} <span class="hint">({{ v.count }})</span>
              </label>
            </div>
          </div>

          <p v-if="filteredEntries.length === 0" class="hint">
            Nothing matches these filters yet.
          </p>
          <table v-else class="ledger">
            <thead>
              <tr>
                <th>Asset</th>
                <th>Categories</th>
                <th class="num">Size</th>
                <th class="num">Price</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in filteredEntries" :key="e.id">
                <td>
                  <strong>{{ e.name }}</strong>
                  <p v-if="e.description" class="hint">{{ e.description }}</p>
                  <p class="hint">
                    {{ e.format }} · passed {{ e.pipeline.join(" → ") }}
                    <template v-if="e.provenance.source"> · source: {{ e.provenance.source }}</template>
                  </p>
                </td>
                <td class="cats">
                  <span
                    v-for="as in e.categories"
                    :key="as.category"
                    class="pill"
                    :title="`${categoryLabel(as.category)} · ${as.source} (${formatConfidence(as.confidence)})`"
                    >{{ as.label }}</span
                  >
                  <span v-if="e.categories.length === 0" class="hint">uncategorized</span>
                </td>
                <td class="num">{{ formatBytes(e.sizeBytes) }}</td>
                <td class="num">
                  {{ e.cachedPriceMicros > 0 ? formatMicros(e.cachedPriceMicros) : "—" }}
                </td>
                <td></td>
              </tr>
            </tbody>
          </table>
        </template>
      </section>

      <!-- Collections (ticket 12) -->
      <section class="card">
        <h3>Your collections</h3>
        <p>
          Data being gathered for you — a Farmer Organization's members answer
          over their own channels, every answer passes quality checks, and
          the delivery is anonymized before it reaches you. An incomplete
          collection shows exactly what's missing and why.
        </p>
        <p v-if="collectionsError" class="error-text">{{ collectionsError }}</p>
        <p v-if="collections.length === 0" class="hint">
          No collections yet — once an organization starts gathering data for
          one of your requests, it appears here.
        </p>
        <table v-else class="ledger">
          <thead>
            <tr>
              <th>Request</th>
              <th>Status</th>
              <th>Gathered</th>
              <th>Missing</th>
              <th>Updated</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in collections" :key="c.id">
              <td><code class="small">{{ c.requestId }}</code></td>
              <td>
                <span class="pill" :class="c.status === 'completed' ? 'ok' : c.status === 'incomplete' ? 'warn' : ''">
                  {{ statusLabel(c.status) }}
                </span>
              </td>
              <td>
                {{ c.items.filter((it) => it.status === "accepted").length }} / {{ c.items.length }}
                <span v-if="c.items.length" class="hint">items</span>
              </td>
              <td class="items-cell">
                <template v-if="c.missingCount > 0">
                  <span class="item-line">
                    {{ c.missingCount }} item{{ c.missingCount === 1 ? "" : "s" }} had no
                    usable answer — the delivered data notes the gaps.
                  </span>
                </template>
                <span v-else class="hint">—</span>
              </td>
              <td>{{ formatDate(c.updatedAt) }}</td>
              <td class="actions-cell">
                <button
                  v-if="c.status !== 'collecting'"
                  class="secondary"
                  :disabled="downloading === c.id"
                  @click="download(c)"
                >
                  {{ downloading === c.id ? "Preparing…" : "Download delivery" }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <section class="card">
        <h3>Credits</h3>
        <p class="hint">
          Your balance derives from the ledger — every grant and charge posts
          a balanced movement you can audit below.
        </p>
        <p v-if="error" class="error-text">{{ error }}</p>
        <template v-if="credits">
          <p class="balance">
            {{ formatCredits(credits.balanceMicros) }}
          </p>

          <p v-if="credits.movements.length === 0" class="hint">
            No movements yet — grants and charges will appear here.
          </p>
          <table v-else class="ledger">
            <thead>
              <tr>
                <th>When</th>
                <th>What</th>
                <th>Detail</th>
                <th class="num">Change</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="mov in credits.movements" :key="mov.id">
                <td>{{ new Date(mov.createdAt).toLocaleString() }}</td>
                <td>{{ kindLabel(mov.kind) }}</td>
                <td class="hint">{{ detailText(mov) }}</td>
                <td class="num" :class="myDelta(mov) < 0 ? 'spend' : 'gain'">
                  {{ myDelta(mov) < 0 ? "" : "+" }}{{ formatCredits(myDelta(mov)) }}
                </td>
              </tr>
            </tbody>
          </table>
        </template>
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

<style scoped>
.balance {
  font-size: 2rem;
  font-weight: 700;
  margin: 0.5rem 0 1rem;
}
.topup-row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  align-items: center;
  margin: 0.5rem 0;
}
.preset.picked {
  outline: 2px solid var(--accent, #2c6e49);
  font-weight: 700;
}
.topup-custom input {
  width: 7rem;
}
.ledger {
  width: 100%;
  border-collapse: collapse;
}
.ledger th,
.ledger td {
  text-align: left;
  padding: 0.5rem 0.75rem 0.5rem 0;
  border-bottom: 1px solid var(--line);
}
.ledger th {
  font-size: 0.8rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.spend {
  color: var(--danger, #b3372f);
}
.gain {
  color: var(--ok, #1d7a46);
}
.catalog-toolbar {
  display: flex;
  gap: 0.5rem;
  align-items: center;
  margin-bottom: 0.75rem;
}
.search {
  flex: 1;
}
.facets {
  display: flex;
  flex-wrap: wrap;
  gap: 1.5rem;
  margin-bottom: 1rem;
}
.facet h4 {
  margin: 0 0 0.3rem;
  font-size: 0.8rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--muted);
}
.facet-value {
  display: block;
  font-size: 0.92rem;
  margin: 0.15rem 0;
}
.facet-value input {
  margin-right: 0.35rem;
}
.cats .pill {
  margin-right: 0.3rem;
}
.items-cell {
  max-width: 20rem;
}
.item-line {
  display: block;
  font-size: 0.88rem;
  margin-bottom: 0.2rem;
}
.actions-cell {
  white-space: nowrap;
}
code.small {
  font-size: 0.82rem;
}
</style>
