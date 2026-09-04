package limited

import (
	"strings"
	"testing"
)

func TestBufferKeepsEverythingUnderTheLimit(t *testing.T) {
	buffer := &Buffer{Limit: 100}

	if _, err := buffer.Write([]byte("hello")); err != nil {
		t.Fatalf("Write returned %v", err)
	}
	if got := buffer.String(); got != "hello" {
		t.Errorf("String() = %q, want %q", got, "hello")
	}
	if buffer.Truncated() {
		t.Error("Truncated() is true, and nothing was dropped")
	}
}

func TestBufferKeepsTheStartAndCountsTheRest(t *testing.T) {
	buffer := &Buffer{Limit: 10}

	written, err := buffer.Write([]byte(strings.Repeat("a", 25)))
	if err != nil || written != 25 {
		t.Fatalf("Write returned (%d, %v), want (25, nil)", written, err)
	}

	got := buffer.String()
	if !strings.HasPrefix(got, strings.Repeat("a", 10)) {
		t.Errorf("String() = %q, want the first ten bytes", got)
	}
	if !strings.Contains(got, "15 more bytes") {
		t.Errorf("String() = %q, and it does not count the dropped bytes", got)
	}
	if !buffer.Truncated() {
		t.Error("Truncated() is false after dropping fifteen bytes")
	}
}

// The limit holds across calls, not only inside one.
func TestBufferCountsAcrossWrites(t *testing.T) {
	buffer := &Buffer{Limit: 4}

	for range 5 {
		if _, err := buffer.Write([]byte("ab")); err != nil {
			t.Fatalf("Write returned %v", err)
		}
	}

	if !strings.HasPrefix(buffer.String(), "abab") {
		t.Errorf("String() = %q, want the first four bytes", buffer.String())
	}
	if !strings.Contains(buffer.String(), "6 more bytes") {
		t.Errorf("String() = %q, want six dropped bytes", buffer.String())
	}
}
