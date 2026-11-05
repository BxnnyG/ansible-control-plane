package runs

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/scan"
)

func baseTemplate() dbgen.Template {
	return dbgen.Template{
		ID: uuid.New(), Playbook: "site.yml", ExtraVars: []byte(`{"a":1}`), EnvVars: []byte(`{}`),
		DefaultLimit: "web", DefaultTags: "base", Verbosity: 1,
		AllowLimitOverride: true, AllowTagsOverride: true, AllowCheckOverride: true,
		AllowDiffOverride: true, AllowVerbosityOverride: true,
	}
}

func sp[T any](v T) *T { return &v }

func TestMergeParams(t *testing.T) {
	survey := []scan.Field{
		{Name: "version", Type: scan.TypeStr, Required: true, Choices: []any{"1.0", "2.0"}},
		{Name: "count", Type: scan.TypeInt, Default: 3},
		{Name: "token", Type: scan.TypeStr, Secret: true},
		{Name: "opts", Type: scan.TypeDict, Fields: []scan.Field{{Name: "mode", Type: scan.TypeBool, Required: true}}},
		{Name: "names", Type: scan.TypeList, Elements: scan.TypeStr},
	}
	tests := []struct {
		name    string
		mutate  func(*dbgen.Template)
		ov      Overrides
		survey  []scan.Field
		strict  bool
		wantErr string
		check   func(*testing.T, Params)
	}{
		{name: "defaults only", check: func(t *testing.T, p Params) {
			if p.Limit != "web" || p.Tags != "base" || p.Verbosity != 1 || p.ExtraVars["a"] != float64(1) {
				t.Errorf("unexpected params %+v", p)
			}
		}},
		{name: "override limit tags check diff verbosity", ov: Overrides{Limit: sp(" db "), Tags: sp("x"), SkipTags: sp("y"),
			Check: sp(true), Diff: sp(true), Verbosity: sp(3)}, check: func(t *testing.T, p Params) {
			if p.Limit != "db" || p.Tags != "x" || p.SkipTags != "y" || !p.Check || !p.Diff || p.Verbosity != 3 {
				t.Errorf("unexpected params %+v", p)
			}
		}},
		{name: "limit override denied", mutate: func(tp *dbgen.Template) { tp.AllowLimitOverride = false }, ov: Overrides{Limit: sp("db")}, wantErr: "overriding the limit"},
		{name: "tags override denied", mutate: func(tp *dbgen.Template) { tp.AllowTagsOverride = false }, ov: Overrides{SkipTags: sp("db")}, wantErr: "overriding tags"},
		{name: "check override denied", mutate: func(tp *dbgen.Template) { tp.AllowCheckOverride = false }, ov: Overrides{Check: sp(true)}, wantErr: "check mode"},
		{name: "diff override denied", mutate: func(tp *dbgen.Template) { tp.AllowDiffOverride = false }, ov: Overrides{Diff: sp(true)}, wantErr: "diff mode"},
		{name: "verbosity override denied", mutate: func(tp *dbgen.Template) { tp.AllowVerbosityOverride = false }, ov: Overrides{Verbosity: sp(2)}, wantErr: "verbosity"},
		{name: "verbosity out of range", ov: Overrides{Verbosity: sp(9)}, wantErr: "between 0 and 4"},
		{name: "vars override denied", ov: Overrides{ExtraVars: map[string]any{"b": 2}}, wantErr: "extra variables"},
		{name: "vars override allowed", mutate: func(tp *dbgen.Template) { tp.AllowVarsOverride = true }, ov: Overrides{ExtraVars: map[string]any{"b": 2, "a": 5}},
			check: func(t *testing.T, p Params) {
				if p.ExtraVars["a"] != 5 || p.ExtraVars["b"] != 2 {
					t.Errorf("vars %v", p.ExtraVars)
				}
			}},
		{name: "bad limit syntax", ov: Overrides{Limit: sp("~re")}, wantErr: "regular expression"},
		{name: "survey answers allowed without vars override", survey: survey, strict: true,
			ov: Overrides{ExtraVars: map[string]any{"version": "1.0", "token": "s3cr3t"}}, check: func(t *testing.T, p Params) {
				if p.ExtraVars["count"] != 3 {
					t.Errorf("default not applied: %v", p.ExtraVars)
				}
				if len(p.SecretVars) != 1 || p.SecretVars[0] != "token" {
					t.Errorf("secret vars %v", p.SecretVars)
				}
				if m := p.Masked(); m.ExtraVars["token"] != MaskedValue || p.ExtraVars["token"] != "s3cr3t" {
					t.Errorf("masking failed: %v / %v", m.ExtraVars["token"], p.ExtraVars["token"])
				}
			}},
		{name: "non-survey var rejected with survey", survey: survey, strict: true,
			ov: Overrides{ExtraVars: map[string]any{"version": "1.0", "other": 1}}, wantErr: "extra variables"},
		{name: "required survey field missing", survey: survey, strict: true, wantErr: "version is required"},
		{name: "survey wrong type", survey: survey, strict: true, ov: Overrides{ExtraVars: map[string]any{"version": 2}}, wantErr: "version must be a string"},
		{name: "survey choice", survey: survey, strict: true, ov: Overrides{ExtraVars: map[string]any{"version": "9.9"}}, wantErr: "allowed choices"},
		{name: "survey int rejects fraction", survey: survey, strict: true, ov: Overrides{ExtraVars: map[string]any{"version": "1.0", "count": 1.5}}, wantErr: "count must be an integer"},
		{name: "survey nested required", survey: survey, strict: true,
			ov: Overrides{ExtraVars: map[string]any{"version": "1.0", "opts": map[string]any{}}}, wantErr: "opts.mode is required"},
		{name: "survey list element type", survey: survey, strict: true,
			ov: Overrides{ExtraVars: map[string]any{"version": "1.0", "names": []any{"a", 1}}}, wantErr: "names[1] must be a string"},
		{name: "lenient survey does not require", survey: survey, strict: false, check: func(t *testing.T, p Params) {
			if len(p.SecretVars) != 0 {
				t.Errorf("secret vars %v", p.SecretVars)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tp := baseTemplate()
			if tc.mutate != nil {
				tc.mutate(&tp)
			}
			p, err := mergeParams(tp, tc.ov, tc.survey, tc.strict)
			if tc.wantErr != "" {
				if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want ErrInvalid containing %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "s3cr3t") {
					t.Error("error leaks a value")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.check != nil {
				tc.check(t, p)
			}
		})
	}
}

func TestParseSurvey(t *testing.T) {
	tests := []struct {
		name, in string
		fields   int
		bad      bool
	}{
		{"empty", ``, 0, false},
		{"null", `null`, 0, false},
		{"array", `[{"name":"a","type":"str"}]`, 1, false},
		{"object", `{"role":"r","fields":[{"name":"a","type":"int"},{"name":"b","type":"bool"}]}`, 2, false},
		{"unknown type", `[{"name":"a","type":"weird"}]`, 0, true},
		{"duplicate", `[{"name":"a","type":"str"},{"name":"a","type":"str"}]`, 0, true},
		{"no name", `[{"type":"str"}]`, 0, true},
		{"scalar", `"x"`, 0, true},
		{"broken", `[{`, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := ParseSurvey([]byte(tc.in))
			if tc.bad {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || len(f) != tc.fields {
				t.Fatalf("fields %d err %v", len(f), err)
			}
		})
	}
}

func TestCursorRoundTrip(t *testing.T) {
	now := baseTime()
	id := uuid.New()
	ts, gotID, err := decodeCursor(encodeCursor(now, id))
	if err != nil || !ts.Equal(now) || gotID != id {
		t.Fatalf("round trip: %v %v %v", ts, gotID, err)
	}
	for _, bad := range []string{"!!", "YQ", ""} {
		if _, _, err := decodeCursor(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("cursor %q: %v", bad, err)
		}
	}
}

func TestMaskedParamsJSONHasNoSecret(t *testing.T) {
	p := Params{ExtraVars: map[string]any{"token": "s3cr3t", "x": 1}, SecretVars: []string{"token"}}
	b, _ := json.Marshal(p.Masked())
	if strings.Contains(string(b), "s3cr3t") {
		t.Errorf("masked params leak: %s", b)
	}
}

func baseTime() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 123456000, time.UTC) }
