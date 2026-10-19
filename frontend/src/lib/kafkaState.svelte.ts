// Kafka tab state shared outside KafkaPane: the open cluster handles (so the
// pane header, the status of a split and the quick palette can read them),
// the topic names each tab last listed (quick palette "kafka:" entries), and
// whether a connection has Kafka enabled through its folder chain.
//
// The handles live on the backend (app_kafka.go); this is a cache refreshed
// by KafkaPane and by a detached window's KafkaList.

import { api, type Connection, type KafkaClusterInfo } from "./api";
import { tree, paneTabs, view } from "./stores.svelte";
import { formatCount } from "./kafkaFormat";

export interface ListenState {
  state: string; // running | done | stopped | error
  received: number; // records kept (matches, with a filter)
  scanned: number;
  filtering: boolean;
  error?: string;
}

class KafkaStore {
  // sessionId (cluster handle id) -> info
  infos = $state<Record<string, KafkaClusterInfo>>({});
  // sessionId -> topic names last listed by that tab
  topics = $state<Record<string, string[]>>({});
  // sessionId -> topic tabs open inside that Kafka tab, in order. Kept here
  // (and mirrored to the backend) so a remount of the pane shows the same
  // tabs, and so the saved session can list them. Absent = this window has
  // not shown the tab yet: KafkaPane asks the backend (adoptTabState).
  openTopics = $state<Record<string, string[]>>({});
  // sessionId -> the view in front: "groups" | "brokers" | "new" | "t:<topic>"
  // | "" (the overview text).
  activeView = $state<Record<string, string>>({});
  // sessionId + "\0" + topic -> TopicState (kafkaTopicState.svelte.ts): the
  // consumer of a topic tab, outliving the components that show it.
  topicStates = new Map<string, { sessionId: string; dispose(stopBackend: boolean): void }>();
  // sessionId -> topic -> state of that topic's consumer, for the badges.
  listening = $state<Record<string, Record<string, ListenState>>>({});
  // A request from the quick palette for a tab to select a topic; the tab
  // with that sessionId consumes it.
  pendingTopic = $state<{ sessionId: string; topic: string } | null>(null);

  set(info: KafkaClusterInfo) {
    this.infos[info.id] = info;
  }
  get(sessionId: string): KafkaClusterInfo | undefined {
    return this.infos[sessionId];
  }
  forget(sessionId: string) {
    delete this.infos[sessionId];
    delete this.topics[sessionId];
    delete this.openTopics[sessionId];
    delete this.activeView[sessionId];
    delete this.listening[sessionId];
    // The consumers end with the handle (KafkaClose), or live on in the
    // window the tab went to; either way this window lets go of them.
    for (const [key, t] of this.topicStates) {
      if (t.sessionId !== sessionId) continue;
      t.dispose(false);
      this.topicStates.delete(key);
    }
  }
  // A consumer reports its state (null = it is gone). Only a change is
  // written, so a poll that learned nothing does not wake the badges.
  setListening(sessionId: string, topic: string, s: ListenState | null) {
    const cur = this.listening[sessionId]?.[topic];
    if (!s) {
      if (!cur) return;
      delete this.listening[sessionId][topic];
      return;
    }
    if (cur && cur.state === s.state && cur.received === s.received && cur.scanned === s.scanned && cur.error === s.error) return;
    (this.listening[sessionId] ??= {})[topic] = s;
  }
  setTopics(sessionId: string, names: string[]) {
    this.topics[sessionId] = names;
  }
  async refresh(): Promise<KafkaClusterInfo[]> {
    const list = (await api.kafkaList()) ?? [];
    for (const k of list) this.infos[k.id] = k;
    return list;
  }
}

export const kafkaTabs = new KafkaStore();

// Walks the folder chain the way the resolver does, so the tree's context
// menu reflects a folder-level kafka_enabled without a resolve round trip.
export function kafkaEnabledFor(conn: Connection | null | undefined): boolean {
  if (!conn) return false;
  const own = conn.overrides?.kafka_enabled;
  if (own !== undefined && own !== null) return !!own;
  return kafkaInheritedFrom(conn.folder_id);
}

// kafka_enabled as the folder chain from folderId upwards sets it.
export function kafkaInheritedFrom(folderId: string | null | undefined): boolean {
  for (let i = 0; i < 10000 && folderId; i++) {
    const f = tree.folderById(folderId);
    if (!f) break;
    const v = f.settings?.kafka_enabled;
    if (v !== undefined && v !== null) return !!v;
    folderId = f.parent_id;
  }
  return false;
}

// Ends a pane's backend session whatever kind it is.
export async function endSession(sessionId: string, kind: string | undefined) {
  if (kind === "local") return api.localShellDisconnect(sessionId);
  if (kind === "kafka") {
    kafkaTabs.forget(sessionId);
    return api.kafkaClose(sessionId);
  }
  return api.sshDisconnect(sessionId);
}

// Quick palette rows for the topics of every open Kafka tab: "Kafka: name /
// topic" jumps to the tab and selects the topic.
export function kafkaPaletteActions(): {
  id: string;
  title: string;
  hint?: string;
  keywords?: string[];
  run: () => void;
}[] {
  const out = [];
  for (const [sid, names] of Object.entries(kafkaTabs.topics)) {
    const info = kafkaTabs.infos[sid];
    if (!info) continue;
    for (const topic of names) {
      out.push({
        id: `kafka:${sid}:${topic}`,
        title: `Kafka: ${info.name} / ${topic}`,
        hint: "open",
        keywords: ["kafka", "topic", topic, info.name],
        run: () => {
          const holds = (n: import("./panetypes").PaneNode): boolean =>
            n.kind === "pane" ? n.sessionId === sid : holds(n.a) || holds(n.b);
          const tab = paneTabs.tabs.find((t) => holds(t.root));
          if (!tab) return;
          paneTabs.activeTabId = tab.tabId;
          view.setTab("terminal");
          kafkaTabs.pendingTopic = { sessionId: sid, topic };
        },
      });
    }
  }
  return out;
}

// What the Kafka tabs among sessionIds are listening to: the consumers still
// running, how many of them failed, and a tooltip line list. A running
// consumer that carries a fetch error counts as failed (kgo keeps retrying,
// but nothing arrives).
export function kafkaListenSummary(sessionIds: string[]): { running: number; failed: number; tip: string } {
  let running = 0;
  let failed = 0;
  const lines: string[] = [];
  for (const sid of sessionIds) {
    for (const [topic, l] of Object.entries(kafkaTabs.listening[sid] ?? {})) {
      const bad = l.state === "error" || !!l.error;
      if (l.state !== "running" && !bad) continue;
      if (bad) failed++;
      else running++;
      const n = l.filtering ? `${formatCount(l.received)} of ${formatCount(l.scanned)}` : formatCount(l.received);
      lines.push(bad ? `${topic}: failed - ${l.error ?? "error"}` : `${topic} (${n})`);
    }
  }
  return { running, failed, tip: lines.length ? `listening: ${lines.join(", ")}` : "" };
}
