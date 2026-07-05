// Take-over dialog + countdown for a WG profile that's live on another
// synced machine. When a connect fails with the "active on another
// machine" error, connectionActions parks the attempt here; the dialog
// lets the user request a hand-over (kill-request) and watches the
// remote presence clear, or connect anyway.

import { api } from "./api";
import { errMsg } from "./connectErrors";

// The backend error prefix that means "a synced WG profile is up
// elsewhere". Must match errProfileBusyPrefix in app_presence.go.
export const BUSY_PREFIX = "network profile is active on another machine: ";

// Parses the busy error's "<profileID>|<ownerName>" payload. Returns
// null when the error isn't the busy-elsewhere case.
export function isBusyElsewhere(e: unknown): { profileId: string; owner: string } | null {
  const m = errMsg(e);
  const i = m.indexOf(BUSY_PREFIX);
  if (i < 0) return null;
  const rest = m.slice(i + BUSY_PREFIX.length).trim();
  const bar = rest.indexOf("|");
  if (bar < 0) return { profileId: "", owner: rest };
  return { profileId: rest.slice(0, bar), owner: rest.slice(bar + 1) };
}

type Phase = "idle" | "asking" | "requesting" | "freed" | "timeout";

class PresenceTakeover {
  open = $state(false);
  phase = $state<Phase>("idle");
  profileId = $state("");
  machineName = $state("");
  remaining = $state(0); // countdown seconds while requesting
  private timer: ReturnType<typeof setInterval> | null = null;
  private resolver: ((decision: "retry" | "cancel") => void) | null = null;

  // ask parks a blocked connect: shows the dialog and resolves with
  // the user's decision. "retry" means the profile is free (or forced)
  // and the caller should attempt the connect again.
  ask(profileId: string, machineName: string): Promise<"retry" | "cancel"> {
    this.reset();
    this.profileId = profileId;
    this.machineName = machineName;
    this.phase = "asking";
    this.open = true;
    return new Promise((resolve) => (this.resolver = resolve));
  }

  // takeOver writes the kill-request and polls until the remote owner
  // clears (retry) or the estimate elapses (timeout -> offer force).
  async takeOver() {
    try {
      const estimate = await api.networkProfileTakeOver(this.profileId);
      if (!estimate) {
        // Already free by the time we asked - just retry.
        this.finish("retry");
        return;
      }
      this.phase = "requesting";
      this.remaining = estimate;
      this.startCountdown();
      this.pollFreed();
    } catch (e) {
      toastErr(e);
      this.finish("cancel");
    }
  }

  // connectAnyway authorises one bring-up despite the live owner (both
  // peers will flap - the dialog warns).
  async connectAnyway() {
    try {
      await api.networkProfileConnectAnyway(this.profileId);
      this.finish("retry");
    } catch (e) {
      toastErr(e);
      this.finish("cancel");
    }
  }

  cancel() {
    this.finish("cancel");
  }

  private startCountdown() {
    this.stopTimers();
    this.timer = setInterval(() => {
      this.remaining -= 1;
      if (this.remaining <= 0) {
        this.stopTimers();
        this.phase = "timeout";
      }
    }, 1000);
  }

  // pollFreed re-checks remote presence every few seconds; when the
  // owner's record is gone, the take-over succeeded.
  private async pollFreed() {
    const startPhase = this.phase;
    while (this.phase === "requesting") {
      await new Promise((r) => setTimeout(r, 3000));
      if (this.phase !== "requesting") break;
      try {
        const ro = await api.networkProfilePresence(this.profileId);
        if (!ro.active) {
          this.finish("retry");
          return;
        }
      } catch {
        // transient - keep waiting until the countdown gives up
      }
    }
    void startPhase;
  }

  private finish(decision: "retry" | "cancel") {
    this.stopTimers();
    this.open = false;
    this.phase = "idle";
    const r = this.resolver;
    this.resolver = null;
    if (r) r(decision);
  }

  private reset() {
    this.stopTimers();
    this.phase = "idle";
    this.remaining = 0;
    this.machineName = "";
    if (this.resolver) {
      this.resolver("cancel");
      this.resolver = null;
    }
  }

  private stopTimers() {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }
}

function toastErr(e: unknown) {
  // Lazy import to avoid a cycle at module load.
  import("./toast.svelte").then((m) => m.toast.err(errMsg(e)));
}

export const presenceTakeover = new PresenceTakeover();
