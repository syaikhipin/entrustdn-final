<script setup lang="ts">
// Farmer Organization home: the shared-data dashboard (ticket 04). Upload
// streams through the backend (ADR 0006) and the anonymize stage cleans
// identifiers at ingest (ticket 05); downloads stream from the platform,
// never from raw storage. The org's pseudonym map is listed below and
// erasable — the GDPR surface (ADR 0005). Ticket 06 adds the taxonomy
// surfaces: uploads auto-categorize, and the org corrects categories on its
// own assets (the correction is the full desired set — the truth, not a
// patch).
import { useSession } from "~/composables/useSession";
import {
  assetDownloadURL,
  deleteAsset,
  errFrom,
  formatBytes,
  formatDate,
  listAssets,
  updateAsset,
  uploadAsset,
  type Asset,
} from "~/assets";
import { erasePseudonyms, listPseudonyms, type PseudonymList } from "~/pseudonyms";
import { listMembers, type Member } from "~/roster";
import {
  createCollection,
  listCollections,
  startGatheringConversation,
  statusLabel,
  syncCollection,
  type Collection,
} from "~/collections";
import { fetchMyRequests, type DataRequest } from "~/requests";
import {
  fetchTerms,
  setCategories,
  TAXONOMY_CATEGORIES,
  type TaxonomyCategory,
  type Term,
} from "~/taxonomy";

const { token, me, account, restore } = useSession();
const backendURL = useBackendURL();

const ready = ref(false);
const assets = ref<Asset[]>([]);
const loadError = ref<string | null>(null);
const notice = ref<string | null>(null);
const error = ref<string | null>(null);
const busy = ref(false);

// Pseudonym map state (ticket 05).
const pseudonyms = ref<PseudonymList | null>(null);
const confirmingErase = ref(false);

// Upload form state.
const file = ref<File | null>(null);
const name = ref("");
const description = ref("");
const source = ref("");
const collectedAt = ref("");
const notes = ref("");

// Edit state: which asset id is open, and its editable fields.
const editing = ref<string | null>(null);
const editName = ref("");
const editDescription = ref("");
const editSource = ref("");
const editCollectedAt = ref("");
const editNotes = ref("");

// Delete confirmation state.
const confirmingDelete = ref<string | null>(null);

// Taxonomy state (ticket 06): the public vocabulary, and per-asset
// correction state — the open asset id, each category's chosen term, and
// the corrections being applied.
const terms = ref<Term[]>([]);
const correcting = ref<string | null>(null);
const correctionPicks = ref<Partial<Record<TaxonomyCategory, string>>>({});
const correctionError = ref<string | null>(null);

// Collections state (ticket 12): the roster and clarified requests the
// creation form picks from, and the collections themselves.
const members = ref<Member[]>([]);
const clarifiedRequests = ref<DataRequest[]>([]);
const collections = ref<Collection[]>([]);
const colRequestID = ref("");
const colMemberIDs = ref<string[]>([]);
const colQuestions = ref("");
const colDeadline = ref("");
const colError = ref<string | null>(null);
const syncing = ref<string | null>(null);

onMounted(async () => {
  await restore();
  ready.value = true;
  if (token.value) {
    await refreshAssets();
    await refreshPseudonyms();
    await refreshTerms();
    await refreshCollections();
  }
});

// The guard waits for restore(): before it finishes, a refreshing user has
// a token but no parsed identity, and bouncing then would strand them at
// /login. Only a completed restore with no account is truly signed out.
watch(ready, (isReady) => {
  if (isReady && !account.value) navigateTo("/login");
});

async function refreshAssets() {
  if (!token.value) return;
  loadError.value = null;
  try {
    assets.value = await listAssets(backendURL, token.value);
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  }
}

async function refreshPseudonyms() {
  if (!token.value) return;
  try {
    pseudonyms.value = await listPseudonyms(backendURL, token.value);
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e);
  }
}

// The vocabulary is public; its labels drive the correction pickers.
async function refreshTerms() {
  try {
    terms.value = await fetchTerms(backendURL);
  } catch (e) {
    // A missing vocabulary degrades to upload-only: corrections hide.
    terms.value = [];
    loadError.value = e instanceof Error ? e.message : String(e);
  }
}

// termsFor lists the vocabulary under one axis for a picker.
function termsFor(category: TaxonomyCategory): Term[] {
  return terms.value.filter((t) => t.category === category);
}

// --- Collections (ticket 12) ---

// refreshCollections loads the roster, the org's clarified requests, and
// the collections themselves. The roster and requests feed the creation
// form; their failures degrade to a create-form-less list.
async function refreshCollections() {
  if (!token.value) return;
  try {
    members.value = await listMembers(backendURL, token.value);
  } catch (e) {
    members.value = [];
    colError.value = e instanceof Error ? e.message : String(e);
  }
  try {
    const reqs = await fetchMyRequests(backendURL, token.value);
    clarifiedRequests.value = reqs.filter((r) => r.status === "clarified");
  } catch (e) {
    clarifiedRequests.value = [];
    colError.value = e instanceof Error ? e.message : String(e);
  }
  try {
    collections.value = await listCollections(backendURL, token.value);
  } catch (e) {
    colError.value = e instanceof Error ? e.message : String(e);
  }
}

// submitCollection opens a Collection: one question per line, members
// multi-picked from the roster.
async function submitCollection() {
  if (!token.value) return;
  busy.value = true;
  colError.value = null;
  notice.value = null;
  try {
    const questions = colQuestions.value
      .split("\n")
      .map((q) => q.trim())
      .filter((q) => q !== "");
    const created = await createCollection(backendURL, token.value, {
      requestId: colRequestID.value,
      memberIds: colMemberIDs.value,
      questions,
      deadline: colDeadline.value ? new Date(colDeadline.value).toISOString() : "",
    });
    notice.value = `Collection opened — ${created.items.length} items to gather. Open each member's conversation from the roster, and sync here as answers arrive.`;
    colRequestID.value = "";
    colMemberIDs.value = [];
    colQuestions.value = "";
    colDeadline.value = "";
    await refreshCollections();
  } catch (e) {
    colError.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

// submitSync runs the gathering step for one collection: ingests new
// answers, opens re-asks, finalizes when the triggers say so.
async function submitSync(id: string) {
  if (!token.value) return;
  syncing.value = id;
  colError.value = null;
  try {
    const got = await syncCollection(backendURL, token.value, id);
    notice.value =
      got.status === "completed"
        ? "Collection completed — every item holds an accepted answer."
        : got.status === "incomplete"
          ? "Collection flagged incomplete — the missing-data summary shows the gaps."
          : "Synced — new answers ingested, re-asks opened where needed.";
    await refreshCollections();
  } catch (e) {
    colError.value = e instanceof Error ? e.message : String(e);
  } finally {
    syncing.value = null;
  }
}

// startConversations opens each member's gathering conversation for one
// collection — every conversation carries the request ID so the sync
// matches it. Members already covered by an open conversation (the sync
// shows their items with rounds) are skipped: asking twice is over-surveying.
async function startConversations(c: Collection) {
  if (!token.value) return;
  syncing.value = c.id;
  colError.value = null;
  notice.value = null;
  try {
    let opened = 0;
    for (const it of c.items) {
      if (it.rounds.length > 0 || it.status !== "collecting") continue;
      await startGatheringConversation(backendURL, token.value, {
        memberId: it.memberId,
        memberName: it.memberName,
        requestId: c.requestId,
        questions: [it.question],
      });
      opened++;
    }
    notice.value =
      opened > 0
        ? `Opened ${opened} conversation${opened === 1 ? "" : "s"} — hand each member their resumable link, then sync here as answers arrive.`
        : "Every member already has a conversation — sync to pick up new answers.";
    await refreshCollections();
  } catch (e) {
    colError.value = e instanceof Error ? e.message : String(e);
  } finally {
    syncing.value = null;
  }
}

// itemProgress renders one item's standing for the collection table.
function itemProgress(status: string): string {
  const labels: Record<string, string> = {
    collecting: "waiting",
    accepted: "accepted",
    blocked: "blocked",
  };
  return labels[status] ?? status;
}

// startCorrection opens the correction row seeded with the asset's current
// assignments (the classifier's suggestions included — the org edits from
// where the machine left off).
function startCorrection(a: Asset) {
  correcting.value = a.id;
  correctionError.value = null;
  const picks: Partial<Record<TaxonomyCategory, string>> = {};
  for (const as of a.categories) picks[as.category] = as.termId;
  correctionPicks.value = picks;
}

// submitCorrection PUTs the full desired set — untouched axes keep their
// current pick, cleared axes drop their assignment.
async function submitCorrection(assetId: string) {
  if (!token.value) return;
  busy.value = true;
  correctionError.value = null;
  const corrections = Object.entries(correctionPicks.value)
    .filter(([, termId]) => termId)
    .map(([category, termId]) => ({ category: category as TaxonomyCategory, termId: termId! }));
  try {
    await setCategories(backendURL, token.value, assetId, corrections);
    correcting.value = null;
    notice.value = "Categories corrected — the catalog reflects the new labels immediately.";
    await refreshAssets();
  } catch (e) {
    correctionError.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

async function submitErasePseudonyms() {
  if (!token.value) return;
  busy.value = true;
  error.value = null;
  try {
    await erasePseudonyms(backendURL, token.value);
    confirmingErase.value = false;
    notice.value =
      "Pseudonym map erased — every identifier anonymized so far gets a fresh pseudonym next time it appears.";
    await refreshPseudonyms();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

function pickFile(event: Event) {
  const input = event.target as HTMLInputElement;
  file.value = input.files?.[0] ?? null;
  // Default the display name from the chosen file.
  if (file.value && !name.value) name.value = file.value.name.replace(/\.[^.]+$/, "");
}

async function submitUpload() {
  if (!token.value || !file.value) {
    error.value = "Choose a file to upload.";
    return;
  }
  busy.value = true;
  error.value = null;
  notice.value = null;
  try {
    const created = await uploadAsset(backendURL, token.value, {
      file: file.value,
      name: name.value,
      description: description.value,
      source: source.value,
      // datetime-local gives a wall-clock time with no zone; new Date()
      // reads it as browser-local, and toISOString converts to UTC — the
      // instant the user picked, rendered in RFC 3339.
      collectedAt: collectedAt.value ? new Date(collectedAt.value).toISOString() : "",
      notes: notes.value,
    });
    notice.value = `Uploaded “${created.name}” — it passed through: ${created.pipeline.join(", ")}.`;
    file.value = null;
    name.value = "";
    description.value = "";
    source.value = "";
    collectedAt.value = "";
    notes.value = "";
    await refreshAssets();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

function startEdit(a: Asset) {
  editing.value = a.id;
  editName.value = a.name;
  editDescription.value = a.description;
  editSource.value = a.provenance.source;
  editCollectedAt.value = a.provenance.collectedAt ? a.provenance.collectedAt.slice(0, 16) : "";
  editNotes.value = a.provenance.notes;
}

async function submitEdit(id: string) {
  if (!token.value) return;
  busy.value = true;
  error.value = null;
  try {
    const updated = await updateAsset(backendURL, token.value, id, {
      name: editName.value,
      description: editDescription.value,
      source: editSource.value,
      collectedAt: editCollectedAt.value ? new Date(editCollectedAt.value).toISOString() : "",
      notes: editNotes.value,
    });
    const idx = assets.value.findIndex((a) => a.id === id);
    if (idx >= 0) assets.value[idx] = updated;
    editing.value = null;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

async function submitDelete(id: string) {
  if (!token.value) return;
  busy.value = true;
  error.value = null;
  try {
    await deleteAsset(backendURL, token.value, id);
    assets.value = assets.value.filter((a) => a.id !== id);
    confirmingDelete.value = null;
    notice.value = "Asset deleted — its stored file is gone too.";
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  } finally {
    busy.value = false;
  }
}

function downloadURL(id: string): string {
  return assetDownloadURL(backendURL, id);
}

// Download via fetch so the Bearer session rides along (a plain link can't
// send headers), then hand the bytes to the browser as a blob URL. The
// backend streams the file either way (ADR 0006).
async function openDownload(a: Asset) {
  if (!token.value) return;
  error.value = null;
  try {
    const resp = await fetch(downloadURL(a.id), {
      headers: { Authorization: `Bearer ${token.value}` },
    });
    if (!resp.ok) {
      const doc = await resp.json().catch(() => ({}));
      throw new Error(errFrom(doc));
    }
    const blob = await resp.blob();
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = a.name;
    document.body.appendChild(link);
    link.click();
    link.remove();
    URL.revokeObjectURL(url);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}
</script>

<template>
  <div>
    <section v-if="!ready" class="card">
      <p class="hint">Loading…</p>
    </section>

    <section v-else-if="account?.status === 'pending_approval'" class="card">
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
        <h2>Shared-data dashboard</h2>
        <p>
          Welcome, <strong>{{ me.account.displayName }}</strong
          >. These are the Data Assets your organization shares. Every upload
          passes through the platform's anonymization stage before it is
          stored; downloads stream from the platform, never from raw storage.
        </p>
      </section>

      <section v-if="notice" class="card">
        <p class="ok-text">{{ notice }}</p>
      </section>
      <section v-if="error" class="card error">
        <p class="error-text">{{ error }}</p>
      </section>

      <!-- Upload -->
      <section class="card">
        <h3>Upload a Data Asset</h3>
        <form @submit.prevent="submitUpload">
          <label>
            File
            <input type="file" @change="pickFile" required />
          </label>
          <label>
            Name
            <input v-model="name" type="text" placeholder="e.g. Herd registry" />
          </label>
          <label>
            Description
            <textarea v-model="description" rows="2" placeholder="What is in this dataset?"></textarea>
          </label>
          <label>
            Provenance — source
            <input v-model="source" type="text" placeholder="e.g. Teagasc Moorepark trial" />
          </label>
          <label>
            Provenance — collected at
            <input v-model="collectedAt" type="datetime-local" />
          </label>
          <label>
            Provenance — notes
            <input v-model="notes" type="text" placeholder="Anything else a buyer should know" />
          </label>
          <button class="primary" type="submit" :disabled="busy || !file">
            {{ busy ? "Uploading…" : "Upload" }}
          </button>
        </form>
      </section>

      <!-- Dashboard -->
      <section class="card">
        <h3>Your Data Assets</h3>
        <p v-if="loadError" class="error-text">{{ loadError }}</p>
        <p v-else-if="assets.length === 0" class="hint">
          Nothing shared yet — upload your first dataset above.
        </p>
        <table v-else class="assets">
          <thead>
            <tr>
              <th>Name</th>
              <th>Format</th>
              <th>Size</th>
              <th>Categories</th>
              <th>Anonymization</th>
              <th>Uploaded</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <template v-for="a in assets" :key="a.id">
              <tr>
                <td>
                  <strong>{{ a.name }}</strong>
                  <p v-if="a.description" class="hint">{{ a.description }}</p>
                  <p v-if="a.provenance.source || a.provenance.notes" class="hint">
                    Source: {{ a.provenance.source || "—" }}
                    <template v-if="a.provenance.collectedAt">
                      · collected {{ formatDate(a.provenance.collectedAt) }}</template
                    >
                  </p>
                </td>
                <td><span class="pill ok">{{ a.format }}</span></td>
                <td>{{ formatBytes(a.sizeBytes) }}</td>
                <td class="cats">
                  <template v-if="a.categories.length">
                    <span
                      v-for="as in a.categories"
                      :key="as.category"
                      class="pill"
                      :title="`${as.category} · ${as.source} (${Math.round(as.confidence * 100)}%)`"
                      >{{ as.label }}</span
                    >
                  </template>
                  <span v-else class="hint">uncategorized</span>
                </td>
                <td>
                  <span class="pill">{{ a.pipeline.join(" → ") }}</span>
                </td>
                <td>{{ formatDate(a.createdAt) }}</td>
                <td class="actions">
                  <a href="#" @click.prevent="openDownload(a)">Download</a>
                  <button v-if="terms.length" class="secondary" @click="startCorrection(a)">Categories</button>
                  <button class="secondary" @click="startEdit(a)">Edit</button>
                  <button class="danger" @click="confirmingDelete = a.id">Delete</button>
                </td>
              </tr>
              <tr v-if="correcting === a.id">
                <td :colspan="terms.length ? 7 : 6">
                  <form class="edit-form" @submit.prevent="submitCorrection(a.id)">
                    <p class="hint">
                      Correct what the classifier guessed — the saved set
                      replaces every category on this asset.
                    </p>
                    <label v-for="cat in TAXONOMY_CATEGORIES" :key="cat">
                      {{ cat.replaceAll("_", " ") }}
                      <select v-model="correctionPicks[cat]">
                        <option value="">— none —</option>
                        <option v-for="t in termsFor(cat)" :key="t.id" :value="t.id">{{ t.label }}</option>
                      </select>
                    </label>
                    <p v-if="correctionError" class="error-text">{{ correctionError }}</p>
                    <div class="edit-actions">
                      <button class="primary" type="submit" :disabled="busy">Save categories</button>
                      <button class="secondary" type="button" @click="correcting = null">Cancel</button>
                    </div>
                  </form>
                </td>
              </tr>
              <tr v-if="editing === a.id">
                <td colspan="7">
                  <form class="edit-form" @submit.prevent="submitEdit(a.id)">
                    <label>
                      Name
                      <input v-model="editName" type="text" required />
                    </label>
                    <label>
                      Description
                      <textarea v-model="editDescription" rows="2"></textarea>
                    </label>
                    <label>
                      Provenance — source
                      <input v-model="editSource" type="text" />
                    </label>
                    <label>
                      Provenance — collected at
                      <input v-model="editCollectedAt" type="datetime-local" />
                    </label>
                    <label>
                      Provenance — notes
                      <input v-model="editNotes" type="text" />
                    </label>
                    <div class="edit-actions">
                      <button class="primary" type="submit" :disabled="busy">Save</button>
                      <button class="secondary" type="button" @click="editing = null">Cancel</button>
                    </div>
                  </form>
                </td>
             </tr>
              <tr v-if="confirmingDelete === a.id">
                <td colspan="7">
                  <p>
                    Delete <strong>{{ a.name }}</strong
                    >? The stored file is removed from the platform along with
                    this record. This cannot be undone.
                  </p>
                  <div class="edit-actions">
                    <button class="danger" :disabled="busy" @click="submitDelete(a.id)">Yes, delete</button>
                    <button class="secondary" @click="confirmingDelete = null">Cancel</button>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </section>

      <!-- Collections (ticket 12) -->
      <section class="card">
        <h3>Data Collections</h3>
        <p>
          Turn a clarified Request into a gathering effort: pick the members,
          list the questions, and sync as answers arrive. Quality triggers
          check every answer — bad ones get re-asked automatically, and a
          member who can't produce a good answer blocks their item with the
          reason surfaced.
        </p>
        <p v-if="colError" class="error-text">{{ colError }}</p>

        <form v-if="members.length && clarifiedRequests.length" class="collection-form" @submit.prevent="submitCollection">
          <h4>Open a collection</h4>
          <label>
            Request
            <select v-model="colRequestID" required>
              <option value="" disabled>Choose a clarified request…</option>
              <option v-for="r in clarifiedRequests" :key="r.id" :value="r.id">
                {{ r.description }} ({{ r.format }})
              </option>
            </select>
          </label>
          <label>
            Members
            <div class="member-picks">
              <label v-for="m in members" :key="m.id" class="member-pick">
                <input v-model="colMemberIDs" type="checkbox" :value="m.id" />
                {{ m.displayName }} <span class="hint">{{ m.contact }}</span>
              </label>
            </div>
          </label>
          <label>
            Questions — one per line
            <textarea v-model="colQuestions" rows="3" placeholder="What is your farm size?&#10;Which crops did you sow?" required></textarea>
          </label>
          <label>
            Deadline (optional)
            <input v-model="colDeadline" type="datetime-local" />
          </label>
          <button class="primary" type="submit" :disabled="busy || !colRequestID || !colMemberIDs.length">
            {{ busy ? "Opening…" : "Open collection" }}
          </button>
          <p class="hint">
            After opening, "Start conversations" asks each member your
            questions over their channel; "Sync" picks the answers up.
          </p>
        </form>
        <p v-else class="hint">
          Collections need roster members and a clarified request —
          {{ members.length ? "no clarified request yet" : "no members on the roster yet" }}.
        </p>

        <p v-if="collections.length === 0" class="hint">No collections yet.</p>
        <table v-else class="assets">
          <thead>
            <tr>
              <th>Request</th>
              <th>Status</th>
              <th>Items</th>
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
              <td class="items-cell">
                <span v-for="it in c.items" :key="it.memberId + it.question" class="item-line">
                  <strong>{{ it.memberName }}</strong> — {{ it.question }}:
                  <span class="pill" :class="it.status === 'accepted' ? 'ok' : it.status === 'blocked' ? 'bad' : ''">
                    {{ itemProgress(it.status) }}
                  </span>
                  <span v-if="it.reasks" class="hint">({{ it.reasks }} re-ask{{ it.reasks === 1 ? "" : "s" }})</span>
                </span>
              </td>
              <td class="items-cell">
                <template v-if="c.missing.length">
                  <span v-for="m in c.missing" :key="m.memberName + m.question" class="item-line">
                    <strong>{{ m.memberName }}</strong> — {{ m.question }}: {{ m.reason }}
                  </span>
                </template>
                <span v-else class="hint">—</span>
              </td>
              <td>{{ formatDate(c.updatedAt) }}</td>
              <td class="actions">
                <button
                  v-if="c.status === 'collecting'"
                  class="secondary"
                  :disabled="syncing === c.id"
                  @click="startConversations(c)"
                >
                  {{ syncing === c.id ? "Starting…" : "Start conversations" }}
                </button>
                <button
                  v-if="c.status === 'collecting'"
                  class="secondary"
                  :disabled="syncing === c.id"
                  @click="submitSync(c.id)"
                >
                  {{ syncing === c.id ? "Syncing…" : "Sync" }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <!-- Pseudonym map (ticket 05) -->
      <section class="card">
        <h3>Pseudonym map</h3>
        <p>
          When your data is anonymized, identifiers (names, phones, emails)
          are replaced with opaque pseudonyms that only this map can trace —
          the map stays inside the platform and is never delivered with your
          data. Erasing it is the erasure request under GDPR: every erased
          identifier gets a fresh pseudonym next time it appears, so old
          pseudonyms stop linking.
        </p>
        <p v-if="pseudonyms" class="hint">
          {{ pseudonyms.total }}
          {{ pseudonyms.total === 1 ? "identifier" : "identifiers" }} held for
          your organization.
        </p>
        <div v-if="pseudonyms && pseudonyms.entries.length" class="edit-actions">
          <button class="danger" :disabled="busy" @click="confirmingErase = true">
            Erase the whole map
          </button>
        </div>
        <template v-if="confirmingErase">
          <p>
            Erase every pseudonym for your organization? Old pseudonyms in
            already delivered data will no longer link to anything, and each
            identifier is re-pseudonymized fresh on its next appearance. This
            cannot be undone.
          </p>
          <div class="edit-actions">
            <button class="danger" :disabled="busy" @click="submitErasePseudonyms">
              Yes, erase the map
            </button>
            <button class="secondary" @click="confirmingErase = false">Cancel</button>
          </div>
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
table.assets {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.92rem;
}
table.assets th {
  text-align: left;
  color: var(--muted);
  font-weight: 600;
  padding: 0.4rem 0.5rem;
  border-bottom: 1px solid var(--line);
}
table.assets td {
  padding: 0.6rem 0.5rem;
  border-bottom: 1px solid var(--line);
  vertical-align: top;
}
.actions {
  white-space: nowrap;
  text-align: right;
}
.actions a,
.actions button {
  margin-left: 0.4rem;
}
.edit-form label {
  margin-bottom: 0.6rem;
}
.collection-form h4 {
  margin: 0 0 0.5rem;
}
.member-picks {
  display: flex;
  flex-wrap: wrap;
  gap: 0.3rem 1rem;
}
.member-pick {
  display: block;
  font-weight: normal;
  margin: 0;
}
.items-cell {
  max-width: 22rem;
}
.item-line {
  display: block;
  font-size: 0.88rem;
  margin-bottom: 0.2rem;
}
code.small {
  font-size: 0.82rem;
}
.edit-actions {
  display: flex;
  gap: 0.5rem;
}
.pill:not(.ok):not(.warn):not(.bad) {
  background: var(--bg);
  color: var(--muted);
}
.cats .pill {
  margin-right: 0.3rem;
}
</style>
