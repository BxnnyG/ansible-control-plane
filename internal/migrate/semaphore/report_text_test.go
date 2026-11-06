package semaphore

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteText(t *testing.T) {
	s, keys, tgt := fixture()
	var b bytes.Buffer
	Map(s, keys, tgt, Options{}).WriteText(&b)
	out := b.String()
	for _, want := range []string{"[new] #1 deploy", "[unmappable]", "--inventory-map 1=prod", "Summary:", "Dry run: nothing was written", "passphrase removed"} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q", want)
		}
	}
	if strings.Contains(out, "x=secret") {
		t.Error("argument value leaked")
	}
}

func TestDescribeKeysMasks(t *testing.T) {
	key, blk := testKeyB64(t)
	col := encGCM(t, blk, payload("svc", "", genKey(t, "")))
	info := DescribeKeys([]Key{{ID: 1, Name: "a", Type: "ssh", Secret: col}, {ID: 2, Name: "b", Type: "ssh", Secret: "garbage"}}, key)
	if info[1].Login != "svc" || info[1].Err != "" || info[2].Err == "" {
		t.Fatalf("%+v", info)
	}
}
