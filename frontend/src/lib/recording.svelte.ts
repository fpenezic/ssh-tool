// Session recording state mirror. The recordings themselves live on
// the Go side (asciicast v2 files, output tapped at the PTY sink);
// this store only tracks WHICH sessions are recording so tab
// indicators and menu labels can react. A "recording_changed" event
// fires on every start/stop, including backend-initiated stops when
// a session dies, so the indicator can't go stale.

import { api, type RecordingState } from "./api";
import { EventsOn } from "./wailsRuntime";
import { toast } from "./toast.svelte";
import { showConfirm } from "./confirmModal.svelte.ts";

class RecordingStore {
  // sessionId -> .cast path. Reassigned on every change (Svelte 5
  // deep tracking misses single-key mutation in some $derived
  // shapes; full replace is cheap at this size).
  active = $state<Record<string, string>>({});

  private wired = false;

  async init() {
    if (this.wired) return;
    this.wired = true;
    EventsOn("recording_changed", (st: RecordingState) => {
      if (!st?.session_id) return;
      const next = { ...this.active };
      if (st.recording) {
        next[st.session_id] = st.path;
      } else {
        delete next[st.session_id];
        // Backend stop (session died with a live recording) needs
        // user-visible feedback too; user-initiated stops also land
        // here, so the toast is the single confirmation path.
        toast.ok("Recording saved: " + st.path, 6000);
      }
      this.active = next;
    });
    // Detached windows / reloads: pick up recordings already running.
    try {
      const live = (await api.recordingActive()) ?? [];
      const next: Record<string, string> = {};
      for (const st of live) next[st.session_id] = st.path;
      this.active = next;
    } catch (e) {
      console.warn("recording init:", e);
    }
  }

  isRecording(sessionId: string): boolean {
    return sessionId in this.active;
  }

  // Confirm before starting, unless the user turned the prompt off. A
  // recording writes every byte the session prints to a plaintext file on
  // disk - anything you cat, any token a command echoes back - so starting
  // one by a misclick is worth a speed bump. Off-switch lives in Settings ->
  // Session recording for people who record routinely.
  async start(sessionId: string) {
    // Read the opt-out outside the try below: a failed settings read must not
    // surface as "Recording failed", and an unreadable setting falls back to
    // asking, which is the safe side.
    let optedOut = false;
    try {
      const v = await api.settingsGet("recording_confirm_disabled");
      optedOut = v === "1" || v === "true";
    } catch { /* ask */ }

    if (!optedOut) {
      const dir = await api.recordingsDir().catch(() => "");
      const ok = await showConfirm({
        title: "Start recording this session?",
        message:
          "Everything the session prints will be written to a plaintext .cast file" +
          (dir ? " in " + dir : "") +
          " - including anything sensitive that scrolls past. Keystrokes are not recorded.",
        okLabel: "Start recording",
      });
      if (!ok) return;
    }

    try {
      const st = await api.recordingStart(sessionId);
      toast.info("Recording started: " + st.path, 4000);
    } catch (e) {
      toast.err("Recording failed: " + (e instanceof Error ? e.message : String(e)));
    }
  }

  async stop(sessionId: string) {
    try {
      await api.recordingStop(sessionId);
      // Saved-toast comes from the recording_changed handler.
    } catch (e) {
      toast.err("Stop recording failed: " + (e instanceof Error ? e.message : String(e)));
    }
  }

  async toggle(sessionId: string) {
    if (this.isRecording(sessionId)) await this.stop(sessionId);
    else await this.start(sessionId);
  }
}

export const recording = new RecordingStore();

// Recordings browser modal state. Lives here (not component-local)
// so the palette, Settings, and any future "play last recording"
// shortcut can all open it.
class RecordingsModalStore {
  isOpen = $state(false);
  // When set, the modal jumps straight into playback of this file.
  initialPath = $state<string | null>(null);

  open(path?: string) {
    this.initialPath = path ?? null;
    this.isOpen = true;
  }

  close() {
    this.isOpen = false;
    this.initialPath = null;
  }
}

export const recordingsModal = new RecordingsModalStore();
