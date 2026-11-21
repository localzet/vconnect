package tunnel

import (
	"bytes"
	"testing"
)

func TestVP8CoverKeyframeIsIndependentAndIgnoredByTunnelDecoder(t *testing.T) {
	first := VP8CoverKeyframe()
	second := VP8CoverKeyframe()
	if len(first) == 0 || !bytes.Equal(first, second) {
		t.Fatal("VP8CoverKeyframe() did not return the expected frame")
	}

	first[0] ^= 0xff
	if bytes.Equal(first, second) {
		t.Fatal("VP8CoverKeyframe() returned shared mutable storage")
	}

	obf, err := NewTunnelObfuscator([]byte("cover-keyframe-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if got := obf.Decode(second); got.HasFrame {
		t.Fatalf("cover keyframe was decoded as tunnel data: %+v", got)
	}
}
