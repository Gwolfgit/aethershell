package daemon

import (
	"bytes"
	"testing"
)

func TestReplayTailCapsSnapshot(t *testing.T) {
	var b bytes.Buffer
	for i := 0; i < 128; i++ {
		b.WriteString("line\n")
	}

	got := replayTail(b.Bytes(), 64)
	if len(got) > 64 {
		t.Fatalf("snapshot length = %d, want <= 64", len(got))
	}
	if len(got) > 0 && got[0] != 'l' {
		t.Fatalf("snapshot starts mid-line: %q", got[:1])
	}
	if !bytes.HasSuffix(got, []byte("line\n")) {
		t.Fatalf("snapshot does not include recent tail: %q", got)
	}
}

func TestReplayTailCopiesWholeSmallBuffer(t *testing.T) {
	src := []byte("recent output\n")
	got := replayTail(src, 64)
	if !bytes.Equal(got, src) {
		t.Fatalf("snapshot = %q, want %q", got, src)
	}
	got[0] = 'R'
	if src[0] == 'R' {
		t.Fatal("snapshot aliases source buffer")
	}
}
