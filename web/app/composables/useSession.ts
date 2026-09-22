// Session state shared across pages: the bearer token and parsed identity
// live in sessionStorage (per-tab), restored by /me on load.
import { fetchMe, homeRoute, type Me, type PublicAccount } from "~/auth";

export const TOKEN_KEY = "thresh.session";

export function useBackendURL(): string {
  const config = useRuntimeConfig();
  return config.public.backendBaseURL || "http://localhost:8080";
}

export function loadToken(): string | null {
  try {
    return sessionStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

function saveToken(token: string | null) {
  try {
    if (token === null) sessionStorage.removeItem(TOKEN_KEY);
    else sessionStorage.setItem(TOKEN_KEY, token);
  } catch {
    // Storage unavailable (private mode): session stays in-memory.
  }
}

export function useSession() {
  const token = useState<string | null>("thresh-token", () => loadToken());
  const me = useState<Me | null>("thresh-me", () => null);
  const loaded = useState<boolean>("thresh-loaded", () => false);

  // Restore the identity once per app load; a dead token is dropped.
  async function restore(): Promise<void> {
    if (loaded.value) return;
    loaded.value = true;
    if (!token.value) return;
    try {
      me.value = await fetchMe(useBackendURL(), token.value);
    } catch {
      saveToken(null);
      token.value = null;
    }
  }

  function signIn(newToken: string, identity: Me) {
    token.value = newToken;
    saveToken(newToken);
    me.value = identity;
  }

  function signOut() {
    saveToken(null);
    token.value = null;
    me.value = null;
    loaded.value = false;
  }

  function refresh(identity: Me) {
    me.value = identity;
  }

  const account = computed<PublicAccount | null>(() => me.value?.account ?? null);
  const home = computed<string | null>(() =>
    account.value ? homeRoute(account.value, me.value?.requiresTosAcceptance ?? false) : null,
  );

  return { token, me, account, home, restore, signIn, signOut, refresh };
}
