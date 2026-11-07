package notify

import (
	"errors"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name      string
		typ       string
		cfg       string
		hasSecret bool
		ok        bool
	}{
		{"webhook ok", TypeWebhook, `{"url":"https://hooks.example.test/x"}`, false, true},
		{"webhook hmac with secret", TypeWebhook, `{"url":"https://hooks.example.test/x","auth":"hmac"}`, true, true},
		{"webhook bearer needs secret", TypeWebhook, `{"url":"https://hooks.example.test/x","auth":"bearer"}`, false, false},
		{"webhook bad auth", TypeWebhook, `{"url":"https://hooks.example.test/x","auth":"basic"}`, true, false},
		{"webhook bad scheme", TypeWebhook, `{"url":"ftp://hooks.example.test/x"}`, false, false},
		{"webhook url credentials", TypeWebhook, `{"url":"https://u:p@hooks.example.test/x"}`, false, false},
		{"webhook unknown field", TypeWebhook, `{"url":"https://h.example.test","x":1}`, false, false},
		{"matrix ok", TypeMatrix, `{"homeserver":"https://m.example.test","room_id":"!abc:example.test"}`, true, true},
		{"matrix no secret", TypeMatrix, `{"homeserver":"https://m.example.test","room_id":"!abc:example.test"}`, false, false},
		{"matrix alias room", TypeMatrix, `{"homeserver":"https://m.example.test","room_id":"#abc:example.test"}`, true, false},
		{"smtp ok", TypeSMTP, `{"host":"mail.example.test","from":"a@example.test","to":["b@example.test"]}`, false, true},
		{"smtp auth needs user", TypeSMTP, `{"host":"mail.example.test","from":"a@example.test","to":["b@example.test"]}`, true, false},
		{"smtp auth with user", TypeSMTP, `{"host":"mail.example.test","username":"u","from":"a@example.test","to":["b@example.test"]}`, true, true},
		{"smtp none needs insecure", TypeSMTP, `{"host":"mail.example.test","security":"none","from":"a@example.test","to":["b@example.test"]}`, false, false},
		{"smtp none insecure", TypeSMTP, `{"host":"mail.example.test","security":"none","insecure":true,"from":"a@example.test","to":["b@example.test"]}`, false, true},
		{"smtp header injection", TypeSMTP, `{"host":"mail.example.test","from":"a@example.test\r\nBcc: x@example.test","to":["b@example.test"]}`, false, false},
		{"smtp no recipients", TypeSMTP, `{"host":"mail.example.test","from":"a@example.test","to":[]}`, false, false},
		{"gotify ok", TypeGotify, `{"url":"http://gotify.example.test:8080"}`, true, true},
		{"gotify no secret", TypeGotify, `{"url":"http://gotify.example.test:8080"}`, false, false},
		{"ntfy ok", TypeNtfy, `{"url":"https://ntfy.example.test","topic":"acp"}`, false, true},
		{"ntfy bad topic", TypeNtfy, `{"url":"https://ntfy.example.test","topic":"a/b"}`, false, false},
		{"unknown type", "pager", `{}`, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(tt.typ, []byte(tt.cfg), tt.hasSecret)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatalf("error must wrap ErrInvalid: %v", err)
			}
		})
	}
}
