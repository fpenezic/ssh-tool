// Curated set of built-in (lucide) icons a connection or folder can use
// instead of an uploaded image. The key (name) is what's persisted in
// connections.icon_name / folders.icon_name; the label drives the
// picker's search. Icon.svelte renders the component tinted by the
// palette colour in icon_color.
//
// Kept intentionally small and server/infra-flavoured - this is a quick
// visual tag, not an icon library. Add sparingly.

import type { Component } from "svelte";

import Server from "@lucide/svelte/icons/server";
import Database from "@lucide/svelte/icons/database";
import Container from "@lucide/svelte/icons/container";
import Boxes from "@lucide/svelte/icons/boxes";
import Cloud from "@lucide/svelte/icons/cloud";
import Router from "@lucide/svelte/icons/router";
import Network from "@lucide/svelte/icons/network";
import Wifi from "@lucide/svelte/icons/wifi";
import Globe from "@lucide/svelte/icons/globe";
import Earth from "@lucide/svelte/icons/earth";
import HardDrive from "@lucide/svelte/icons/hard-drive";
import HardDriveDownload from "@lucide/svelte/icons/hard-drive-download";
import Cpu from "@lucide/svelte/icons/cpu";
import Monitor from "@lucide/svelte/icons/monitor";
import Laptop from "@lucide/svelte/icons/laptop";
import Smartphone from "@lucide/svelte/icons/smartphone";
import Apple from "@lucide/svelte/icons/apple";
import Printer from "@lucide/svelte/icons/printer";
import Cctv from "@lucide/svelte/icons/cctv";
import Terminal from "@lucide/svelte/icons/terminal";
import ShieldCheck from "@lucide/svelte/icons/shield-check";
import Lock from "@lucide/svelte/icons/lock";
import Key from "@lucide/svelte/icons/key";
import GitBranch from "@lucide/svelte/icons/git-branch";
import Box from "@lucide/svelte/icons/box";
import Package from "@lucide/svelte/icons/package";
import Layers from "@lucide/svelte/icons/layers";
import Mail from "@lucide/svelte/icons/mail";
import Activity from "@lucide/svelte/icons/activity";
import Gauge from "@lucide/svelte/icons/gauge";
import Bug from "@lucide/svelte/icons/bug";
import Flame from "@lucide/svelte/icons/flame";
import Zap from "@lucide/svelte/icons/zap";
import Rocket from "@lucide/svelte/icons/rocket";
import Cog from "@lucide/svelte/icons/cog";
import House from "@lucide/svelte/icons/house";
import Building from "@lucide/svelte/icons/building";
import Folder from "@lucide/svelte/icons/folder";
import Bot from "@lucide/svelte/icons/bot";
import Sparkles from "@lucide/svelte/icons/sparkles";
import RadioTower from "@lucide/svelte/icons/radio-tower";
import Plug from "@lucide/svelte/icons/plug";
import Lightbulb from "@lucide/svelte/icons/lightbulb";

export type BuiltinIcon = { name: string; label: string; icon: Component };

export const BUILTIN_ICONS: BuiltinIcon[] = [
  { name: "server", label: "Server", icon: Server },
  { name: "database", label: "Database", icon: Database },
  { name: "container", label: "Container / Docker", icon: Container },
  { name: "boxes", label: "Cluster / Kubernetes", icon: Boxes },
  { name: "cloud", label: "Cloud", icon: Cloud },
  { name: "router", label: "Router", icon: Router },
  { name: "network", label: "Network", icon: Network },
  { name: "wifi", label: "Wi-Fi", icon: Wifi },
  { name: "globe", label: "Globe / Public", icon: Globe },
  { name: "earth", label: "Earth / Region", icon: Earth },
  { name: "hard-drive", label: "Storage / Disk", icon: HardDrive },
  { name: "hard-drive-download", label: "NAS / Backup", icon: HardDriveDownload },
  { name: "cpu", label: "CPU / Compute", icon: Cpu },
  { name: "monitor", label: "Workstation / Windows", icon: Monitor },
  { name: "laptop", label: "Laptop", icon: Laptop },
  { name: "smartphone", label: "Phone / Mobile", icon: Smartphone },
  { name: "apple", label: "Apple / macOS", icon: Apple },
  { name: "printer", label: "Printer", icon: Printer },
  { name: "cctv", label: "Camera / NVR", icon: Cctv },
  { name: "terminal", label: "Shell / Linux", icon: Terminal },
  { name: "shield-check", label: "Security / Firewall", icon: ShieldCheck },
  { name: "lock", label: "Lock / Vault", icon: Lock },
  { name: "key", label: "Key / Auth", icon: Key },
  { name: "git-branch", label: "Git / VCS", icon: GitBranch },
  { name: "box", label: "Box / App", icon: Box },
  { name: "package", label: "Package / Registry", icon: Package },
  { name: "layers", label: "Layers / Stack", icon: Layers },
  { name: "mail", label: "Mail", icon: Mail },
  { name: "activity", label: "Monitoring", icon: Activity },
  { name: "gauge", label: "Metrics / Dashboard", icon: Gauge },
  { name: "bug", label: "Debug / Staging", icon: Bug },
  { name: "flame", label: "Hot / Production", icon: Flame },
  { name: "zap", label: "Fast / Edge", icon: Zap },
  { name: "rocket", label: "Deploy / Launch", icon: Rocket },
  { name: "cog", label: "Config / Service", icon: Cog },
  { name: "house", label: "Home / Lab", icon: House },
  { name: "building", label: "Office / Datacenter", icon: Building },
  { name: "folder", label: "Folder", icon: Folder },
  { name: "bot", label: "AI / Claude Code", icon: Bot },
  { name: "sparkles", label: "AI / Assistant", icon: Sparkles },
  { name: "radio-tower", label: "Console server / Serial-over-IP", icon: RadioTower },
  { name: "plug", label: "Serial / Direct", icon: Plug },
  { name: "lightbulb", label: "IoT / Smart home / Idea", icon: Lightbulb },
];

const byName = new Map(BUILTIN_ICONS.map((b) => [b.name, b]));

export function builtinIconByName(name: string | null | undefined): BuiltinIcon | undefined {
  if (!name) return undefined;
  return byName.get(name);
}
