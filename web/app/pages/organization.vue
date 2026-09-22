<script setup lang="ts">
// Farmer Organization home: the shared-data dashboard (ticket 04). Upload
// streams through the backend (ADR 0006) and the anonymize stage cleans
// identifiers at ingest (ticket 05); downloads stream from the platform,
// never from raw storage. The org's pseudonym map is listed below and
// erasable — the GDPR surface (ADR 0005).
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

onMounted(async () => {
  await restore();
  ready.value = true;
  if (token.value) {
    await refreshAssets();
    await refreshPseudonyms();
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
                <td>
                  <span class="pill">{{ a.pipeline.join(" → ") }}</span>
                </td>
                <td>{{ formatDate(a.createdAt) }}</td>
                <td class="actions">
                  <a href="#" @click.prevent="openDownload(a)">Download</a>
                  <button class="secondary" @click="startEdit(a)">Edit</button>
                  <button class="danger" @click="confirmingDelete = a.id">Delete</button>
                </td>
              </tr>
              <tr v-if="editing === a.id">
                <td colspan="6">
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
                <td colspan="6">
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
.edit-actions {
  display: flex;
  gap: 0.5rem;
}
.pill:not(.ok):not(.warn):not(.bad) {
  background: var(--bg);
  color: var(--muted);
}
</style>
