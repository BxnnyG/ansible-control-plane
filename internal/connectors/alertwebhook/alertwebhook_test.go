package alertwebhook

import (
	"context"
	"testing"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

func TestSyncNeverTouchesAlerts(t *testing.T) {
	c, err := New(connectors.Instance{}, connectors.Credential{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Sync(context.Background())
	if err != nil || res.ReplaceAlerts || len(res.Alerts) != 0 {
		t.Errorf("res=%+v err=%v", res, err)
	}
	if err := c.Test(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestSpec(t *testing.T) {
	reg := connectors.NewRegistry()
	Register(reg)
	spec, _ := reg.Spec(Type)
	if spec.BaseURL || spec.SecretRequired || !spec.GeneratesSecret {
		t.Errorf("spec %+v", spec)
	}
	if err := reg.Validate(Type, "", nil, "", false); err != nil {
		t.Error(err)
	}
	if err := reg.Validate(Type, "", nil, secrets.TypeToken, true); err != nil {
		t.Error(err)
	}
	if err := reg.Validate(Type, "https://x.example.test", nil, "", false); err == nil {
		t.Error("base URL must be rejected")
	}
}
