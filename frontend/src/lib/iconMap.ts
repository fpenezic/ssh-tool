// Central lucide icon mapping. Use this rather than importing
// individual icon components at every call site - keeps the swap
// surface tiny if we ever rename a kind or change icon families.
//
// Components are re-exported so consumers can either:
//   import { IconFolder } from "./iconMap"
// or:
//   import { credentialKindIcon } from "./iconMap"
//   const C = credentialKindIcon(kind);  // returns the component

import Folder from "@lucide/svelte/icons/folder";
import Zap from "@lucide/svelte/icons/zap";
import Layers from "@lucide/svelte/icons/layers";
import FolderPlus from "@lucide/svelte/icons/folder-plus";
import Server from "@lucide/svelte/icons/server";
import Monitor from "@lucide/svelte/icons/monitor";
import Key from "@lucide/svelte/icons/key";
import KeyRound from "@lucide/svelte/icons/key-round";
import KeySquare from "@lucide/svelte/icons/key-square";
import Lock from "@lucide/svelte/icons/lock";
import Plus from "@lucide/svelte/icons/plus";
import FilePlus from "@lucide/svelte/icons/file-plus";
import X from "@lucide/svelte/icons/x";
import RotateCw from "@lucide/svelte/icons/rotate-cw";
import RefreshCw from "@lucide/svelte/icons/refresh-cw";
import Trash from "@lucide/svelte/icons/trash";
import History from "@lucide/svelte/icons/history";
import Star from "@lucide/svelte/icons/star";
import Link from "@lucide/svelte/icons/link";
import FileText from "@lucide/svelte/icons/file-text";
import Terminal from "@lucide/svelte/icons/terminal";
import ChevronRight from "@lucide/svelte/icons/chevron-right";
import ChevronDown from "@lucide/svelte/icons/chevron-down";
import ChevronsUpDown from "@lucide/svelte/icons/chevrons-up-down";
import ChevronsDownUp from "@lucide/svelte/icons/chevrons-down-up";
import User from "@lucide/svelte/icons/user";
import Clipboard from "@lucide/svelte/icons/clipboard";
import ClipboardCopy from "@lucide/svelte/icons/clipboard-copy";
import Eye from "@lucide/svelte/icons/eye";
import EyeOff from "@lucide/svelte/icons/eye-off";

export {
  Folder as IconFolder,
  Zap as IconAction,
  Layers as IconWorkspace,
  FolderPlus as IconFolderPlus,
  Server as IconHost,
  Monitor as IconMonitor,
  Key as IconKey,
  KeyRound as IconKeyRound,
  KeySquare as IconKeySquare,
  Lock as IconLock,
  Plus as IconPlus,
  FilePlus as IconFilePlus,
  X as IconX,
  RotateCw as IconRotateCw,
  RefreshCw as IconRefresh,
  Trash as IconTrash,
  History as IconHistory,
  Star as IconStar,
  Link as IconLink,
  FileText as IconFile,
  Terminal as IconTerminal,
  ChevronRight as IconChevronRight,
  ChevronDown as IconChevronDown,
  ChevronsUpDown as IconExpandAll,
  ChevronsDownUp as IconCollapseAll,
  User as IconUser,
  Clipboard as IconClipboard,
  ClipboardCopy as IconClipboardCopy,
  Eye as IconEye,
  EyeOff as IconEyeOff,
};

// Status icons
import LoaderCircle from "@lucide/svelte/icons/loader-circle";
import Radio from "@lucide/svelte/icons/radio";
import Settings from "@lucide/svelte/icons/settings";
import Globe from "@lucide/svelte/icons/globe";
import GlobeLock from "@lucide/svelte/icons/globe-lock";
import Activity from "@lucide/svelte/icons/activity";
import Search from "@lucide/svelte/icons/search";
import Cpu from "@lucide/svelte/icons/cpu";
import Bot from "@lucide/svelte/icons/bot";
import MemoryStick from "@lucide/svelte/icons/memory-stick";
import HardDrive from "@lucide/svelte/icons/hard-drive";
import Users from "@lucide/svelte/icons/users";
export {
  LoaderCircle as IconLoading,
  Radio as IconBroadcast,
  Settings as IconSettings,
  Globe as IconGlobe,
  GlobeLock as IconVpn,
  Activity as IconActivity,
  Search as IconSearch,
  Cpu as IconCpu,
  Bot as IconBot,
  MemoryStick as IconMemory,
  HardDrive as IconDisk,
  Users as IconUsers,
};

// Split layout icons (separate so we can import lazily if needed)
import Columns2 from "@lucide/svelte/icons/columns-2";
import Rows2 from "@lucide/svelte/icons/rows-2";
export {
  Columns2 as IconSplitH,
  Rows2 as IconSplitV,
};

// Tunnel / forward icons.
import Cable from "@lucide/svelte/icons/cable";
import Play from "@lucide/svelte/icons/play";
import Square from "@lucide/svelte/icons/square";
import ExternalLink from "@lucide/svelte/icons/external-link";
import PictureInPicture from "@lucide/svelte/icons/picture-in-picture";
export {
  Cable as IconTunnel,
  Play as IconPlay,
  Square as IconStop,
  ExternalLink as IconExternalLink,
  PictureInPicture as IconPopOut,
};

// Dynamic-inventory entry kinds. Host shares IconHost (server tower)
// with the rest of the app. VM uses Monitor (full machine). LXC
// container uses Box for the "lightweight container" connotation.
import Box from "@lucide/svelte/icons/box";
export {
  Box as IconContainer,
};

// dynamicEntryIcon returns the right icon component for a dynamic
// entry kind. Single source of truth so the tree, the Ctrl+K
// palette, the detail-pane header, and tab indicators all agree.
// "host" / "server" / anything we don't recognise → tower; the
// rest match the explicit kinds.
import type { Component } from "svelte";
export function dynamicEntryIcon(kind: string): Component {
  if (kind === "guest_vm") return Monitor as Component;
  if (kind === "guest_lxc") return Box as Component;
  // KindServer + KindHost + unknown fall through to the same tower
  // icon the rest of the app uses for plain hosts.
  return Server as Component;
}

// Map a credential kind ("password" | "key" | other) to its icon.
export function credentialKindIcon(kind: string) {
  switch (kind) {
    case "password":  return Lock;
    case "key":       return KeyRound;
    case "opkssh":    return KeySquare;
    case "api_token": return Globe;
    default:          return Key;
  }
}
