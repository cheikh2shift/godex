//go:build !linux

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

func readClipboardImage() []byte {
	return clipboard.Read(clipboard.FmtImage)
}

func readClipboardText() []byte {
	return clipboard.Read(clipboard.FmtText)
}
