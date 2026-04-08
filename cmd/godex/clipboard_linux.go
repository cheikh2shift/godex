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
	if len(data) > 0 {
		log.Printf("[Clipboard] read %d bytes", len(data))
	}
	return data
}

func readClipboardImage() []byte {
	return clipboard.Read(clipboard.FmtImage)
}

func readClipboardImagePath() string {
	return ""
}
