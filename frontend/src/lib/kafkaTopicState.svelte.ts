// State of one topic tab inside a Kafka tab: the consumer form, the backend
// consumer it started, the preview mirror of its buffer and what the view
// had picked. It lives here, not in KafkaConsumer, so a remount of the pane
// (split, moving the tab, a hidden topic tab) neither stops the consumer nor
// forgets the records. Only closing the topic tab (release) or ending the
// Kafka session (kafkaTabs.forget) ends it.
//
// The mirror holds PREVIEWS (every key / value / header cut to 2 KiB by the
// backend), bounded in records and bytes; the full record is fetched when
// the user opens one. A heavy topic with large records would otherwise put
// gigabytes into the webview.

import { api, type KafkaMessage, type KafkaStartMode, type KafkaConsumeSnapshot, type KafkaConsumeRequest } from "./api";
import { kafkaTabs, tabTopic } from "./kafkaState.svelte";
import { messageTextBytes } from "./kafkaFormat";
import { toast } from "./toast.svelte.ts";
import { errMsg } from "./connectErrors";

const MIRROR_CAP = 10_000;
// Bound on the mirror's preview text. Previews are at most ~2 KiB a field,
// so this only bites on records with many large headers.
const MIRROR_BYTES = 24 << 20;
// The backend divides this much buffer among the consumers of the cluster
// (internal/kafka ClusterBufferBudget); the mirror shrinks with its share, so
// many listening topics do not add up in the webview.
export const CLUSTER_BUDGET = 256 << 20;
// Poll period on screen and in a hidden tab (the badges still count).
const POLL_SHOWN = 400;
const POLL_HIDDEN = 2000;

export type TopicSubtab = "messages" | "produce" | "partitions" | "config" | "metrics";

function localNow(deltaMs: number): string {
  return localAt(Date.now() + deltaMs);
}

// A datetime-local input value for a time in ms.
function localAt(ms: number): string {
  const d = new Date(ms);
  const p = (x: number) => String(x).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

export class TopicState {
  // Consume form.
  mode = $state<KafkaStartMode>("last");
  n = $state(20);
  offset = $state(0);
  when = $state(localNow(-3600_000));
  partition = $state(-1);
  limit = $state(0);
  stopAtEnd = $state(true);
  // Backend match: only records that match are kept (the rest are counted).
  keyHas = $state("");
  valueHas = $state("");
  headerHas = $state("");

  consumerId = $state<string | null>(null);
  snap = $state<Omit<KafkaConsumeSnapshot, "messages"> | null>(null);
  // $state.raw: thousands of records must not become deep proxies. The array
  // is replaced, never mutated, so reassignment is the only signal needed.
  msgs = $state.raw<KafkaMessage[]>([]);
  starting = $state(false);

  // View: the in-buffer filter, following the tail, the picked record (its
  // preview and, once fetched, the full one), the topic subtab.
  filter = $state("");
  follow = $state(true);
  selected = $state.raw<KafkaMessage | null>(null);
  full = $state.raw<KafkaMessage | null>(null);
  fullErr = $state<string | null>(null);
  detailTab = $state<"value" | "key" | "headers">("value");
  subtab = $state<TopicSubtab>("messages");
  scroll = 0;
  // Set by the view while it is on screen; a hidden one polls slower.
  visible = false;

  private mirrorBytes = 0;
  private lastSeq = 0;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private polling = false;
  private disposed = false;

  readonly topic: string;

  // tab is the tab key (kafkaState tabTopic): one topic can be open in
  // several tabs, each with its own consumer.
  constructor(readonly sessionId: string, readonly tab: string) {
    this.topic = tabTopic(tab);
  }

  get running(): boolean {
    return !!this.consumerId && this.snap?.state === "running";
  }

  async start() {
    await this.drop();
    this.reset();
    this.starting = true;
    try {
      const id = await api.kafkaConsumeStart(this.sessionId, {
        topic: this.topic,
        mode: this.mode,
        n: this.mode === "last" ? this.n : this.mode === "offset" ? this.offset : 0,
        timestamp: this.mode === "timestamp" ? new Date(this.when).getTime() : 0,
        partition: this.partition,
        limit: Math.max(0, this.limit | 0),
        stop_at_end: this.mode !== "latest" && this.stopAtEnd,
        key_contains: this.keyHas,
        value_contains: this.valueHas,
        header_contains: this.headerHas,
        tab: this.tab,
      });
      // The tab was closed while the start offsets were listed.
      if (this.disposed) {
        api.kafkaConsumeStop(id).catch(() => {});
        return;
      }
      // adoptTabState took over another consumer of this tab meanwhile:
      // the one just started wins.
      if (this.consumerId && this.consumerId !== id) {
        api.kafkaConsumeStop(this.consumerId).catch(() => {});
        this.reset();
      }
      this.consumerId = id;
      this.poll();
    } catch (e) {
      toast.err(`Consume: ${errMsg(e)}`);
    } finally {
      this.starting = false;
    }
  }

  // Take over a consumer this window did not start (detach, redock, reload):
  // the form shows what started it, the mirror pages the buffer in again.
  adopt(id: string, req: KafkaConsumeRequest) {
    if (this.consumerId === id) return;
    if (this.consumerId) {
      // Two consumers for one tab: keep the one already shown.
      api.kafkaConsumeStop(id).catch(() => {});
      return;
    }
    this.mode = req.mode;
    if (req.mode === "last") this.n = req.n;
    if (req.mode === "offset") this.offset = req.n;
    if (req.mode === "timestamp") this.when = localAt(req.timestamp);
    this.partition = req.partition;
    this.limit = req.limit;
    if (req.mode !== "latest") this.stopAtEnd = req.stop_at_end;
    this.keyHas = req.key_contains ?? "";
    this.valueHas = req.value_contains ?? "";
    this.headerHas = req.header_contains ?? "";
    this.reset();
    this.consumerId = id;
    this.poll();
  }

  // Stop fetching. The backend keeps the buffer, so records read so far can
  // still be opened in full and exported.
  async stop() {
    this.unschedule();
    const id = this.consumerId;
    if (!id) return;
    await api.kafkaConsumeHalt(id).catch(() => {});
    await this.poll();
    if (this.snap && this.snap.state === "running") this.snap = { ...this.snap, state: "stopped" };
    this.publish();
  }

  // Stop and let the backend drop the buffer.
  async drop() {
    this.unschedule();
    const id = this.consumerId;
    this.consumerId = null;
    this.publish();
    if (id) await api.kafkaConsumeStop(id).catch(() => {});
  }

  clear() {
    this.drop();
    this.reset();
  }

  // Ends this state: stopBackend releases the consumer and its buffer (topic
  // tab closed); without it the backend keeps it for another window, or the
  // cluster close already ends it.
  dispose(stopBackend: boolean) {
    this.disposed = true;
    this.unschedule();
    if (stopBackend && this.consumerId) api.kafkaConsumeStop(this.consumerId).catch(() => {});
    this.consumerId = null;
    this.msgs = [];
    this.mirrorBytes = 0;
  }

  // Open a record: show its preview at once, then the full record when any
  // part of it was cut.
  async select(m: KafkaMessage | null) {
    this.selected = m;
    this.full = null;
    this.fullErr = null;
    const id = this.consumerId;
    if (!m || !id) return;
    const cut = m.key?.truncated || m.value?.truncated || m.headers.some((h) => h.value.truncated);
    if (!cut) {
      this.full = m;
      return;
    }
    try {
      const r = await api.kafkaConsumeMessage(id, m.seq);
      if (this.selected?.seq === m.seq) this.full = r;
    } catch (e) {
      if (this.selected?.seq === m.seq) this.fullErr = errMsg(e);
    }
  }

  // The view came on screen: catch up now instead of at the slow tick.
  show(on: boolean) {
    this.visible = on;
    if (on && this.timer) {
      this.unschedule();
      this.poll();
    }
  }

  private reset() {
    this.msgs = [];
    this.mirrorBytes = 0;
    this.lastSeq = 0;
    this.selected = null;
    this.full = null;
    this.fullErr = null;
    this.snap = null;
    this.publish();
  }

  private schedule() {
    this.unschedule();
    if (this.disposed) return;
    this.timer = setTimeout(() => {
      this.timer = null;
      this.poll();
    }, this.visible ? POLL_SHOWN : POLL_HIDDEN);
  }

  private unschedule() {
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
  }

  async poll() {
    const id = this.consumerId;
    if (!id || this.polling || this.disposed) return;
    this.polling = true;
    let more = true;
    try {
      for (let round = 0; round < 20; round++) {
        const s = await api.kafkaConsumeSnapshot(id, this.lastSeq, 1000);
        if (id !== this.consumerId) return;
        const { messages, ...rest } = s;
        this.snap = rest;
        if (messages.length) {
          this.lastSeq = messages[messages.length - 1].seq;
          for (const m of messages) this.mirrorBytes += messageTextBytes(m);
          this.msgs = this.trimMirror(this.msgs.concat(messages));
        }
        // A short page means caught up: the backend also cuts pages at
        // about 2 MiB, so compare with what was asked, not with 1000.
        if (!messages.length || (s.buffered > 0 && messages[messages.length - 1].seq >= s.received)) break;
      }
      more = this.snap?.state === "running";
    } catch {
      // The consumer is gone (cluster closed); stop polling.
      more = false;
    } finally {
      this.polling = false;
      this.publish();
    }
    if (more && id === this.consumerId) this.schedule();
  }

  // Drops the oldest mirrored records beyond this consumer's share.
  private trimMirror(next: KafkaMessage[]): KafkaMessage[] {
    const s = this.snap;
    const capN = Math.max(1, Math.min(MIRROR_CAP, s?.buffer_messages || MIRROR_CAP));
    const capB = MIRROR_BYTES * Math.min(1, s?.buffer_bytes ? s.buffer_bytes / CLUSTER_BUDGET : 1);
    let cut = 0;
    let bytes = this.mirrorBytes;
    while (cut < next.length - 1 && (next.length - cut > capN || bytes > capB)) {
      bytes -= messageTextBytes(next[cut]);
      cut++;
    }
    this.mirrorBytes = bytes;
    return cut > 0 ? next.slice(cut) : next;
  }

  // Tell the badges (tab bar, topic list) what this consumer is doing.
  private publish() {
    const s = this.snap;
    kafkaTabs.setListening(
      this.sessionId,
      this.tab,
      this.consumerId && s && !this.disposed ? { state: s.state, received: s.received, scanned: s.scanned, filtering: s.filtering, error: s.error } : null,
    );
  }
}

// The state of a topic tab (by tab key), made on first use.
export function topicState(sessionId: string, tab: string): TopicState {
  const key = sessionId + "\u0000" + tab;
  let t = kafkaTabs.topicStates.get(key) as TopicState | undefined;
  if (!t) {
    t = new TopicState(sessionId, tab);
    kafkaTabs.topicStates.set(key, t);
  }
  return t;
}

// Closing a topic tab: its consumer ends and the backend drops the buffer.
export function releaseTopic(sessionId: string, tab: string) {
  const key = sessionId + "\u0000" + tab;
  kafkaTabs.topicStates.get(key)?.dispose(true);
  kafkaTabs.topicStates.delete(key);
  kafkaTabs.setListening(sessionId, tab, null);
}

// A window that did not open this Kafka tab (detach, redock, reload) takes
// the topic tabs and consumers from the backend. Call once per session; the
// tabs the user opened meanwhile stay, after the backend's.
export async function adoptTabState(sessionId: string) {
  let st;
  try {
    st = await api.kafkaGetTabState(sessionId);
  } catch {
    return;
  }
  if (!st) return;
  const mine = kafkaTabs.openTopics[sessionId] ?? [];
  // A consumer names its tab (an older one only its topic).
  const tabOf = (r: KafkaConsumeRequest) => r.tab || r.topic;
  const names = [...st.topics];
  for (const c of st.consumers) if (!names.includes(tabOf(c.request))) names.push(tabOf(c.request));
  for (const t of mine) if (!names.includes(t)) names.push(t);
  kafkaTabs.openTopics[sessionId] = names;
  for (const c of st.consumers) topicState(sessionId, tabOf(c.request)).adopt(c.id, c.request as KafkaConsumeRequest);
}
