//go:build linux
// +build linux

package main

import (
	"log"

	clipboard "golang.design/x/clipboard"
)

func initClipboard() {
	if err := clipboard.Init(); err != nil {
		log.Printf("[Clipboard] Error: %v", err)
	}
}

func readClipboardText() []byte {
	data := clipboard.Read(clipboard.FmtText)
	return data
}

func readClipboardImage() []byte {
	return clipboard.Read(clipboard.FmtImage)
}

func readClipboardImagePath() string {
	return ""
}
