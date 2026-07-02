//go:build !android && !ios

// Native file/directory dialogs. Desktop-only: Wails' Dialog API is not
// available on mobile (Android open-directory / save-file return errors in
// the alpha runtime), and the SFTP folder-transfer / backup flows that use
// these are themselves desktop-only here. Split out of wails3_runtime.go so
// the shared runtime shim (rt, EventsEmit, BrowserOpenURL) still compiles
// for android/ios.

package main

func OpenFileDialog(opts OpenFileDialogOptions) (string, error) {
	if rt == nil {
		return "", nil
	}
	d := rt.Dialog.OpenFile().
		CanChooseFiles(true).
		CanChooseDirectories(false)
	if opts.Title != "" {
		d.SetTitle(opts.Title)
	}
	return d.PromptForSingleSelection()
}

// OpenDirectoryDialog asks the user for a directory path. Used by the
// SFTP recursive transfer flow (upload a folder).
func OpenDirectoryDialog(opts OpenFileDialogOptions) (string, error) {
	if rt == nil {
		return "", nil
	}
	d := rt.Dialog.OpenFile().
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true)
	if opts.Title != "" {
		d.SetTitle(opts.Title)
	}
	return d.PromptForSingleSelection()
}

func SaveFileDialog(opts SaveFileDialogOptions) (string, error) {
	if rt == nil {
		return "", nil
	}
	d := rt.Dialog.SaveFile()
	// SaveFileDialogStruct has no SetTitle in v3 alpha.95 - closest is
	// SetMessage (the prompt text inside the dialog body).
	if opts.Title != "" {
		d.SetMessage(opts.Title)
	}
	if opts.DefaultFilename != "" {
		d.SetFilename(opts.DefaultFilename)
	}
	return d.PromptForSingleSelection()
}
