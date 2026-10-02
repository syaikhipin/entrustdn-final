<script setup lang="ts">
// App shell: shared chrome around every page. NuxtPage carries the routes
// (index, register, login, verify, homes, admin).
const { account, restore, signOut } = useSession();

onMounted(() => {
  restore();
});

const backendURL = useBackendURL();

async function logout() {
  const { token } = useSession();
  const dying = token.value;
  // Local session ends first: a slow or dead backend must never hold the
  // logout hostage.
  signOut();
  navigateTo("/");
  if (!dying) return;
  try {
    await (await import("~/auth")).logout(backendURL, dying);
  } catch {
    // The token dies server-side on TTL; the local session is already gone.
  }
}
</script>

<template>
  <div class="shell">
    <header class="topbar">
      <NuxtLink to="/" class="brand">Thresh</NuxtLink>
      <nav>
        <template v-if="account">
          <NuxtLink to="/requests">Requests</NuxtLink>
          <NuxtLink to="/modules">Modules</NuxtLink>
          <NuxtLink v-if="account.role === 'platform_admin'" to="/admin-stats">Stats</NuxtLink>
          <span class="who">{{ account.displayName }}</span>
          <button class="link" @click="logout">Log out</button>
        </template>
        <template v-else>
          <NuxtLink to="/register">Register</NuxtLink>
          <NuxtLink to="/login">Log in</NuxtLink>
        </template>
      </nav>
    </header>
    <main><NuxtPage /></main>
  </div>
</template>

<style>
:root {
  --ink: #2b3327;
  --muted: #5b6350;
  --line: #d8dcd2;
  --card: #ffffff;
  --bg: #f4f5f0;
  --accent: #2f5d28;
  --accent-soft: #e4efe0;
  --warn: #8a5a00;
  --warn-soft: #fdf0d8;
  --danger: #c44536;
}
body {
  margin: 0;
  font-family: system-ui, -apple-system, sans-serif;
  color: var(--ink);
  background: var(--bg);
}
.shell {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}
.topbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 0.75rem 1.5rem;
  background: var(--card);
  border-bottom: 1px solid var(--line);
}
.brand {
  font-weight: 700;
  font-size: 1.15rem;
  color: var(--accent);
  text-decoration: none;
}
nav {
  display: flex;
  gap: 1rem;
  align-items: center;
}
nav a {
  color: var(--muted);
  text-decoration: none;
}
nav a:hover {
  color: var(--ink);
}
.who {
  color: var(--muted);
}
.link {
  border: 0;
  background: 0;
  color: var(--muted);
  cursor: pointer;
  font: inherit;
  padding: 0;
}
.link:hover {
  color: var(--ink);
}
main {
  flex: 1;
  width: 100%;
  max-width: 48rem;
  margin: 0 auto;
  padding: 2rem 1rem 4rem;
  box-sizing: border-box;
}
.card {
  background: var(--card);
  border: 1px solid var(--line);
  border-radius: 0.5rem;
  padding: 1.5rem;
  margin-bottom: 1.25rem;
}
.card.error {
  border-color: var(--danger);
}
label {
  display: block;
  margin-bottom: 0.9rem;
  color: var(--muted);
  font-size: 0.92rem;
}
input,
textarea,
select {
  display: block;
  width: 100%;
  box-sizing: border-box;
  margin-top: 0.3rem;
  padding: 0.5rem 0.6rem;
  border: 1px solid var(--line);
  border-radius: 0.35rem;
  font: inherit;
  background: #fff;
}
button.primary {
  background: var(--accent);
  color: #fff;
  border: 0;
  border-radius: 0.35rem;
  padding: 0.55rem 1.2rem;
  font: inherit;
  font-weight: 600;
  cursor: pointer;
}
button.primary:disabled {
  opacity: 0.6;
  cursor: default;
}
button.secondary {
  background: #fff;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: 0.35rem;
  padding: 0.45rem 1rem;
  font: inherit;
  cursor: pointer;
}
button.danger {
  background: #fff;
  color: var(--danger);
  border: 1px solid var(--danger);
  border-radius: 0.35rem;
  padding: 0.45rem 1rem;
  font: inherit;
  cursor: pointer;
}
.error-text {
  color: var(--danger);
}
.ok-text {
  color: var(--accent);
}
.hint {
  color: var(--muted);
  font-size: 0.85rem;
}
.pill {
  display: inline-block;
  padding: 0.1rem 0.6rem;
  border-radius: 999px;
  font-size: 0.82rem;
  font-weight: 600;
}
.pill.ok {
  background: var(--accent-soft);
  color: var(--accent);
}
.pill.warn {
  background: var(--warn-soft);
  color: var(--warn);
}
.pill.bad {
  background: #fbe4e1;
  color: var(--danger);
}
.warn-text {
  color: var(--warn);
}
</style>
