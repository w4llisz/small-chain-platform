package jobs

import (
	"errors"
	"testing"
)

func TestPayloadEncoding(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		valid         bool
	}{
		{"unicode", "你好", true}, {"escaped NUL", "a\x00b", true}, {"invalid UTF-8", string([]byte{0xff}), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec, err := (Spec{Kind: "demo.checksum", Payload: test.payload}).Normalize()
			if test.valid {
				if err != nil || spec.Payload != test.payload {
					t.Fatalf("changed valid payload: %+v %v", spec, err)
				}
			} else if !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}
