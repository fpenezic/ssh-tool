//go:build !android && !ios

// File-drop forwarder for Wails v3 native drag-and-drop. When the user
// drops a file/folder onto an element tagged `data-file-drop-target`,
// the JS runtime sends a WindowFilesDropped event back to Go with the
// resolved filesystem paths (not just names - that's why we use the
// platform-level API instead of HTML5 dataTransfer.files). We re-emit
// the payload as a custom "file_drop" event so Svelte components can
// react with the standard EventsOn pattern.

package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// FileDropPayload is what we hand to the frontend.
type FileDropPayload struct {
	Filenames []string          `json:"filenames"`
	X         int               `json:"x"`
	Y         int               `json:"y"`
	TargetID  string            `json:"target_id"`
	ClassList []string          `json:"class_list"`
	Attrs     map[string]string `json:"attrs"`
}

// registerFileDropForwarding wires the WindowFilesDropped listener.
// Call once per window. Idempotent (each call registers a fresh
// listener, but the same handler logic so no harm if called twice).
func registerFileDropForwarding(w *application.WebviewWindow) {
	if w == nil {
		return
	}
	w.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		ctx := e.Context()
		dropTarget := ctx.DropTargetDetails()
		payload := FileDropPayload{
			Filenames: ctx.DroppedFiles(),
		}
		if dropTarget != nil {
			payload.X = dropTarget.X
			payload.Y = dropTarget.Y
			payload.TargetID = dropTarget.ElementID
			payload.ClassList = dropTarget.ClassList
			payload.Attrs = dropTarget.Attributes
		}
		EventsEmit("file_drop", payload)
	})
}
