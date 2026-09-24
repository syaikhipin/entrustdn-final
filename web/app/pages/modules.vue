<script setup lang="ts">
// Modules (ticket 08): any active account is a Module Author. Upload a
// versioned Module — manifest (kind, version, A2A capability description)
// plus markdown and/or configuration; it is private to you until you grant
// access, and the Platform Admin promotes reviewed Modules system-wide.
// Everything here is shown, never run.
import {
  MODULE_KINDS,
  deprecateModuleVersion,
  errFrom,
  fetchModuleGrants,
  fetchModuleVersions,
  fetchMyModules,
  fetchSystemModules,
  formatModule,
  grantModule,
  promoteModule,
  publishModuleVersion,
  revokeModule,
  uploadModule,
  type ModuleKind,
  type ModuleGrant,
  type StoredModule,
} from "~/modules";
import { useSession } from "~/composables/useSession";

const backendURL = useBackendURL();
const { token, me, account, restore } = useSession();

const ready = ref(false);
const error = ref<string | null>(null);
const mine = ref<StoredModule[]>([]);
const world = ref<StoredModule[]>([]);

// Upload form state.
const showUpload = ref(false);
const name = ref("");
const kind = ref<ModuleKind>("process_template");
const version = ref("1.0.0");
const capability = ref("");
const content = ref("");
const config = ref("");
const uploading = ref(false);

// One open module at a time: its version history and grants.
const openId = ref<string | null>(null);
const versions = ref<StoredModule[]>([]);
const grants = ref<ModuleGrant[]>([]);
const grantEmail = ref("");
const detailError = ref<string | null>(null);
const nextVersion = ref("");
const nextCapability = ref("");
const nextContent = ref("");
const nextConfig = ref("");
const publishing = ref(false);
const notice = ref<string | null>(null);

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
    [mine.value, world.value] = await Promise.all([
      fetchMyModules(backendURL, token.value),
      fetchSystemModules(backendURL, token.value),
    ]);
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  }
}

const canUpload = computed(
  () =>
    /^[a-z0-9][a-z0-9-]*$/.test(name.value) &&
    capability.value.trim() !== "" &&
    (content.value.trim() !== "" || config.value.trim() !== "") &&
    !uploading.value,
);

async function submitUpload() {
  if (!token.value || !canUpload.value) return;
  uploading.value = true;
  error.value = null;
  try {
    await uploadModule(backendURL, token.value, {
      name: name.value,
      kind: kind.value,
      version: version.value,
      capability: capability.value,
      content: content.value,
      config: config.value,
    });
    name.value = "";
    capability.value = "";
    content.value = "";
    config.value = "";
    showUpload.value = false;
    await load();
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  } finally {
    uploading.value = false;
  }
}

const openModule = computed(() => mine.value.find((m) => m.moduleId === openId.value) ?? null);

async function open(moduleId: string) {
  openId.value = moduleId;
  detailError.value = null;
  notice.value = null;
  versions.value = [];
  grants.value = [];
  if (!token.value) return;
  try {
    versions.value = await fetchModuleVersions(backendURL, token.value, moduleId);
    grants.value = await fetchModuleGrants(backendURL, token.value, moduleId);
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

function close() {
  openId.value = null;
}

async function addGrant() {
  if (!token.value || !openId.value || grantEmail.value.trim() === "") return;
  detailError.value = null;
  try {
    await grantModule(backendURL, token.value, openId.value, grantEmail.value.trim());
    grantEmail.value = "";
    grants.value = await fetchModuleGrants(backendURL, token.value, openId.value);
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

async function removeGrant(email: string) {
  if (!token.value || !openId.value) return;
  detailError.value = null;
  try {
    await revokeModule(backendURL, token.value, openId.value, email);
    grants.value = await fetchModuleGrants(backendURL, token.value, openId.value);
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

const canPublish = computed(
  () =>
    nextVersion.value.trim() !== "" &&
    nextCapability.value.trim() !== "" &&
    (nextContent.value.trim() !== "" || nextConfig.value.trim() !== "") &&
    !publishing.value,
);

async function publish() {
  if (!token.value || !openId.value || !canPublish.value) return;
  publishing.value = true;
  detailError.value = null;
  try {
    await publishModuleVersion(backendURL, token.value, openId.value, {
      version: nextVersion.value,
      capability: nextCapability.value,
      content: nextContent.value,
      config: nextConfig.value,
    });
    nextVersion.value = "";
    nextCapability.value = "";
    nextContent.value = "";
    nextConfig.value = "";
    versions.value = await fetchModuleVersions(backendURL, token.value, openId.value);
    await load();
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : errFrom(e);
  } finally {
    publishing.value = false;
  }
}

async function toggleDeprecated(v: StoredModule) {
  if (!token.value || !openId.value) return;
  detailError.value = null;
  try {
    await deprecateModuleVersion(backendURL, token.value, openId.value, v.id, !v.deprecated);
    versions.value = await fetchModuleVersions(backendURL, token.value, openId.value);
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : errFrom(e);
  }
}

// Admin promotion is the admin page's job, but an admin visiting this page
// gets the button here too — the review-and-promote loop stays one click.
const isAdmin = computed(() => account.value?.role === "platform_admin");

async function togglePromote(m: StoredModule) {
  if (!token.value || !isAdmin.value) return;
  error.value = null;
  try {
    await promoteModule(backendURL, token.value, m.moduleId, !m.systemWide);
    await load();
    if (openId.value === m.moduleId) await open(m.moduleId);
    notice.value = m.systemWide ? "Module demoted." : "Module promoted system-wide.";
  } catch (e) {
    error.value = e instanceof Error ? e.message : errFrom(e);
  }
}
</script>

<template>
  <div>
    <section v-if="!ready" class="card">
      <p class="hint">Loading…</p>
    </section>

    <template v-else-if="me">
      <section class="card">
        <h2>Modules</h2>
        <p>
          Upload versioned Modules — an Agent Skill, a Process Template, or a
          Connector. A Module is a manifest (with an A2A capability
          description) plus markdown and/or configuration: it is shown, never
          run. New Modules are private to you; grant access to colleagues by
          email, and the Platform Admin promotes reviewed Modules
          system-wide.
        </p>
        <button v-if="!showUpload" @click="showUpload = true">Upload a module</button>
        <form v-else class="upload" @submit.prevent="submitUpload">
          <div class="row">
            <label>
              Name (lowercase slug)
              <input v-model="name" type="text" placeholder="barley-survey" required />
            </label>
            <label>
              Kind
              <select v-model="kind">
                <option v-for="k in MODULE_KINDS" :key="k" :value="k">{{ k }}</option>
              </select>
            </label>
            <label>
              Version
              <input v-model="version" type="text" placeholder="1.0.0" required />
            </label>
          </div>
          <label>
            Capability description (A2A — what this module lets the agent do)
            <input
              v-model="capability"
              type="text"
              placeholder="Runs a spring-barley yield survey workflow for Requests."
              required
            />
          </label>
          <label>
            Content (markdown)
            <textarea
              v-model="content"
              rows="5"
              placeholder="# Barley survey&#10;&#10;Ask county first, then field count."
            ></textarea>
          </label>
          <label>
            Configuration (optional)
            <textarea
              v-model="config"
              rows="3"
              placeholder="source:&#10;  type: csv&#10;  url: https://example.org/parcels.csv"
            ></textarea>
          </label>
          <p class="hint">Executable content (scripts, shebangs) is refused.</p>
          <div class="row">
            <button type="submit" :disabled="!canUpload">Upload</button>
            <button type="button" class="secondary" @click="showUpload = false">Cancel</button>
          </div>
        </form>
        <p v-if="notice" class="ok-text">{{ notice }}</p>
        <p v-if="error" class="error-text">{{ error }}</p>
      </section>

      <!-- The open module: versions + grants -->
      <section v-if="openModule" class="card">
        <h3>{{ openModule.name }}</h3>
        <p class="hint">
          {{ formatModule(openModule) }} · {{ openModule.capability }}
          <template v-if="openModule.systemWide"> · <span class="pill ok">system-wide</span></template>
        </p>
        <p v-if="detailError" class="error-text">{{ detailError }}</p>

        <h4>Versions</h4>
        <table class="ledger">
          <tbody>
            <tr v-for="v in versions" :key="v.id">
              <td>
                <strong>v{{ v.version }}</strong>
                <span v-if="v.deprecated" class="pill warn">deprecated</span>
                <p class="hint">{{ v.capability }}</p>
              </td>
              <td class="num">
                <button class="secondary" @click="toggleDeprecated(v)">
                  {{ v.deprecated ? "Restore" : "Deprecate" }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>

        <details class="publish">
          <summary>Publish a new version</summary>
          <label>
            Version
            <input v-model="nextVersion" type="text" placeholder="1.1.0" />
          </label>
          <label>
            Capability description
            <input v-model="nextCapability" type="text" />
          </label>
          <label>
            Content (markdown)
            <textarea v-model="nextContent" rows="4"></textarea>
          </label>
          <label>
            Configuration
            <textarea v-model="nextConfig" rows="2"></textarea>
          </label>
          <button :disabled="!canPublish" @click="publish">
            {{ publishing ? "Publishing…" : "Publish version" }}
          </button>
        </details>

        <h4>Access</h4>
        <p v-if="grants.length === 0" class="hint">
          Private — only you can see this module. Grant access below.
        </p>
        <ul v-else class="grants">
          <li v-for="g in grants" :key="g.accountId">
            <span class="hint">{{ g.accountId }}</span>
            <button class="link" @click="removeGrant(g.accountId)">revoke</button>
          </li>
        </ul>
        <form class="grant-form" @submit.prevent="addGrant">
          <input v-model="grantEmail" type="email" placeholder="colleague@example.org" />
          <button type="submit" :disabled="grantEmail.trim() === ''">Grant access</button>
        </form>

        <button v-if="isAdmin" class="secondary" @click="togglePromote(openModule)">
          {{ openModule.systemWide ? "Demote" : "Promote system-wide" }}
        </button>
        <button class="secondary" @click="close">Close</button>
      </section>

      <section class="card">
        <h3>Your modules</h3>
        <p v-if="mine.length === 0" class="hint">No modules yet — upload one above.</p>
        <table v-else class="ledger">
          <thead>
            <tr>
              <th>Module</th>
              <th>Manifest</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in mine" :key="m.id">
              <td>
                <strong>{{ m.name }}</strong>
                <p class="hint">{{ m.capability }}</p>
              </td>
              <td>
                {{ formatModule(m) }}
                <span v-if="m.systemWide" class="pill ok">system-wide</span>
              </td>
              <td>
                <button v-if="m.moduleId !== openId" class="secondary" @click="open(m.moduleId)">
                  Open
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <section class="card">
        <h3>System-wide registry</h3>
        <p v-if="world.length === 0" class="hint">
          Nothing promoted yet — the Platform Admin promotes reviewed modules here.
        </p>
        <table v-else class="ledger">
          <tbody>
            <tr v-for="m in world" :key="m.id">
              <td>
                <strong>{{ m.name }}</strong>
                <p class="hint">{{ m.capability }}</p>
              </td>
              <td>{{ formatModule(m) }}</td>
            </tr>
          </tbody>
        </table>
      </section>
    </template>
  </div>
</template>

<style scoped>
.upload {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  margin-top: 0.75rem;
}
.upload .row,
.row {
  display: flex;
  gap: 1rem;
}
.upload .row label {
  flex: 1;
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
}
.publish {
  margin: 0.75rem 0;
}
.grants {
  list-style: none;
  padding: 0;
}
.grants li {
  display: flex;
  gap: 0.75rem;
  align-items: center;
  padding: 0.25rem 0;
}
.grant-form {
  display: flex;
  gap: 0.5rem;
  margin: 0.5rem 0 0.75rem;
}
.grant-form input {
  flex: 1;
  margin-top: 0;
}
h4 {
  margin: 1rem 0 0.5rem;
}
</style>
