package admin

import (
	"encoding/json"
	"testing"
)

func TestSummarizeScan(t *testing.T) {
	tests := []struct {
		name       string
		scan, warn string
		want       ScanSummary
	}{
		{"empty", `{}`, `[]`, ScanSummary{}},
		{"counts", `{"playbooks":[{},{}],"roles":[{}],"inventories":[{}],"vars_files":[{},{},{}],"vault_files":[{}],"warnings":[{}]}`, `["scan failed"]`, ScanSummary{2, 1, 1, 3, 1, 2}},
		{"invalid", `nope`, ``, ScanSummary{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SummarizeScan(json.RawMessage(tt.scan), json.RawMessage(tt.warn)); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
