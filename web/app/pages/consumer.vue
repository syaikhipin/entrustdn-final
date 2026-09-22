<script setup lang="ts">
// Data Consumer home: identity, the provable TOS record, and the spend
// view — balance and entry history derived from the Ledger (ticket 03).
// The catalog and Requests arrive in later tickets.
import { errFrom, fetchMyCredits, formatCredits, type CreditsView } from "~/credits";
import { useSession } from "~/composables/useSession";

const backendURL = useBackendURL();
const { token, account, restore } = useSession();

const ready = ref(false);
const credits = ref<CreditsView | null>(null);
const error = ref<string | null>(null);

onMounted(async () => {
  await restore();
  ready.value = true;
  if (token.value) await load();
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
          >. The catalog and Requests arrive in the next slices — this is your
          role's home.
        </p>
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
</style>
