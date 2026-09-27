<script setup lang="ts">
// Platform Admin home: the Farmer Organization application queue
// (approve/reject), TOS publishing, the credits desk (ticket 03), and the
// taxonomy workbench (ticket 06) — the vocabulary every upload is
// auto-categorized against.
import { decideApplication, errFrom, listApplications, publishTos, type Application } from "~/auth";
import {
  adminAdjust,
  adminCharge,
  adminGrant,
  fetchPricing,
  formatCredits,
  savePricing,
  type PricingRules,
} from "~/credits";
import {
  createTerm,
  deleteTerm,
  fetchTerms,
  updateTerm,
  TAXONOMY_CATEGORIES,
  type TaxonomyCategory,
  type Term,
} from "~/taxonomy";
import {
  fetchGatewayConfig,
  saveGatewayConfig,
  type GatewayConfig,
} from "~/payments";
import {
  createMemoryProvider,
  deleteMemoryProvider,
  fetchMemoryProviders,
  type MemoryProvider,
} from "~/memory";
import { useSession } from "~/composables/useSession";

const backendURL = useBackendURL();
const { token, account, restore } = useSession();

const ready = ref(false);
const applications = ref<Application[]>([]);
const error = ref<string | null>(null);
const notice = ref<string | null>(null);
const busyId = ref<string | null>(null);

// TOS publishing form.
const tosVersion = ref("");
const tosBody = ref("");
const tosNotice = ref<string | null>(null);
const tosError = ref<string | null>(null);

// Credits desk form state.
const creditAccountId = ref("");
const creditAmount = ref<number | null>(null);
const creditMemo = ref("");
const creditNotice = ref<string | null>(null);
const creditError = ref<string | null>(null);

// Test charge form state.
const chargeModel = ref("");
const chargeInput = ref<number | null>(null);
const chargeCached = ref<number | null>(null);
const chargeOutput = ref<number | null>(null);
const chargeDataClass = ref<"cached" | "unique">("cached");
const chargeUnits = ref<number | null>(null);
const chargeMode = ref<"inference" | "data">("inference");

// Price book state.
const pricing = ref<PricingRules | null>(null);
const pricingNotice = ref<string | null>(null);
const pricingError = ref<string | null>(null);

// Taxonomy workbench state (ticket 06).
const terms = ref<Term[]>([]);
const taxError = ref<string | null>(null);
const taxNotice = ref<string | null>(null);

// Payment gateway form state (ticket 09). The credential fields stay blank
// unless the admin is setting or rotating them — blank means keep stored.
const gateway = ref<GatewayConfig | null>(null);
const gatewayProvider = ref("");
const gatewayApiKey = ref("");
const gatewaySecret = ref("");
const gatewayCurrency = ref("");
const gatewayMicrosPerCent = ref<number | null>(null);
const gatewayError = ref<string | null>(null);
const gatewayNotice = ref<string | null>(null);

// Memory Provider registry state (ticket 14): the external recall services
// the agent dials over MCP — connection facts only.
const providers = ref<MemoryProvider[]>([]);
const newProviderName = ref("");
const newProviderEndpoint = ref("");
const providersError = ref<string | null>(null);
const providersNotice = ref<string | null>(null);
const newCategory = ref<TaxonomyCategory>("crop");
const newLabel = ref("");
const newKeywords = ref("");
const editingTerm = ref<Term | null>(null);
const editLabel = ref("");
const editKeywords = ref("");

onMounted(async () => {
  await restore();
  ready.value = true;
});

// The guard waits for restore(): before it finishes, a refreshing admin has
// a token but no parsed identity, and bouncing then would strand them at
// /login. Only a completed restore with no account is truly signed out.
watch(ready, (isReady) => {
  if (!isReady) return;
  if (!account.value) navigateTo("/login");
  else if (account.value.role === "platform_admin") {
    load();
    loadPricing();
    loadTerms();
    loadGateway();
    loadProviders();
  }
});

async function load() {
  if (!token.value) return;
  try {
    applications.value = await listApplications(backendURL, token.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  }
}

async function loadPricing() {
  if (!token.value) return;
  pricingError.value = null;
  try {
    pricing.value = await fetchPricing(backendURL, token.value);
  } catch (e) {
    // An unconfigured price book is a normal pilot state; anything else
    // (network, auth) must NOT hand the admin an editable empty book they
    // could accidentally save over the real one.
    const msg = e instanceof Error ? e.message : errFrom(e);
    if (/no price book/i.test(msg)) {
      pricing.value = { inference: [], data: { cachedAssetMicrosPerUnit: 0, uniqueMicrosPerUnit: 0 } };
    } else {
      pricing.value = null;
      pricingError.value = msg;
    }
  }
}

// The Revenue Share percent edits through a writable proxy so "unset"
// (undefined — the backend's 80/20 default) renders as an empty input
// rather than a misleading 80; typing a number makes it explicit. Writing
// the empty string deletes the field, so the save carries no percentage.
const orgPercentProxy = computed<number | "">({
  get: () => pricing.value?.revenueShareOrgPercent ?? "",
  set: (v) => {
    if (!pricing.value) return;
    if (v === "" || v === null || v === undefined) {
      delete pricing.value.revenueShareOrgPercent;
    } else {
      pricing.value.revenueShareOrgPercent = Number(v);
    }
  },
});

// loadGateway reads the masked gateway config. An unconfigured gateway is a
// normal state — the form starts empty rather than erroring.
async function loadGateway() {
  if (!token.value) return;
  gatewayError.value = null;
  try {
    applyGateway(await fetchGatewayConfig(backendURL, token.value));
  } catch (e) {
    gateway.value = null;
    gatewayError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// applyGateway fills the form from the masked config; credential inputs
// always start blank.
function applyGateway(cfg: GatewayConfig) {
  gateway.value = cfg;
  gatewayProvider.value = cfg.provider;
  gatewayApiKey.value = "";
  gatewaySecret.value = "";
  gatewayCurrency.value = cfg.currency;
  gatewayMicrosPerCent.value = cfg.microsPerCent > 0 ? cfg.microsPerCent : null;
}

// submitGateway saves the config. Blank credential fields ride as empty
// strings — the backend keeps the stored values (blank-keeps-credential
// rotation). Saving a blank provider disables top-ups.
async function submitGateway() {
  if (!token.value) return;
  gatewayError.value = null;
  gatewayNotice.value = null;
  try {
    const cfg = await saveGatewayConfig(backendURL, token.value, {
      provider: gatewayProvider.value.trim(),
      apiKey: gatewayApiKey.value,
      webhookSecret: gatewaySecret.value,
      currency: gatewayCurrency.value.trim(),
      microsPerCent: gatewayMicrosPerCent.value ?? 0,
    });
    applyGateway(cfg);
    gatewayNotice.value = cfg.enabled
      ? `Gateway saved — top-ups are live via ${cfg.provider}.`
      : "Gateway disabled — top-ups are off.";
  } catch (e) {
    gatewayError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// disableGateway turns top-ups off entirely: an empty provider clears the
// stored config.
async function disableGateway() {
  if (!token.value) return;
  gatewayError.value = null;
  gatewayNotice.value = null;
  try {
    applyGateway(await saveGatewayConfig(backendURL, token.value, {
      provider: "",
      currency: "",
      microsPerCent: 0,
    }));
    gatewayNotice.value = "Gateway disabled — top-ups are off.";
  } catch (e) {
    gatewayError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// loadProviders reads the Memory Provider registry (ticket 14).
async function loadProviders() {
  if (!token.value) return;
  providersError.value = null;
  try {
    providers.value = await fetchMemoryProviders(backendURL, token.value);
  } catch (e) {
    providersError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// submitProvider registers one provider — a connection fact, never
// credentials: the agent connects over MCP with none.
async function submitProvider() {
  if (!token.value) return;
  providersError.value = null;
  providersNotice.value = null;
  try {
    const p = await createMemoryProvider(backendURL, token.value, {
      name: newProviderName.value.trim(),
      endpoint: newProviderEndpoint.value.trim(),
    });
    newProviderName.value = "";
    newProviderEndpoint.value = "";
    providersNotice.value = `Provider ${p.name} registered — recall now includes it.`;
    await loadProviders();
  } catch (e) {
    providersError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// removeProvider deletes one provider; the agent stops dialing it on the
// next turn.
async function removeProvider(p: MemoryProvider) {
  if (!token.value) return;
  providersError.value = null;
  providersNotice.value = null;
  try {
    await deleteMemoryProvider(backendURL, token.value, p.id);
    providersNotice.value = `Provider ${p.name} removed.`;
    await loadProviders();
  } catch (e) {
    providersError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

async function decide(app: Application, decision: "approve" | "reject") {
  if (!token.value) return;
  busyId.value = app.id;
  error.value = null;
  notice.value = null;
  try {
    await decideApplication(backendURL, token.value, app.id, decision);
    notice.value =
      decision === "approve"
        ? `Approved ${app.displayName} — they can now trade on the platform.`
        : `Declined ${app.displayName}.`;
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  } finally {
    busyId.value = null;
  }
}

async function publish() {
  if (!token.value) return;
  tosError.value = null;
  tosNotice.value = null;
  try {
    await publishTos(backendURL, token.value, tosVersion.value.trim(), tosBody.value.trim());
    tosNotice.value = `Version ${tosVersion.value.trim()} published — every account re-accepts at next login.`;
    tosVersion.value = "";
    tosBody.value = "";
  } catch (e) {
    tosError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// microsFromCredits parses a whole-or-fractional credit amount into micros;
// null when the field is empty or not a number.
function microsFromCredits(value: number | null): number | null {
  if (value === null || Number.isNaN(value)) return null;
  return Math.round(value * 1_000_000);
}

// requireCreditForm validates the credits-desk form; returns the target
// account id and amount in micros, or null after setting creditError.
function requireCreditForm(positiveOnly: boolean): { id: string; micros: number } | null {
  creditError.value = null;
  const id = creditAccountId.value.trim();
  if (id === "") {
    creditError.value = "Give the account id to credit.";
    return null;
  }
  const micros = microsFromCredits(creditAmount.value);
  if (micros === null || micros === 0) {
    creditError.value = "Give a non-zero amount in credits.";
    return null;
  }
  if (positiveOnly && micros <= 0) {
    creditError.value = "Grants must be positive — use the adjustment for corrections.";
    return null;
  }
  return { id, micros };
}

async function grant() {
  if (!token.value) return;
  creditNotice.value = null;
  const form = requireCreditForm(true);
  if (form === null) return;
  try {
    await adminGrant(backendURL, token.value, form.id, form.micros, creditMemo.value.trim());
    creditNotice.value = `Granted ${formatCredits(form.micros)} — posted through the ledger.`;
  } catch (e) {
    creditError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

async function adjust() {
  if (!token.value) return;
  creditNotice.value = null;
  const form = requireCreditForm(false);
  if (form === null) return;
  try {
    await adminAdjust(backendURL, token.value, form.id, form.micros, creditMemo.value.trim());
    creditNotice.value = `Adjusted by ${formatCredits(form.micros)} — the ledger holds the correction.`;
  } catch (e) {
    creditError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

async function charge() {
  if (!token.value) return;
  creditNotice.value = null;
  const id = creditAccountId.value.trim();
  if (id === "") {
    creditError.value = "Give the account id to charge.";
    return;
  }
  try {
    let charged: number;
    if (chargeMode.value === "inference") {
      charged = await adminCharge(backendURL, token.value, {
        accountId: id,
        inference: {
          model: chargeModel.value.trim(),
          inputTokens: Math.max(0, Math.round(chargeInput.value ?? 0)),
          cachedInputTokens: Math.max(0, Math.round(chargeCached.value ?? 0)),
          outputTokens: Math.max(0, Math.round(chargeOutput.value ?? 0)),
        },
        memo: creditMemo.value.trim(),
      });
    } else {
      charged = await adminCharge(backendURL, token.value, {
        accountId: id,
        data: { class: chargeDataClass.value, units: Math.max(0, Math.round(chargeUnits.value ?? 0)) },
        memo: creditMemo.value.trim(),
      });
    }
    creditNotice.value = `Charged ${formatCredits(charged)} — priced automatically from the current price book.`;
  } catch (e) {
    creditError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

async function saveRules() {
  if (!token.value || !pricing.value) return;
  pricingError.value = null;
  pricingNotice.value = null;
  try {
    await savePricing(backendURL, token.value, pricing.value);
    pricingNotice.value = "Price book saved — future charges use it immediately; posted movements are never rewritten.";
  } catch (e) {
    pricingError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

function addModelRule() {
  if (!pricing.value) return;
  pricing.value.inference.push({
    model: "",
    inputMicrosPer1K: 0,
    cachedInputMicrosPer1K: 0,
    outputMicrosPer1K: 0,
  });
}

function removeModelRule(index: number) {
  pricing.value?.inference.splice(index, 1);
}

// --- Taxonomy workbench (ticket 06) ---

async function loadTerms() {
  if (!token.value) return;
  try {
    terms.value = await fetchTerms(backendURL);
  } catch (e) {
    taxError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// termsFor lists one axis's vocabulary; keywords render as a compact hint.
function termsFor(category: TaxonomyCategory): Term[] {
  return terms.value.filter((t) => t.category === category);
}

function keywordsHint(t: Term): string {
  return t.keywords.length ? t.keywords.join(", ") : "no keywords — label words are used";
}

async function submitCreateTerm() {
  if (!token.value) return;
  taxError.value = null;
  taxNotice.value = null;
  const keywords = newKeywords.value
    .split(",")
    .map((k) => k.trim().toLowerCase())
    .filter(Boolean);
  try {
    const created = await createTerm(backendURL, token.value, {
      category: newCategory.value,
      label: newLabel.value.trim(),
      keywords,
    });
    taxNotice.value = `Added “${created.label}” — future uploads classify against it.`;
    newLabel.value = "";
    newKeywords.value = "";
    await loadTerms();
  } catch (e) {
    taxError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

function startEditTerm(t: Term) {
  editingTerm.value = t;
  editLabel.value = t.label;
  editKeywords.value = t.keywords.join(", ");
}

async function submitEditTerm() {
  if (!token.value || !editingTerm.value) return;
  taxError.value = null;
  taxNotice.value = null;
  const keywords = editKeywords.value
    .split(",")
    .map((k) => k.trim().toLowerCase())
    .filter(Boolean);
  try {
    await updateTerm(backendURL, token.value, editingTerm.value.id, {
      label: editLabel.value.trim(),
      keywords,
    });
    taxNotice.value = "Term updated — its category and value never change, so past assignments stay honest.";
    editingTerm.value = null;
    await loadTerms();
  } catch (e) {
    taxError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

async function submitDeleteTerm(t: Term) {
  if (!token.value) return;
  taxError.value = null;
  taxNotice.value = null;
  try {
    await deleteTerm(backendURL, token.value, t.id);
    taxNotice.value = `Removed “${t.label}”.`;
    await loadTerms();
  } catch (e) {
    // A 409 (assets still carry the term) lands here with the backend's fix-it message.
    taxError.value = e instanceof Error ? e.message : errFrom(e);
  }
}
</script>

<template>
  <div>
    <section v-if="!ready" class="card">
      <p class="hint">Loading…</p>
    </section>

    <template v-else>
      <section class="card">
        <h2>Platform Admin</h2>
        <p class="hint">
          Applications, the contract versions, the credits desk, and the
          taxonomy workbench; modules and platform stats arrive in later
          slices.
        </p>
        <p v-if="error" class="error-text">{{ error }}</p>
        <p v-if="notice" class="ok-text">{{ notice }}</p>
      </section>

      <section class="card">
        <h3>Farmer Organization applications</h3>
        <p v-if="applications.length === 0" class="hint">No pending applications.</p>
        <ul v-else class="queue">
          <li v-for="app in applications" :key="app.id" class="row">
            <div>
              <strong>{{ app.displayName }}</strong>
              <div class="hint">{{ app.email }} · applied {{ new Date(app.createdAt).toLocaleDateString() }}</div>
            </div>
            <div class="row-actions">
              <button class="primary" :disabled="busyId === app.id" @click="decide(app, 'approve')">Approve</button>
              <button class="danger" :disabled="busyId === app.id" @click="decide(app, 'reject')">Reject</button>
            </div>
          </li>
        </ul>
      </section>

      <section class="card">
        <h3>Credits desk</h3>
        <p class="hint">
          Every grant, adjustment, and test charge posts a balanced movement
          through the ledger. Charges are priced automatically from the price
          book below — cached tokens bill at the cached rate, fresh tokens and
          outputs at their own rates.
        </p>
        <form @submit.prevent>
          <label>
            Account id
            <input v-model="creditAccountId" type="text" required placeholder="acct-uuid…" />
          </label>
          <label>
            Amount (credits)
            <input v-model.number="creditAmount" type="number" step="0.000001" placeholder="e.g. 100" />
          </label>
          <label>
            Memo
            <input v-model="creditMemo" type="text" placeholder="why this movement exists" />
          </label>
          <div class="row-actions">
            <button class="primary" type="button" @click="grant">Grant</button>
            <button type="button" @click="adjust">Adjust</button>
          </div>
        </form>

        <h4>Test charge (automatic pricing)</h4>
        <p class="hint">
          Posts a real charge against the account at current prices — the demo
          path for the pricing engine.
        </p>
        <div class="row-actions charge-mode">
          <label><input v-model="chargeMode" type="radio" value="inference" /> Inference</label>
          <label><input v-model="chargeMode" type="radio" value="data" /> Data</label>
        </div>
        <form @submit.prevent>
          <template v-if="chargeMode === 'inference'">
            <label>
              Model
              <input v-model="chargeModel" type="text" placeholder="e.g. test-model" />
            </label>
            <label>
              Input tokens
              <input v-model.number="chargeInput" type="number" min="0" />
            </label>
            <label>
              Cached input tokens
              <input v-model.number="chargeCached" type="number" min="0" />
            </label>
            <label>
              Output tokens
              <input v-model.number="chargeOutput" type="number" min="0" />
            </label>
          </template>
          <template v-else>
            <label>
              Data class
              <select v-model="chargeDataClass">
                <option value="cached">cached (already on the platform)</option>
                <option value="unique">unique (fresh to this request)</option>
              </select>
            </label>
            <label>
              Units
              <input v-model.number="chargeUnits" type="number" min="0" />
            </label>
          </template>
          <button class="primary" type="button" @click="charge">Post charge</button>
        </form>
        <p v-if="creditError" class="error-text">{{ creditError }}</p>
        <p v-if="creditNotice" class="ok-text">{{ creditNotice }}</p>
      </section>

      <section class="card">
        <h3>Price book</h3>
        <p class="hint">
          Rates are micro-credits per 1k tokens (inference) or per unit
          (data). Saving applies to future charges only.
        </p>
        <p v-if="pricingError" class="error-text">{{ pricingError }}</p>
        <p v-if="pricingNotice" class="ok-text">{{ pricingNotice }}</p>
        <p v-if="!pricing && !pricingError" class="hint">Loading price book…</p>
        <template v-if="pricing">
          <table class="ledger">
            <thead>
              <tr>
                <th>Model</th>
                <th class="num">Input /1k</th>
                <th class="num">Cached input /1k</th>
                <th class="num">Output /1k</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(rule, i) in pricing.inference" :key="i">
                <td><input v-model="rule.model" type="text" placeholder="model name" /></td>
                <td class="num"><input v-model.number="rule.inputMicrosPer1K" type="number" min="0" class="rate" /></td>
                <td class="num"><input v-model.number="rule.cachedInputMicrosPer1K" type="number" min="0" class="rate" /></td>
                <td class="num"><input v-model.number="rule.outputMicrosPer1K" type="number" min="0" class="rate" /></td>
                <td><button type="button" class="danger" @click="removeModelRule(i)">✕</button></td>
              </tr>
            </tbody>
          </table>
          <button type="button" @click="addModelRule">Add model rule</button>

          <h4>Data rates</h4>
          <label>
            Cached asset, micros per unit
            <input v-model.number="pricing.data.cachedAssetMicrosPerUnit" type="number" min="0" />
          </label>
          <label>
            Unique data, micros per unit
            <input v-model.number="pricing.data.uniqueMicrosPerUnit" type="number" min="0" />
          </label>

          <h4>Revenue share</h4>
          <label>
            Farmer Organization's share of the data premium, %
            <input
              v-model.number="orgPercentProxy"
              type="number"
              min="0"
              max="100"
            />
          </label>
          <p class="hint">
            The rest is the platform's share. Left unset, the default 80/20
            applies. Changing it affects future deliveries only.
          </p>

          <button class="primary" type="button" @click="saveRules">Save price book</button>
        </template>
      </section>

      <!-- Payment gateway (ticket 09): server-side platform config -->
      <section class="card">
        <h3>Payment gateway</h3>
        <p class="hint">
          Top-ups ride a payment gateway you configure here — server-side
          platform config, never a module. Credentials are stored server-side
          and shown only as "set"; re-enter one only when rotating it. The
          exchange rate says how many micro-credits one cent buys (10,000 →
          €1 = 1 credit).
        </p>
        <p v-if="gatewayError" class="error-text">{{ gatewayError }}</p>
        <p v-if="gatewayNotice" class="ok-text">{{ gatewayNotice }}</p>
        <p v-if="!gateway && !gatewayError" class="hint">Loading gateway config…</p>
        <template v-if="gateway">
          <p>
            <span class="pill" :class="gateway.enabled ? 'ok' : 'warn'">
              {{ gateway.enabled ? `Enabled — ${gateway.provider}` : "Disabled — top-ups are off" }}
            </span>
            <span v-if="gateway.apiKeySet" class="hint"> · API key set</span>
            <span v-if="gateway.webhookSecretSet" class="hint"> · webhook secret set</span>
          </p>
          <form @submit.prevent="submitGateway">
            <label>
              Provider
              <input v-model="gatewayProvider" type="text" placeholder="stripe" />
            </label>
            <label>
              API key
              <input
                v-model="gatewayApiKey"
                type="password"
                :placeholder="gateway.apiKeySet ? 'stored — leave blank to keep' : 'sk_live_…'"
                autocomplete="off"
              />
            </label>
            <label>
              Webhook secret
              <input
                v-model="gatewaySecret"
                type="password"
                :placeholder="gateway.webhookSecretSet ? 'stored — leave blank to keep' : 'whsec_…'"
                autocomplete="off"
              />
            </label>
            <label>
              Currency (ISO 4217)
              <input v-model="gatewayCurrency" type="text" placeholder="eur" />
            </label>
            <label>
              Micro-credits per cent
              <input v-model.number="gatewayMicrosPerCent" type="number" min="1" step="1" />
            </label>
            <div class="row-actions">
              <button class="primary" type="submit">Save gateway</button>
              <button
                v-if="gateway.enabled"
                class="danger"
                type="button"
                @click="disableGateway"
              >
                Disable top-ups
              </button>
            </div>
          </form>
        </template>
      </section>

      <!-- Memory providers (ticket 14): external recall services the agent
           dials over MCP. Connection facts only — no credentials exist in
           the shape, so none are asked for. -->
      <section class="card">
        <h3>Memory providers</h3>
        <p class="hint">
          The agent recalls what earlier conversations established before it
          re-asks a consumer. Register the recall services here — mem0,
          Hindsight, supermemory, or any MCP-speaking provider. Connection
          facts only: name and endpoint; the agent authenticates with none.
          At most five.
        </p>
        <p v-if="providersError" class="error-text">{{ providersError }}</p>
        <p v-if="providersNotice" class="ok-text">{{ providersNotice }}</p>

        <form @submit.prevent="submitProvider">
          <label>
            Name (lowercase slug)
            <input v-model="newProviderName" type="text" required placeholder="mem0-primary" />
          </label>
          <label>
            MCP endpoint
            <input v-model="newProviderEndpoint" type="url" required placeholder="http://memory.local:8080/mcp" />
          </label>
          <button class="primary" type="submit">Register provider</button>
        </form>

        <p v-if="providers.length === 0" class="hint">No providers configured — the agent works without memory.</p>
        <ul v-else class="term-list">
          <li v-for="p in providers" :key="p.id">
            <strong>{{ p.name }}</strong>
            <span class="hint"> — {{ p.endpoint }}</span>
            <button class="danger" type="button" @click="removeProvider(p)">Remove</button>
          </li>
        </ul>
      </section>

      <section class="card">
        <h3>Taxonomy</h3>
        <p class="hint">
          The vocabulary every upload is auto-categorized against, across six
          axes. Terms carry keywords the classifier matches; labels are what
          buyers see. A term's category and value never change once created,
          and a term assets still carry cannot be deleted.
        </p>
        <p v-if="taxError" class="error-text">{{ taxError }}</p>
        <p v-if="taxNotice" class="ok-text">{{ taxNotice }}</p>

        <form class="new-term" @submit.prevent="submitCreateTerm">
          <label>
            Category
            <select v-model="newCategory">
              <option v-for="c in TAXONOMY_CATEGORIES" :key="c" :value="c">{{ c.replaceAll("_", " ") }}</option>
            </select>
          </label>
          <label>
            Label
            <input v-model="newLabel" type="text" required placeholder="e.g. County Leitrim" />
          </label>
          <label>
            Keywords (comma-separated)
            <input v-model="newKeywords" type="text" placeholder="e.g. leitrim, border" />
          </label>
          <button class="primary" type="submit">Add term</button>
        </form>

        <div v-for="c in TAXONOMY_CATEGORIES" :key="c" class="term-axis">
          <h4>{{ c.replaceAll("_", " ") }}</h4>
          <p v-if="termsFor(c).length === 0" class="hint">No terms yet.</p>
          <ul v-else class="term-list">
            <li v-for="t in termsFor(c)" :key="t.id">
              <template v-if="editingTerm?.id === t.id">
                <form class="edit-term" @submit.prevent="submitEditTerm">
                  <input v-model="editLabel" type="text" required />
                  <input v-model="editKeywords" type="text" placeholder="keywords, comma-separated" />
                  <button class="primary" type="submit">Save</button>
                  <button class="secondary" type="button" @click="editingTerm = null">Cancel</button>
                </form>
              </template>
              <template v-else>
                <div class="term-row">
                  <div>
                    <strong>{{ t.label }}</strong>
                    <span class="hint"> · {{ t.value }} · keywords: {{ keywordsHint(t) }}</span>
                  </div>
                  <div class="row-actions">
                    <button class="secondary" @click="startEditTerm(t)">Edit</button>
                    <button class="danger" @click="submitDeleteTerm(t)">Delete</button>
                  </div>
                </div>
              </template>
            </li>
          </ul>
        </div>
      </section>

      <section class="card">
        <h3>Publish a new Terms of Service version</h3>
        <p class="hint">
          Publishing makes the version current: every account that accepted an
          older one must re-accept at next login. Versions are immutable once
          published.
        </p>
        <form @submit.prevent="publish">
          <label>
            Version
            <input v-model="tosVersion" type="text" required placeholder="e.g. 1.1" />
          </label>
          <label>
            Terms text
            <textarea v-model="tosBody" rows="5" required placeholder="The full contract text…"></textarea>
          </label>
          <p v-if="tosError" class="error-text">{{ tosError }}</p>
          <p v-if="tosNotice" class="ok-text">{{ tosNotice }}</p>
          <button class="primary" type="submit">Publish version</button>
        </form>
      </section>
    </template>
  </div>
</template>

<style scoped>
.queue {
  list-style: none;
  margin: 0;
  padding: 0;
}
.row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 1rem;
  padding: 0.75rem 0;
  border-bottom: 1px solid var(--line);
}
.row:last-child {
  border-bottom: 0;
}
.row-actions {
  display: flex;
  gap: 0.5rem;
  flex-shrink: 0;
}
.charge-mode {
  margin: 0.5rem 0;
}
.ledger {
  width: 100%;
  border-collapse: collapse;
  margin-bottom: 0.75rem;
}
.ledger th,
.ledger td {
  text-align: left;
  padding: 0.4rem 0.5rem 0.4rem 0;
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
.rate {
  width: 7rem;
}
.new-term {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: end;
  margin-bottom: 1rem;
}
.new-term label {
  margin-bottom: 0;
  min-width: 10rem;
}
.new-term button {
  margin-bottom: 0.15rem;
}
.term-axis h4 {
  margin: 0.75rem 0 0.3rem;
  font-size: 0.8rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--muted);
}
.term-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.term-list li {
  border-bottom: 1px solid var(--line);
  padding: 0.4rem 0;
}
.term-list li:last-child {
  border-bottom: 0;
}
.term-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 1rem;
}
.term-row .row-actions {
  display: flex;
  gap: 0.4rem;
  flex-shrink: 0;
}
.edit-term {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  align-items: center;
}
</style>
