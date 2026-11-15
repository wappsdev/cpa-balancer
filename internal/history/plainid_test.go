package history

import (
	"strings"
	"testing"
)

func TestPlainIDIsAStoredKeyThatIsNeitherEmptyNorOther(t *testing.T) {
	for id, want := range map[string]bool{
		uid: true, uid + "/ab12": true, "codex:abc": true,
		"": false, OtherSession: false, "a b": false, "a\nb": false, strings.Repeat("a", maxIDLength+1): false,
	} {
		if got := PlainID(id); got != want {
			t.Errorf("PlainID(%q) = %v, want %v", id, got, want)
		}
	}
}
