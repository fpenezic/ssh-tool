/// <reference types="svelte" />
/// <reference types="vite/client" />

// noVNC ships no type declarations. We only use a small slice of the RFB
// API (constructor, a few props/methods, addEventListener), so a loose
// ambient declaration is enough to keep svelte-check happy.
declare module "@novnc/novnc" {
  export default class RFB extends EventTarget {
    constructor(
      target: HTMLElement,
      urlOrChannel: string,
      options?: { credentials?: { password?: string; username?: string; target?: string } },
    );
    scaleViewport: boolean;
    resizeSession: boolean;
    showDotCursor: boolean;
    background: string;
    qualityLevel: number;
    compressionLevel: number;
    disconnect(): void;
    sendCtrlAltDel(): void;
    sendKey(keysym: number, code: string, down?: boolean): void;
    sendCredentials(creds: { password?: string; username?: string; target?: string }): void;
    clipboardPasteFrom(text: string): void;
    focus(options?: FocusOptions): void;
  }
}
