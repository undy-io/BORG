package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeDiscoverer struct {
	endpoints []Endpoint
	err       error
}

func (f *fakeDiscoverer) Discover(context.Context) ([]Endpoint, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]Endpoint(nil), f.endpoints...), nil
}

type replaceCall struct {
	sourceID  string
	endpoints []Endpoint
}

type recordingRegistry struct {
	calls []replaceCall
	err   error
}

func (r *recordingRegistry) ReplaceSource(sourceID string, endpoints []Endpoint) error {
	if r.err != nil {
		return r.err
	}
	cloned := make([]Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		endpoint.Models = append([]string(nil), endpoint.Models...)
		cloned = append(cloned, endpoint)
	}
	r.calls = append(r.calls, replaceCall{sourceID: sourceID, endpoints: cloned})
	return nil
}

func TestReconcilerReplacesNamedSource(t *testing.T) {
	discoverer := &fakeDiscoverer{endpoints: []Endpoint{
		{URL: "http://upstream", APIKey: "sk-test", Models: []string{"alpha", "beta"}},
	}}
	registry := &recordingRegistry{}

	err := NewReconciler("pods:models", discoverer).Update(context.Background(), registry)
	if err != nil {
		t.Fatal(err)
	}
	want := []replaceCall{{
		sourceID: "pods:models",
		endpoints: []Endpoint{{URL: "http://upstream", APIKey: "sk-test", Models: []string{"alpha", "beta"}}},
	}}
	if !reflect.DeepEqual(registry.calls, want) {
		t.Fatalf("unexpected registry calls\nwant: %#v\n got: %#v", want, registry.calls)
	}
}

func TestReconcilerSuccessfulEmptyRefreshClearsSource(t *testing.T) {
	discoverer := &fakeDiscoverer{endpoints: []Endpoint{{URL: "http://upstream", Models: []string{"alpha"}}}}
	registry := &recordingRegistry{}
	reconciler := NewReconciler("pods:models", discoverer)

	if err := reconciler.Update(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	discoverer.endpoints = nil
	if err := reconciler.Update(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	if len(registry.calls) != 2 || len(registry.calls[1].endpoints) != 0 {
		t.Fatalf("expected an empty replacement, got %#v", registry.calls)
	}
}

func TestReconcilerFailedDiscoveryDoesNotMutateRegistry(t *testing.T) {
	discoverer := &fakeDiscoverer{err: errors.New("list failed")}
	registry := &recordingRegistry{}

	if err := NewReconciler("pods:models", discoverer).Update(context.Background(), registry); err == nil {
		t.Fatal("expected discovery error")
	}
	if len(registry.calls) != 0 {
		t.Fatalf("expected no registry calls, got %#v", registry.calls)
	}
}

func TestReconcilerReturnsRegistryRejection(t *testing.T) {
	discoverer := &fakeDiscoverer{endpoints: []Endpoint{{URL: "http://upstream", Models: []string{"alpha"}}}}
	registry := &recordingRegistry{err: errors.New("API key conflict")}

	err := NewReconciler("services:router", discoverer).Update(context.Background(), registry)
	if err == nil || err.Error() != "API key conflict" {
		t.Fatalf("expected registry rejection, got %v", err)
	}
}
