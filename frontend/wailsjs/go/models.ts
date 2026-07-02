export namespace creds {
	
	export class GenerateParams {
	    key_type: string;
	    bits?: number;
	    comment: string;
	    passphrase?: string;
	
	    static createFrom(source: any = {}) {
	        return new GenerateParams(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key_type = source["key_type"];
	        this.bits = source["bits"];
	        this.comment = source["comment"];
	        this.passphrase = source["passphrase"];
	    }
	}
	export class CreateInput {
	    kind: string;
	    name: string;
	    hint?: string;
	    tags: string[];
	    default_username?: string;
	    rotation_reminder_days?: number;
	    password: string;
	    params?: GenerateParams;
	    private_openssh: string;
	    passphrase?: string;
	    key_path: string;
	    socket_path?: string;
	    fingerprint?: string;
	    key_basename: string;
	    opkssh_config_yaml: string;
	    provider_hint?: string;
	    max_cert_age_hours?: number;
	    min_remaining_before_refresh_minutes?: number;
	
	    static createFrom(source: any = {}) {
	        return new CreateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.name = source["name"];
	        this.hint = source["hint"];
	        this.tags = source["tags"];
	        this.default_username = source["default_username"];
	        this.rotation_reminder_days = source["rotation_reminder_days"];
	        this.password = source["password"];
	        this.params = this.convertValues(source["params"], GenerateParams);
	        this.private_openssh = source["private_openssh"];
	        this.passphrase = source["passphrase"];
	        this.key_path = source["key_path"];
	        this.socket_path = source["socket_path"];
	        this.fingerprint = source["fingerprint"];
	        this.key_basename = source["key_basename"];
	        this.opkssh_config_yaml = source["opkssh_config_yaml"];
	        this.provider_hint = source["provider_hint"];
	        this.max_cert_age_hours = source["max_cert_age_hours"];
	        this.min_remaining_before_refresh_minutes = source["min_remaining_before_refresh_minutes"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CreateResult {
	    credential?: store.CredentialRef;
	    public_key?: string;
	    fingerprint?: string;
	
	    static createFrom(source: any = {}) {
	        return new CreateResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.credential = this.convertValues(source["credential"], store.CredentialRef);
	        this.public_key = source["public_key"];
	        this.fingerprint = source["fingerprint"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class Status {
	    state: string;
	    auto_unlock_available?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.auto_unlock_available = source["auto_unlock_available"];
	    }
	}

}

export namespace main {
	
	export class ActiveSessionInfo {
	    session_id: string;
	    connection_id: string;
	    name: string;
	    hostname: string;
	
	    static createFrom(source: any = {}) {
	        return new ActiveSessionInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.session_id = source["session_id"];
	        this.connection_id = source["connection_id"];
	        this.name = source["name"];
	        this.hostname = source["hostname"];
	    }
	}
	export class BrowserLaunchResult {
	    pid: number;
	
	    static createFrom(source: any = {}) {
	        return new BrowserLaunchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pid = source["pid"];
	    }
	}
	export class ConnectionCopyInfo {
	    username: string;
	    hostname: string;
	    port: number;
	    has_password: boolean;
	    ssh_command: string;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionCopyInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.username = source["username"];
	        this.hostname = source["hostname"];
	        this.port = source["port"];
	        this.has_password = source["has_password"];
	        this.ssh_command = source["ssh_command"];
	    }
	}
	export class ConnectionsBatchUpdateInput {
	    ids: string[];
	    patch: store.InheritableSettings;
	    clear_fields: string[];
	
	    static createFrom(source: any = {}) {
	        return new ConnectionsBatchUpdateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ids = source["ids"];
	        this.patch = this.convertValues(source["patch"], store.InheritableSettings);
	        this.clear_fields = source["clear_fields"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConnectionsBatchUpdateResult {
	    updated: number;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionsBatchUpdateResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.updated = source["updated"];
	    }
	}
	export class ConnectionsCreateInput {
	    folder_id?: string;
	    name: string;
	    hostname: string;
	    sort_order: number;
	    overrides: store.InheritableSettings;
	    tags: string[];
	    notes: string;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionsCreateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.folder_id = source["folder_id"];
	        this.name = source["name"];
	        this.hostname = source["hostname"];
	        this.sort_order = source["sort_order"];
	        this.overrides = this.convertValues(source["overrides"], store.InheritableSettings);
	        this.tags = source["tags"];
	        this.notes = source["notes"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConnectionsUpdateInput {
	    id: string;
	    folder_id?: string;
	    clear_folder: boolean;
	    name?: string;
	    hostname?: string;
	    sort_order?: number;
	    overrides?: store.InheritableSettings;
	    tags?: string[];
	    notes?: string;
	    favorite?: boolean;
	    sensitive?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionsUpdateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.folder_id = source["folder_id"];
	        this.clear_folder = source["clear_folder"];
	        this.name = source["name"];
	        this.hostname = source["hostname"];
	        this.sort_order = source["sort_order"];
	        this.overrides = this.convertValues(source["overrides"], store.InheritableSettings);
	        this.tags = source["tags"];
	        this.notes = source["notes"];
	        this.favorite = source["favorite"];
	        this.sensitive = source["sensitive"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CredentialsRotateKeyInput {
	    id: string;
	    generate_new: boolean;
	    private_openssh: string;
	    passphrase?: string;
	
	    static createFrom(source: any = {}) {
	        return new CredentialsRotateKeyInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.generate_new = source["generate_new"];
	        this.private_openssh = source["private_openssh"];
	        this.passphrase = source["passphrase"];
	    }
	}
	export class CredentialsUpdateInput {
	    id: string;
	    kind?: string;
	    folder_id?: string;
	    set_folder_to_null: boolean;
	    name?: string;
	    hint?: string;
	    tags?: string[];
	    config?: Record<string, any>;
	    public_key?: string;
	    set_public_key_to_null: boolean;
	    default_username?: string;
	    set_default_username_to_null: boolean;
	    rotation_reminder_days?: number;
	    set_reminder_to_null: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CredentialsUpdateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.folder_id = source["folder_id"];
	        this.set_folder_to_null = source["set_folder_to_null"];
	        this.name = source["name"];
	        this.hint = source["hint"];
	        this.tags = source["tags"];
	        this.config = source["config"];
	        this.public_key = source["public_key"];
	        this.set_public_key_to_null = source["set_public_key_to_null"];
	        this.default_username = source["default_username"];
	        this.set_default_username_to_null = source["set_default_username_to_null"];
	        this.rotation_reminder_days = source["rotation_reminder_days"];
	        this.set_reminder_to_null = source["set_reminder_to_null"];
	    }
	}
	export class FoldersCreateInput {
	    parent_id?: string;
	    name: string;
	    sort_order: number;
	    settings: store.InheritableSettings;
	
	    static createFrom(source: any = {}) {
	        return new FoldersCreateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.parent_id = source["parent_id"];
	        this.name = source["name"];
	        this.sort_order = source["sort_order"];
	        this.settings = this.convertValues(source["settings"], store.InheritableSettings);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FoldersUpdateInput {
	    id: string;
	    parent_id?: string;
	    clear_parent: boolean;
	    name?: string;
	    sort_order?: number;
	    settings?: store.InheritableSettings;
	
	    static createFrom(source: any = {}) {
	        return new FoldersUpdateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.parent_id = source["parent_id"];
	        this.clear_parent = source["clear_parent"];
	        this.name = source["name"];
	        this.sort_order = source["sort_order"];
	        this.settings = this.convertValues(source["settings"], store.InheritableSettings);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ForwardCreateInput {
	    connection_id: string;
	    kind: string;
	    local_addr?: string;
	    local_port?: number;
	    remote_host?: string;
	    remote_port?: number;
	    auto_start: boolean;
	    description: string;
	
	    static createFrom(source: any = {}) {
	        return new ForwardCreateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.connection_id = source["connection_id"];
	        this.kind = source["kind"];
	        this.local_addr = source["local_addr"];
	        this.local_port = source["local_port"];
	        this.remote_host = source["remote_host"];
	        this.remote_port = source["remote_port"];
	        this.auto_start = source["auto_start"];
	        this.description = source["description"];
	    }
	}
	export class ForwardUpdateInput {
	    id: string;
	    local_addr?: string;
	    clear_local_addr: boolean;
	    local_port?: number;
	    clear_local_port: boolean;
	    remote_host?: string;
	    clear_remote_host: boolean;
	    remote_port?: number;
	    clear_remote_port: boolean;
	    auto_start?: boolean;
	    description?: string;
	
	    static createFrom(source: any = {}) {
	        return new ForwardUpdateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.local_addr = source["local_addr"];
	        this.clear_local_addr = source["clear_local_addr"];
	        this.local_port = source["local_port"];
	        this.clear_local_port = source["clear_local_port"];
	        this.remote_host = source["remote_host"];
	        this.clear_remote_host = source["clear_remote_host"];
	        this.remote_port = source["remote_port"];
	        this.clear_remote_port = source["clear_remote_port"];
	        this.auto_start = source["auto_start"];
	        this.description = source["description"];
	    }
	}
	export class ImagePayload {
	    mime: string;
	    b64: string;
	
	    static createFrom(source: any = {}) {
	        return new ImagePayload(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mime = source["mime"];
	        this.b64 = source["b64"];
	    }
	}
	export class SftpListResult {
	    path: string;
	    entries: ssh.SftpEntry[];
	
	    static createFrom(source: any = {}) {
	        return new SftpListResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.entries = this.convertValues(source["entries"], ssh.SftpEntry);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SftpPreview {
	    b64: string;
	    truncated: boolean;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new SftpPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.b64 = source["b64"];
	        this.truncated = source["truncated"];
	        this.size = source["size"];
	    }
	}
	export class SshConnectResult {
	    session_id: string;
	
	    static createFrom(source: any = {}) {
	        return new SshConnectResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.session_id = source["session_id"];
	    }
	}

}

export namespace rdm {
	
	export class ConnectionAttention {
	    name: string;
	    hostname: string;
	    reason: string;
	    detail?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionAttention(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.hostname = source["hostname"];
	        this.reason = source["reason"];
	        this.detail = source["detail"];
	    }
	}
	export class Summary {
	    folders_created: number;
	    connections_created: number;
	    images_stored: number;
	    jump_resolved: number;
	    jump_unresolved: number;
	    skipped_non_ssh: number;
	    credentials_created: number;
	    credentials_need_secret: number;
	    unresolved_jumps: string[];
	    unresolved_creds: string[];
	    needs_attention: ConnectionAttention[];
	    warnings: string[];
	
	    static createFrom(source: any = {}) {
	        return new Summary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.folders_created = source["folders_created"];
	        this.connections_created = source["connections_created"];
	        this.images_stored = source["images_stored"];
	        this.jump_resolved = source["jump_resolved"];
	        this.jump_unresolved = source["jump_unresolved"];
	        this.skipped_non_ssh = source["skipped_non_ssh"];
	        this.credentials_created = source["credentials_created"];
	        this.credentials_need_secret = source["credentials_need_secret"];
	        this.unresolved_jumps = source["unresolved_jumps"];
	        this.unresolved_creds = source["unresolved_creds"];
	        this.needs_attention = this.convertValues(source["needs_attention"], ConnectionAttention);
	        this.warnings = source["warnings"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace ssh {
	
	export class ForwardStatus {
	    id: string;
	    kind: string;
	    session_id: string;
	    local_addr: string;
	    local_port: number;
	    remote_host?: string;
	    remote_port?: number;
	    state: string;
	    error?: string;
	    bytes_in: number;
	    bytes_out: number;
	    started_at: number;
	
	    static createFrom(source: any = {}) {
	        return new ForwardStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.session_id = source["session_id"];
	        this.local_addr = source["local_addr"];
	        this.local_port = source["local_port"];
	        this.remote_host = source["remote_host"];
	        this.remote_port = source["remote_port"];
	        this.state = source["state"];
	        this.error = source["error"];
	        this.bytes_in = source["bytes_in"];
	        this.bytes_out = source["bytes_out"];
	        this.started_at = source["started_at"];
	    }
	}
	export class SftpEntry {
	    name: string;
	    path: string;
	    is_dir: boolean;
	    is_link: boolean;
	    size: number;
	    mode: number;
	    mode_str: string;
	    mod_time: number;
	    target?: string;
	
	    static createFrom(source: any = {}) {
	        return new SftpEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.is_dir = source["is_dir"];
	        this.is_link = source["is_link"];
	        this.size = source["size"];
	        this.mode = source["mode"];
	        this.mode_str = source["mode_str"];
	        this.mod_time = source["mod_time"];
	        this.target = source["target"];
	    }
	}

}

export namespace store {
	
	export class JumpHostSpec {
	    hostname: string;
	    port?: number;
	    username?: string;
	    auth_ref?: string;
	    via?: JumpHostSpec;
	
	    static createFrom(source: any = {}) {
	        return new JumpHostSpec(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostname = source["hostname"];
	        this.port = source["port"];
	        this.username = source["username"];
	        this.auth_ref = source["auth_ref"];
	        this.via = this.convertValues(source["via"], JumpHostSpec);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class JumpHostOverride {
	    kind: string;
	    chain?: JumpHostSpec;
	
	    static createFrom(source: any = {}) {
	        return new JumpHostOverride(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.chain = this.convertValues(source["chain"], JumpHostSpec);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class InheritableSettings {
	    username?: string;
	    port?: number;
	    auth_ref?: string;
	    jump_host?: JumpHostOverride;
	    ssh_options?: Record<string, string>;
	    env_vars?: Record<string, string>;
	    color_tag?: string;
	    broadcast_group_id?: string;
	    keepalive_interval?: number;
	    terminal_type?: string;
	    auto_reconnect?: boolean;
	    verbose?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new InheritableSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.username = source["username"];
	        this.port = source["port"];
	        this.auth_ref = source["auth_ref"];
	        this.jump_host = this.convertValues(source["jump_host"], JumpHostOverride);
	        this.ssh_options = source["ssh_options"];
	        this.env_vars = source["env_vars"];
	        this.color_tag = source["color_tag"];
	        this.broadcast_group_id = source["broadcast_group_id"];
	        this.keepalive_interval = source["keepalive_interval"];
	        this.terminal_type = source["terminal_type"];
	        this.auto_reconnect = source["auto_reconnect"];
	        this.verbose = source["verbose"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Connection {
	    id: string;
	    folder_id?: string;
	    name: string;
	    hostname: string;
	    sort_order: number;
	    overrides: InheritableSettings;
	    tags: string[];
	    notes: string;
	    favorite: boolean;
	    sensitive: boolean;
	    icon_image_id?: string;
	    last_used_at?: number;
	    created_at: number;
	    updated_at: number;
	    password_vault_key?: string;
	
	    static createFrom(source: any = {}) {
	        return new Connection(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.folder_id = source["folder_id"];
	        this.name = source["name"];
	        this.hostname = source["hostname"];
	        this.sort_order = source["sort_order"];
	        this.overrides = this.convertValues(source["overrides"], InheritableSettings);
	        this.tags = source["tags"];
	        this.notes = source["notes"];
	        this.favorite = source["favorite"];
	        this.sensitive = source["sensitive"];
	        this.icon_image_id = source["icon_image_id"];
	        this.last_used_at = source["last_used_at"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	        this.password_vault_key = source["password_vault_key"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CredentialFolder {
	    id: string;
	    parent_id?: string;
	    name: string;
	    sort_order: number;
	    created_at: number;
	    updated_at: number;
	
	    static createFrom(source: any = {}) {
	        return new CredentialFolder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.parent_id = source["parent_id"];
	        this.name = source["name"];
	        this.sort_order = source["sort_order"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class CredentialHistoryEntry {
	    id: string;
	    credential_id: string;
	    changed_at: number;
	    note: string;
	    rotated_by: string;
	    has_value: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CredentialHistoryEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.credential_id = source["credential_id"];
	        this.changed_at = source["changed_at"];
	        this.note = source["note"];
	        this.rotated_by = source["rotated_by"];
	        this.has_value = source["has_value"];
	    }
	}
	export class CredentialRef {
	    id: string;
	    folder_id?: string;
	    name: string;
	    kind: string;
	    storage_mode: string;
	    hint: string;
	    tags: string[];
	    config: Record<string, any>;
	    public_key?: string;
	    vault_key?: string;
	    default_username?: string;
	    last_rotated_at?: number;
	    expires_at?: number;
	    rotation_reminder_days?: number;
	    retain_history: boolean;
	    icon_image_id?: string;
	    created_at: number;
	    updated_at: number;
	
	    static createFrom(source: any = {}) {
	        return new CredentialRef(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.folder_id = source["folder_id"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.storage_mode = source["storage_mode"];
	        this.hint = source["hint"];
	        this.tags = source["tags"];
	        this.config = source["config"];
	        this.public_key = source["public_key"];
	        this.vault_key = source["vault_key"];
	        this.default_username = source["default_username"];
	        this.last_rotated_at = source["last_rotated_at"];
	        this.expires_at = source["expires_at"];
	        this.rotation_reminder_days = source["rotation_reminder_days"];
	        this.retain_history = source["retain_history"];
	        this.icon_image_id = source["icon_image_id"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class Folder {
	    id: string;
	    parent_id?: string;
	    name: string;
	    sort_order: number;
	    settings: InheritableSettings;
	    icon_image_id?: string;
	    created_at: number;
	    updated_at: number;
	
	    static createFrom(source: any = {}) {
	        return new Folder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.parent_id = source["parent_id"];
	        this.name = source["name"];
	        this.sort_order = source["sort_order"];
	        this.settings = this.convertValues(source["settings"], InheritableSettings);
	        this.icon_image_id = source["icon_image_id"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	export class PortForward {
	    id: string;
	    connection_id: string;
	    kind: string;
	    local_addr?: string;
	    local_port?: number;
	    remote_host?: string;
	    remote_port?: number;
	    auto_start: boolean;
	    description: string;
	
	    static createFrom(source: any = {}) {
	        return new PortForward(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.connection_id = source["connection_id"];
	        this.kind = source["kind"];
	        this.local_addr = source["local_addr"];
	        this.local_port = source["local_port"];
	        this.remote_host = source["remote_host"];
	        this.remote_port = source["remote_port"];
	        this.auto_start = source["auto_start"];
	        this.description = source["description"];
	    }
	}
	export class ResolvedSettings {
	    hostname: string;
	    username?: string;
	    port: number;
	    auth_ref?: string;
	    jump_host?: JumpHostSpec;
	    ssh_options: Record<string, string>;
	    env_vars: Record<string, string>;
	    color_tag?: string;
	    broadcast_group_id?: string;
	    keepalive_interval: number;
	    terminal_type: string;
	    auto_reconnect: boolean;
	    verbose: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ResolvedSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hostname = source["hostname"];
	        this.username = source["username"];
	        this.port = source["port"];
	        this.auth_ref = source["auth_ref"];
	        this.jump_host = this.convertValues(source["jump_host"], JumpHostSpec);
	        this.ssh_options = source["ssh_options"];
	        this.env_vars = source["env_vars"];
	        this.color_tag = source["color_tag"];
	        this.broadcast_group_id = source["broadcast_group_id"];
	        this.keepalive_interval = source["keepalive_interval"];
	        this.terminal_type = source["terminal_type"];
	        this.auto_reconnect = source["auto_reconnect"];
	        this.verbose = source["verbose"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class UsageRef {
	    kind: string;
	    id: string;
	    name: string;
	    hostname?: string;
	
	    static createFrom(source: any = {}) {
	        return new UsageRef(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kind = source["kind"];
	        this.id = source["id"];
	        this.name = source["name"];
	        this.hostname = source["hostname"];
	    }
	}

}

