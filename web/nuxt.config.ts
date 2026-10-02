// https://nuxt.com/docs/api/configuration/nuxt-config
export default defineNuxtConfig({
  compatibilityDate: '2025-07-15',
  // Client-only rendering: the app is a session-bound dashboard (bearer
  // tokens live in sessionStorage, reads happen in onMounted), so SSR only
  // served an empty first paint — and its server-side useState pass
  // serialized a null token over the client's real sessionStorage value,
  // which logged refreshing users out (#4).
  ssr: false,
  devtools: { enabled: true },
  runtimeConfig: {
    public: {
      backendBaseURL: process.env.NUXT_PUBLIC_BACKEND_BASE_URL || '',
    },
  },
})
