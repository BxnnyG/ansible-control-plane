// Package fake provides a scriptable connector type "static" for tests. It is
// never registered in the production registry.
package fake

import (
	"context"
	"errors"
	"sync"

	"github.com/BxnnyG/ansible-control-plane/internal/connectors"
)

// Type is the registered type name.
const Type = "static"

// Script controls what the connector instances return.
type Script struct {
	mu     sync.Mutex
	result connectors.Result
	err    error
	calls  int
	block  chan struct{}
}

// Set replaces the result returned by Sync and clears any error.
func (s *Script) Set(r connectors.Result) { s.mu.Lock(); s.result, s.err = r, nil; s.mu.Unlock() }

// Fail makes Sync (and Test) return err.
func (s *Script) Fail(err error) { s.mu.Lock(); s.err = err; s.mu.Unlock() }

// Block makes Sync wait until ctx is done or the returned func is called.
func (s *Script) Block() (release func()) {
	ch := make(chan struct{})
	s.mu.Lock()
	s.block = ch
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// Calls returns the number of Sync calls.
func (s *Script) Calls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

type conn struct{ s *Script }

func (conn) Type() string { return Type }

func (c conn) Test(context.Context) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	return c.s.err
}

func (c conn) Sync(ctx context.Context) (connectors.Result, error) {
	c.s.mu.Lock()
	c.s.calls++
	r, err, blk := c.s.result, c.s.err, c.s.block
	c.s.mu.Unlock()
	if blk != nil {
		select {
		case <-blk:
		case <-ctx.Done():
			return connectors.Result{}, ctx.Err()
		}
	}
	if err != nil {
		return connectors.Result{}, err
	}
	return r, nil
}

// Register adds the "static" type driven by s to reg. The type accepts an
// optional api_token secret and no base URL.
func Register(reg *connectors.Registry, s *Script) {
	reg.Register(connectors.Spec{
		Type: Type, Label: "Static (test)", SecretTypes: []string{"api_token"},
		Fields: []connectors.Field{{Key: "note", Label: "Note", Kind: connectors.FieldString}},
	}, func(connectors.Instance, connectors.Credential) (connectors.Connector, error) {
		if s == nil {
			return nil, errors.New("fake: no script")
		}
		return conn{s: s}, nil
	})
}
