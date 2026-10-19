// A username + password dialog for a service login (database, Kafka SASL):
// asked when a database has none, and behind "+ New login..." in the
// editors. With save, the caller stores it as a vault credential; without,
// it is used once.

import { api } from "./api";
import { credentials } from "./stores.svelte";
import { toast } from "./toast.svelte.ts";

export interface LoginResult {
  user: string;
  password: string;
  save: boolean;
  name: string;
}

interface LoginState {
  title: string;
  message: string;
  // false: the login is always saved (the editors' "+ New login...").
  canSkipSave: boolean;
  defaultName: string;
  defaultUser: string;
  resolve: (v: LoginResult | null) => void;
}

class LoginModalStore {
  pending = $state<LoginState | null>(null);

  show(opts: { title: string; message?: string; canSkipSave?: boolean; defaultName?: string; defaultUser?: string }): Promise<LoginResult | null> {
    return new Promise((resolve) => {
      this.pending = {
        title: opts.title,
        message: opts.message ?? "",
        canSkipSave: opts.canSkipSave ?? false,
        defaultName: opts.defaultName ?? "",
        defaultUser: opts.defaultUser ?? "",
        resolve,
      };
    });
  }
  done(v: LoginResult | null) {
    const p = this.pending;
    this.pending = null;
    p?.resolve(v);
  }
}

export const loginModal = new LoginModalStore();
export const askLogin = (opts: Parameters<LoginModalStore["show"]>[0]) => loginModal.show(opts);

// "+ New login..." in an editor: asks for a login, stores it as a vault
// password credential and returns its id (null = cancelled or failed).
export async function createLoginCredential(defaultName: string): Promise<string | null> {
  const r = await askLogin({ title: "New login", message: "Saved in the vault as a password credential.", defaultName });
  if (!r) return null;
  try {
    const res = await api.credentialsCreate({ kind: "password", name: r.name || defaultName, password: r.password, default_username: r.user });
    await credentials.load();
    return res.credential?.id ?? null;
  } catch (e) {
    toast.err(`Could not save the login: ${String((e as any)?.message ?? e)}`);
    return null;
  }
}
