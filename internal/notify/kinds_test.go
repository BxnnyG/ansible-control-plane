package notify

import (
	"reflect"
	"testing"
)

func TestDeriveKinds(t *testing.T) {
	tests := []struct {
		name string
		f    RunFacts
		want []string
	}{
		{"failed", RunFacts{Status: "failed"}, []string{KindFailed, KindFinished}},
		{"lost", RunFacts{Status: "lost", PrevStatus: "succeeded"}, []string{KindFailed, KindFinished}},
		{"recovered after failed", RunFacts{Status: "succeeded", PrevStatus: "failed"}, []string{KindRecovered, KindFinished}},
		{"recovered after lost", RunFacts{Status: "succeeded", PrevStatus: "lost"}, []string{KindRecovered, KindFinished}},
		{"succeeded after succeeded", RunFacts{Status: "succeeded", PrevStatus: "succeeded"}, []string{KindFinished}},
		{"succeeded without previous", RunFacts{Status: "succeeded"}, []string{KindFinished}},
		{"drift", RunFacts{Status: "succeeded", Check: true, HostsChanged: 2}, []string{KindDrift, KindFinished}},
		{"check without changes", RunFacts{Status: "succeeded", Check: true}, []string{KindFinished}},
		{"changes without check", RunFacts{Status: "succeeded", HostsChanged: 4}, []string{KindFinished}},
		{"recovered and drift", RunFacts{Status: "succeeded", PrevStatus: "failed", Check: true, HostsChanged: 1},
			[]string{KindRecovered, KindDrift, KindFinished}},
		{"cancelled", RunFacts{Status: "cancelled", PrevStatus: "failed"}, []string{KindFinished}},
		{"failed check run is not drift", RunFacts{Status: "failed", Check: true, HostsChanged: 1}, []string{KindFailed, KindFinished}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveKinds(tt.f); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestPickKind(t *testing.T) {
	all := []string{KindFailed, KindRecovered, KindDrift, KindFinished}
	tests := []struct {
		name    string
		derived []string
		sub     []string
		want    string
	}{
		{"most specific wins", all, []string{KindFinished, KindFailed}, KindFailed},
		{"recovered over drift", all, []string{KindDrift, KindRecovered}, KindRecovered},
		{"drift over finished", all, []string{KindFinished, KindDrift}, KindDrift},
		{"fallback to finished", []string{KindFailed, KindFinished}, []string{KindFinished}, KindFinished},
		{"not interested", []string{KindFinished}, []string{KindFailed}, ""},
		{"empty subscription", all, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := map[string]bool{}
			for _, k := range tt.sub {
				sub[k] = true
			}
			if got := PickKind(tt.derived, sub); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	prev := Backoff(1)
	for n := 2; n <= 8; n++ {
		d := Backoff(n)
		if d < prev || d > maxBackoff {
			t.Fatalf("attempt %d: %v after %v", n, d, prev)
		}
		prev = d
	}
	if Backoff(0) != Backoff(1) {
		t.Fatal("attempt 0 must behave like 1")
	}
}
