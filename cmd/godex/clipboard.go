//go:build !linux

package main

import (
	"log"

	clipboard "golang.design/x/clipboard"
)

func initClipboard() {
	if err := clipboard.Init(); err != nil {
		log.Printf("[Clipboard] Warning: clipboard initialization failed: %v", err)
	}
}

func readClipboardImage() []byte {
	return clipboard.Read(clipboard.FmtImage)
}

func readClipboardText() []byte {
	return clipboard.Read(clipboard.FmtText)
}
