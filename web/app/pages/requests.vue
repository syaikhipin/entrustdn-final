<script setup lang="ts">
// Requests (ticket 07): a Data Consumer creates a Request — data wanted,
// format, quality bar, budget in Credits — and clarifies it with the Agent
// in a web chat. Every turn meters the model call and charges the Ledger
// against the Request's budget; the page shows the running spend and
// surfaces a budget refusal (budget_exhausted) instead of hiding it.
import {
  chatTurn,
  createRequest,
  errFrom,
  fetchMyRequests,
  formatBudget,
  type DataRequest,
} from "~/requests";
import { useSession } from "~/composables/useSession";

const backendURL = useBackendURL();
const { token, me, account, restore } = useSession();

const ready = ref(false);
const requests = ref<DataRequest[]>([]);
const error = ref<string | null>(null);

// Creation form state.
const description = ref("");
const format = ref("csv");
const qualityBar = ref("");
const budgetCredits = ref("10");
const creating = ref(false);
const showForm = ref(false);

// Chat state: one open conversation at a time.
const openId = ref<string | null>(null);
const draft = ref("");
const sending = ref(false);
const chatError = ref<string | null>(null);
const lastCharge = ref<string | null>(null);

onMounted(async () => {
  await restore();
  ready.value = true;
  if (token.value) await load();
});

watch(ready, (isReady) => {
  if (isReady && !account.value) navigateTo("/login");
});

async function load() {
  if (!token.value) return;
  try {
    requests.value = await fetchMyRequests(backendURL, token.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  }
}

async function submit() {
  if (!token.value || creating.value) return;
  creating.value = true;
  error.value = null;
  try {
    const created = await createRequest(backendURL, token.value, {
      description: description.value,
      format: format.value,
      qualityBar: qualityBar.value,
      budgetMicros: Math.round(Number(budgetCredits.value) * 1_000_000),
    });
    description.value = "";
    qualityBar.value = "";
    showForm.value = false;
    await load();
    open(created.id);
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  } finally {
    creating.value = false;
  }
}

function open(id: string) {
  openId.value = id;
  chatError.value = null;
  lastCharge.value = null;
  draft.value = "";
}

function close() {
  openId.value = null;
}

const openRequest = computed(
  () => requests.value.find((r) => r.id === openId.value) ?? null,
);

async function send() {
  if (!token.value || !openId.value || sending.value) return;
  const message = draft.value.trim();
  if (message === "") return;
  sending.value = true;
  chatError.value = null;
  lastCharge.value = null;
  try {
    const turn = await chatTurn(backendURL, token.value, openId.value, message);
    draft.value = "";
    lastCharge.value = `this turn cost ${turn.chargedMicros.toLocaleString("en-IE")} µcr`;
    replaceRequest(turn.request);
  } catch (e) {
    chatError.value = e instanceof Error ? e.message : errFrom(e);
    // A refused turn (402) still changed the request server-side — refetch
    // so the surfaced status shows instead of a stale conversation.
    await load();
  } finally {
    sending.value = false;
  }
}

function replaceRequest(updated: DataRequest) {
  const i = requests.value.findIndex((r) => r.id === updated.id);
  if (i >= 0) requests.value[i] = updated;
  else requests.value.unshift(updated);
}

// statusLabel translates a request status for the list and the header.
function statusLabel(status: DataRequest["status"]): string {
  const labels: Record<string, string> = {
    clarifying: "Clarifying",
    clarified: "Clarified",
    budget_exhausted: "Budget exhausted",
  };
  return labels[status] ?? status;
}

const canSubmit = computed(
  () =>
    description.value.trim() !== "" &&
    format.value.trim() !== "" &&
    Number(budgetCredits.value) > 0 &&
    !creating.value,
);
</script>

<template>
  <div>
    <section v-if="!ready" class="card">
      <p class="hint">Loading…</p>
    </section>

    <template v-else-if="me">
      <section class="card">
        <h2>Requests</h2>
        <p>
          Commission data you can't find in the catalog: describe the need,
          set a budget in Credits, and clarify it with the Agent. Every chat
          turn is metered against the budget — when it can't cover a turn,
          the request is marked and shown here.
        </p>
        <button v-if="!showForm" @click="showForm = true">New request</button>
        <form v-else class="new-request" @submit.prevent="submit">
          <label>
            What data do you need?
            <textarea
              v-model="description"
              rows="3"
              placeholder="Spring barley yields across Leinster for the 2026 season"
              required
            ></textarea>
          </label>
          <div class="row">
            <label>
              Format
              <input v-model="format" type="text" placeholder="csv" required />
            </label>
            <label>
              Budget (credits)
              <input v-model="budgetCredits" type="number" min="0.000001" step="any" required />
            </label>
          </div>
          <label>
            Quality bar (optional)
            <input
              v-model="qualityBar"
              type="text"
              placeholder="Farm-level records with provenance"
            />
          </label>
          <div class="row">
            <button type="submit" :disabled="!canSubmit">Create request</button>
            <button type="button" class="secondary" @click="showForm = false">Cancel</button>
          </div>
        </form>
        <p v-if="error" class="error-text">{{ error }}</p>
      </section>

      <!-- The open conversation -->
      <section v-if="openRequest" class="card">
        <h3>{{ openRequest.description }}</h3>
        <p class="hint">
          {{ openRequest.format }}
          <template v-if="openRequest.qualityBar"> · {{ openRequest.qualityBar }}</template>
          · {{ formatBudget(openRequest.spentMicros, openRequest.budgetMicros) }}
          · <strong>{{ statusLabel(openRequest.status) }}</strong>
        </p>
        <p v-if="openRequest.matches.length" class="matches hint">
          Already in the catalog:
          <span v-for="m in openRequest.matches" :key="m.assetId" class="pill" :title="m.reason">
            {{ m.name }}
          </span>
        </p>
        <div class="chat" aria-live="polite">
          <p v-if="openRequest.messages.length === 0" class="hint">
            No messages yet — say what you need.
          </p>
          <div
            v-for="(m, i) in openRequest.messages"
            :key="i"
            class="bubble"
            :class="m.role"
          >
            {{ m.body }}
          </div>
        </div>
        <p v-if="lastCharge" class="hint charge">{{ lastCharge }}</p>
        <p v-if="chatError" class="error-text">{{ chatError }}</p>
        <form v-if="openRequest.status === 'clarifying'" class="chat-input" @submit.prevent="send">
          <input
            v-model="draft"
            type="text"
            placeholder="Answer the agent…"
            :disabled="sending"
          />
          <button type="submit" :disabled="sending || draft.trim() === ''">
            {{ sending ? "Sending…" : "Send" }}
          </button>
        </form>
        <p v-else class="hint">
          This request is no longer taking turns —
          {{ openRequest.status === "clarified" ? "the need is fully specified." : "raise the budget to continue (coming soon)." }}
        </p>
        <button class="secondary" @click="close">Close conversation</button>
      </section>

      <!-- The list -->
      <section class="card">
        <h3>Your requests</h3>
        <p v-if="requests.length === 0" class="hint">
          No requests yet — create one above.
        </p>
        <table v-else class="ledger">
          <thead>
            <tr>
              <th>Request</th>
              <th>Status</th>
              <th class="num">Budget</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in requests" :key="r.id">
              <td>
                <strong>{{ r.description }}</strong>
                <p class="hint">{{ r.format }} · {{ r.matches.length }} catalog match(es)</p>
              </td>
              <td>{{ statusLabel(r.status) }}</td>
              <td class="num">{{ formatBudget(r.spentMicros, r.budgetMicros) }}</td>
              <td>
                <button v-if="r.id !== openId" class="secondary" @click="open(r.id)">Open</button>
              </td>
            </tr>
          </tbody>
        </table>
      </section>
    </template>
  </div>
</template>

<style scoped>
.new-request {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  margin-top: 0.75rem;
}
.new-request .row {
  display: flex;
  gap: 1rem;
}
.new-request .row label {
  flex: 1;
}
.chat {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
  margin: 0.75rem 0;
}
.bubble {
  max-width: 75%;
  padding: 0.5rem 0.8rem;
  border-radius: 0.9rem;
  border: 1px solid var(--line);
}
.bubble.consumer {
  align-self: flex-end;
  background: var(--accent-soft, #eef4ee);
}
.bubble.agent {
  align-self: flex-start;
}
.chat-input {
  display: flex;
  gap: 0.5rem;
  margin-bottom: 0.75rem;
}
.chat-input input {
  flex: 1;
}
.charge {
  margin: 0.2rem 0;
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
</style>
