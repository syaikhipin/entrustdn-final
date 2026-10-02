<script setup lang="ts">
// Platform Admin stats dashboard (ticket 16): one page of real numbers —
// requests over time, request/collection statuses, the Ledger's credit
// flows, Channel activity, and module usage. Every figure derives from
// platform records through GET /api/v1/admin/stats; the page renders the
// document, it never invents one.
import { errFrom } from "~/api";
import {
  fetchAdminStats,
  formatCredits,
  type StatsSnapshot,
} from "~/stats";
import { useSession } from "~/composables/useSession";

const backendURL = useBackendURL();
const { token, account, restore } = useSession();

const ready = ref(false);
const stats = ref<StatsSnapshot | null>(null);
const error = ref<string | null>(null);

// The hovered day for the requests-over-time tooltip.
const hoverDay = ref<number | null>(null);

onMounted(async () => {
  await restore();
  ready.value = true;
});

watch(ready, (isReady) => {
  if (!isReady) return;
  if (!account.value) navigateTo("/login");
  else if (account.value.role === "platform_admin") load();
  else navigateTo("/");
});

async function load() {
  if (!token.value) return;
  error.value = null;
  try {
    stats.value = await fetchAdminStats(backendURL, token.value);
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// statusCount sums a status breakdown to a headline number.
function statusTotal(list: { status: string; count: number }[]): number {
  return list.reduce((n, s) => n + s.count, 0);
}

function statusGet(list: { status: string; count: number }[], status: string): number {
  return list.find((s) => s.status === status)?.count ?? 0;
}

// maxDaily caps the requests-over-time bar heights.
const maxDaily = computed(() =>
  Math.max(1, ...(stats.value?.requestsOverTime ?? []).map((d) => d.count))
);

// barHeight maps one day's count into the 0–96px plot area, thin bars with
// a floor of 2px so a zero day still reads as a baseline tick.
function barHeight(count: number): number {
  if (count <= 0) return 2;
  return Math.max(4, Math.round((count / maxDaily.value) * 96));
}

// The five ledger kinds the platform posts, in display order.
const FLOW_KINDS: { kind: string; label: string }[] = [
  { kind: "top_up", label: "Top-ups" },
  { kind: "inference_charge", label: "Inference spend" },
  { kind: "data_charge", label: "Data sales" },
  { kind: "revenue_share", label: "Revenue shares" },
  { kind: "grant", label: "Admin grants" },
  { kind: "adjustment", label: "Adjustments" },
];

function flowFor(kind: string) {
  return stats.value?.creditsFlow.find((f) => f.kind === kind);
}
</script>

<template>
  <div>
    <section v-if="!ready" class="card">
      <p class="hint">Loading…</p>
    </section>

    <template v-else>
      <section class="card">
        <h2>Platform stats</h2>
        <p class="hint">
          Derived live from the platform's records: requests, collections,
          the ledger, member conversations, and the module registry. The
          over-time window is the last 30 days; everything else is
          whole-history.
        </p>
        <p v-if="error" class="error-text">{{ error }} <button class="secondary" @click="load">Retry</button></p>
      </section>

      <template v-if="stats">
        <!-- Requests over time: one series, one hue, no legend needed. -->
        <section class="card">
          <h3>Requests over time</h3>
          <p class="hint">Data Consumer commissions per day, last 30 days.</p>
          <div v-if="stats.requestsOverTime.length === 0" class="hint">No requests yet.</div>
          <div
            v-else
            class="bars"
            role="img"
            :aria-label="`Requests per day, ${stats.requestsOverTime[0]!.day} to ${
              stats.requestsOverTime[stats.requestsOverTime.length - 1]!.day
            }, peak ${maxDaily}`"
          >
            <button
              v-for="(d, i) in stats.requestsOverTime"
              :key="d.day"
              type="button"
              class="bar-col"
              @mouseenter="hoverDay = i"
              @mouseleave="hoverDay = null"
              @focus="hoverDay = i"
              @blur="hoverDay = null"
            >
              <span class="bar-tooltip" v-if="hoverDay === i">
                {{ d.day }} — {{ d.count }} request{{ d.count === 1 ? "" : "s" }}
              </span>
              <span class="bar" :style="{ height: barHeight(d.count) + 'px' }"></span>
              <span class="bar-day">{{ d.day.slice(8) }}</span>
            </button>
          </div>
          <table class="stat-table" v-if="stats.requestsOverTime.length > 0">
            <caption class="sr-only">Requests per day (data table)</caption>
            <thead>
              <tr><th scope="col">Day</th><th scope="col" class="num">Requests</th></tr>
            </thead>
            <tbody>
              <tr v-for="d in stats.requestsOverTime" :key="d.day">
                <td>{{ d.day }}</td>
                <td class="num">{{ d.count }}</td>
              </tr>
            </tbody>
          </table>
        </section>

        <!-- Collections: completed vs incomplete headline + status table. -->
        <section class="card">
          <h3>Collections</h3>
          <div class="tiles">
            <div class="tile">
              <div class="tile-value">{{ statusGet(stats.collectionStatuses, "completed") }}</div>
              <div class="tile-label">Completed</div>
            </div>
            <div class="tile">
              <div class="tile-value">{{ statusGet(stats.collectionStatuses, "incomplete") }}</div>
              <div class="tile-label">Incomplete</div>
            </div>
            <div class="tile">
              <div class="tile-value">{{ statusGet(stats.collectionStatuses, "collecting") }}</div>
              <div class="tile-label">Collecting</div>
            </div>
          </div>
          <table class="stat-table">
            <thead>
              <tr><th scope="col">Status</th><th scope="col" class="num">Collections</th></tr>
            </thead>
            <tbody>
              <tr v-for="s in stats.collectionStatuses" :key="s.status">
                <td>{{ s.status }}</td>
                <td class="num">{{ s.count }}</td>
              </tr>
              <tr v-if="stats.collectionStatuses.length === 0">
                <td class="hint" colspan="2">No collections yet.</td>
              </tr>
            </tbody>
          </table>
        </section>

        <!-- Credits flow: one row per Ledger movement kind. -->
        <section class="card">
          <h3>Credits flow</h3>
          <p class="hint">
            Every figure sums the ledger entries of that movement kind —
            top-ups, inference spend, data sales, revenue shares, and the
            admin's grant/adjustment paths.
          </p>
          <table class="stat-table">
            <thead>
              <tr>
                <th scope="col">Movement kind</th>
                <th scope="col" class="num">Credited</th>
                <th scope="col" class="num">Debited</th>
                <th scope="col" class="num">Movements</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="k in FLOW_KINDS" :key="k.kind">
                <td>{{ k.label }}</td>
                <td class="num">{{ flowFor(k.kind) ? formatCredits(flowFor(k.kind)!.inMicros) : "0" }}</td>
                <td class="num">{{ flowFor(k.kind) ? formatCredits(flowFor(k.kind)!.outMicros) : "0" }}</td>
                <td class="num">{{ flowFor(k.kind)?.movements ?? 0 }}</td>
              </tr>
            </tbody>
          </table>
        </section>

        <!-- Channels: conversations and turns per channel. -->
        <section class="card">
          <h3>Channel activity</h3>
          <p class="hint">
            Member conversations per Channel (WhatsApp, Telegram, email, web)
            and the message turns they carry.
          </p>
          <table class="stat-table">
            <thead>
              <tr>
                <th scope="col">Channel</th>
                <th scope="col" class="num">Conversations</th>
                <th scope="col" class="num">Turns</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in stats.channels" :key="c.channel">
                <td>{{ c.channel }}</td>
                <td class="num">{{ c.conversations }}</td>
                <td class="num">{{ c.turns }}</td>
              </tr>
              <tr v-if="stats.channels.length === 0">
                <td class="hint" colspan="3">No member conversations yet.</td>
              </tr>
            </tbody>
          </table>
        </section>

        <!-- Modules: usage by kind and visibility. -->
        <section class="card">
          <h3>Module usage</h3>
          <div class="tiles">
            <div class="tile">
              <div class="tile-value">{{ stats.modules.systemWide }}</div>
              <div class="tile-label">System-wide</div>
            </div>
            <div class="tile">
              <div class="tile-value">{{ stats.modules.private }}</div>
              <div class="tile-label">Private</div>
            </div>
          </div>
          <table class="stat-table">
            <thead>
              <tr><th scope="col">Kind</th><th scope="col" class="num">Modules</th></tr>
            </thead>
            <tbody>
              <tr>
                <td>Agent Skills</td>
                <td class="num">{{ stats.modules.agentSkill }}</td>
              </tr>
              <tr>
                <td>Process Templates</td>
                <td class="num">{{ stats.modules.processTemplate }}</td>
              </tr>
              <tr>
                <td>Connectors</td>
                <td class="num">{{ stats.modules.connector }}</td>
              </tr>
            </tbody>
          </table>
        </section>

        <!-- Request statuses: the clarification pipeline at a glance. -->
        <section class="card">
          <h3>Request statuses</h3>
          <p class="hint">{{ statusTotal(stats.requestStatuses) }} requests on the platform.</p>
          <table class="stat-table">
            <thead>
              <tr><th scope="col">Status</th><th scope="col" class="num">Requests</th></tr>
            </thead>
            <tbody>
              <tr v-for="s in stats.requestStatuses" :key="s.status">
                <td>{{ s.status }}</td>
                <td class="num">{{ s.count }}</td>
              </tr>
              <tr v-if="stats.requestStatuses.length === 0">
                <td class="hint" colspan="2">No requests yet.</td>
              </tr>
            </tbody>
          </table>
        </section>
      </template>
    </template>
  </div>
</template>

<style scoped>
.bars {
  display: flex;
  align-items: flex-end;
  gap: 3px;
  height: 128px;
  padding: 0.25rem 0 0;
  overflow-x: auto;
}
.bar-col {
  position: relative;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: flex-end;
  gap: 4px;
  min-width: 14px;
  flex: 1 0 auto;
  background: none;
  border: 0;
  padding: 0;
  cursor: default;
}
.bar {
  width: 10px;
  border-radius: 4px 4px 0 0;
  background: var(--accent);
  min-height: 2px;
}
.bar-day {
  font-size: 0.6rem;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}
.bar-tooltip {
  position: absolute;
  bottom: 100%;
  left: 50%;
  transform: translateX(-50%);
  white-space: nowrap;
  background: var(--ink);
  color: var(--card);
  font-size: 0.7rem;
  padding: 0.2rem 0.45rem;
  border-radius: 4px;
  pointer-events: none;
  z-index: 1;
}
.tiles {
  display: flex;
  gap: 1rem;
  flex-wrap: wrap;
  margin: 0.5rem 0 0.75rem;
}
.tile {
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 0.6rem 1rem;
  min-width: 7rem;
}
.tile-value {
  font-size: 1.4rem;
  font-weight: 700;
}
.tile-label {
  font-size: 0.75rem;
  color: var(--muted);
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.stat-table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 0.5rem;
}
.stat-table th,
.stat-table td {
  text-align: left;
  padding: 0.35rem 0.5rem 0.35rem 0;
  border-bottom: 1px solid var(--line);
}
.stat-table th {
  font-size: 0.75rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--muted);
}
.stat-table .num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  overflow: hidden;
  clip: rect(0 0 0 0);
}
</style>
