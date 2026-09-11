package githubtools

import (
	"strings"
	"testing"
)

func TestCheckOutputBoundsMemoryWithoutShortWrite(t *testing.T) {
	output := &boundedCheckOutput{}
	chunk := strings.Repeat("x", maxStaticCheckOutputChars+100)
	for i := 0; i < 3; i++ {
		n, err := output.Write([]byte(chunk))
		if n != len(chunk) || err != nil {
			t.Fatalf("unexpected write: %d %v", n, err)
		}
	}
	if len(output.String()) != maxStaticCheckOutputChars || !output.truncated {
		t.Fatal("unbounded output or missing truncation flag")
	}
}
