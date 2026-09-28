//go:build android

// Android has no xdg-open. Every URL, from the releases page to the APK
// download, goes to the system browser as an Intent.ACTION_VIEW through the
// JNI bridge - the same path the opkssh login already uses.

package main

import "github.com/wailsapp/wails/v3/pkg/application"

func openURLPlatform(url string) error {
	application.Android.OpenURL(url)
	return nil
}
