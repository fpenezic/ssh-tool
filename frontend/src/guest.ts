// Entry point for the browser-share guest page. Standalone: mounts GuestApp
// into a plain browser, no Wails runtime. Served over HTTPS by the Go share
// server (internal/share).
import { mount } from "svelte";
import GuestApp from "./guest/GuestApp.svelte";

mount(GuestApp, { target: document.getElementById("guest")! });
