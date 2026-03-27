package hive

import (
	"crypto/rand"
	"fmt"
)

var nameAdjectives = []string{
	"Brisk",
	"Calm",
	"Bright",
	"Clever",
	"Swift",
	"Quiet",
	"Gentle",
	"Bold",
	"Nimble",
	"Patient",
	"Curious",
	"Witty",
}

var nameNouns = []string{
	"Otter",
	"Fox",
	"Hawk",
	"Pine",
	"River",
	"Comet",
	"Cedar",
	"Lynx",
	"Heron",
	"Sparrow",
	"Wolf",
	"Badger",
}

func randomHumanName() string {
	adj := nameAdjectives[randomIndex(len(nameAdjectives))]
	noun := nameNouns[randomIndex(len(nameNouns))]
	num := randomIndex(90) + 10
	return fmt.Sprintf("%s %s %d", adj, noun, num)
}

func randomIndex(n int) int {
	if n <= 1 {
		return 0
	}
	b := make([]byte, 1)
	_, _ = rand.Read(b)
	return int(b[0]) % n
}
