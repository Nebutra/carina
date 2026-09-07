/// <reference types="vite/client" />

declare const __CARINA_VERSION__: string

interface TauriCore {
  invoke<T = unknown>(command: string, args?: Record<string, unknown>): Promise<T>
}

interface Window {
  __TAURI__?: {
    core?: TauriCore
  }
}
