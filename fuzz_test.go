package moirai

import "testing"

func FuzzCodecsDoNotPanic(f *testing.F) {
	for _, seed := range [][]byte{
		nil,
		[]byte(`{}`),
		[]byte(`{"messages":[]}`),
		[]byte("{\"type\":\"session_meta\"}\nnot-json\n"),
		[]byte{0, 1, 2, 0xff},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		limits := DefaultLimits()
		limits.MaxInputBytes = 64 << 10
		_, _ = DetectFormat(data)
		for _, format := range Formats {
			codec, err := DefaultRegistry.Codec(format)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = codec.Parse(data, ParseOptions{Limits: limits, SourceID: "fuzz"})
		}
	})
}

func FuzzSelectorDoesNotPanic(f *testing.F) {
	for _, seed := range []string{"session", "session#1", "session#1-3", "", "#", "x#-1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		_, _ = ParseSelector(value)
	})
}
