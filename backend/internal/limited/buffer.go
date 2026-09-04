// Package limited holds a writer that keeps only its first bytes.
//
// Two callers need the same thing for opposite reasons: the compiler may print
// diagnostics without end, and a program from a user may print without end on
// purpose. Neither may become memory in this process.
package limited

import (
	"fmt"
	"strings"
)

// Buffer keeps the first Limit bytes written to it and counts the rest. The zero
// value keeps nothing, so a Limit is always worth setting.
type Buffer struct {
	Limit int

	kept    strings.Builder
	dropped int
}

// Write never fails and never writes short. Refusing bytes here would make the
// writer on the other end fail on a broken pipe, and by then more output would
// help nobody.
func (b *Buffer) Write(p []byte) (int, error) {
	room := b.Limit - b.kept.Len()
	if room > len(p) {
		room = len(p)
	}
	if room > 0 {
		b.kept.Write(p[:room])
	}
	b.dropped += len(p) - room
	return len(p), nil
}

// Truncated says whether anything was dropped.
func (b *Buffer) Truncated() bool {
	return b.dropped > 0
}

// String returns what was kept, followed by a note when bytes were dropped. The
// note matters: output that stops in the middle with no explanation reads like a
// program that stopped in the middle.
func (b *Buffer) String() string {
	if b.dropped == 0 {
		return b.kept.String()
	}
	return fmt.Sprintf("%s\n... %d more bytes were dropped", b.kept.String(), b.dropped)
}
