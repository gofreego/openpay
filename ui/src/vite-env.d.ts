/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Where the API is reached: opengate, which authenticates the session. */
  readonly VITE_API_BASE_URL: string
  /** OpenAuth's login page. */
  readonly VITE_LOGIN_URL: string
  /** Development only: skip the OpenAuth session check (see vite.config.ts). */
  readonly VITE_DEV_SKIP_LOGIN?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
