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
	return f.endpoints, nil
}

type registryCall struct {
	action   string
	endpoint string
	apiKey   string
	models   []string
}

type recordingRegistry struct {
	calls []registryCall
}

func (r *recordingRegistry) AddInstance(endpoint string, apiKey string, models []string) {
	r.calls = append(r.calls, registryCall{
		action:   "add",
		endpoint: endpoint,
		apiKey:   apiKey,
		models:   append([]string(nil), models...),
	})
}

func (r *recordingRegistry) RemoveInstance(endpoint string, models []string) {
	r.calls = append(r.calls, registryCall{
		action:   "remove",
		endpoint: endpoint,
		models:   append([]string(nil), models...),
	})
}

func TestReconcilerInitialAdd(t *testing.T) {
	discoverer := &fakeDiscoverer{endpoints: []Endpoint{
		{URL: "http://upstream-b", APIKey: "sk-b", Models: []string{"beta"}},
		{URL: "http://upstream-a", APIKey: "sk-a", Models: []string{"gamma", "alpha"}},
	}}
	registry := &recordingRegistry{}

	err := NewReconciler(discoverer).Update(context.Background(), registry)
	if err != nil {
		t.Fatal(err)
	}

	want := []registryCall{
		{action: "add", endpoint: "http://upstream-a", apiKey: "sk-a", models: []string{"alpha", "gamma"}},
		{action: "add", endpoint: "http://upstream-b", apiKey: "sk-b", models: []string{"beta"}},
	}
	if !reflect.DeepEqual(registry.calls, want) {
		t.Fatalf("unexpected registry calls\nwant: %#v\n got: %#v", want, registry.calls)
	}
}

func TestReconcilerAuthoritativeRemoval(t *testing.T) {
	discoverer := &fakeDiscoverer{endpoints: []Endpoint{
		{URL: "http://upstream-a", Models: []string{"alpha", "beta"}},
		{URL: "http://upstream-b", Models: []string{"alpha"}},
	}}
	registry := &recordingRegistry{}
	reconciler := NewReconciler(discoverer)

	if err := reconciler.Update(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	registry.calls = nil

	discoverer.endpoints = []Endpoint{
		{URL: "http://upstream-a", Models: []string{"beta"}},
		{URL: "http://upstream-c", APIKey: "sk-c", Models: []string{"gamma"}},
	}

	if err := reconciler.Update(context.Background(), registry); err != nil {
		t.Fatal(err)
	}

	want := []registryCall{
		{action: "remove", endpoint: "http://upstream-a", models: []string{"alpha"}},
		{action: "remove", endpoint: "http://upstream-b", models: []string{"alpha"}},
		{action: "add", endpoint: "http://upstream-c", apiKey: "sk-c", models: []string{"gamma"}},
	}
	if !reflect.DeepEqual(registry.calls, want) {
		t.Fatalf("unexpected registry calls\nwant: %#v\n got: %#v", want, registry.calls)
	}
}

func TestReconcilerModelSpecificAddRemove(t *testing.T) {
	discoverer := &fakeDiscoverer{endpoints: []Endpoint{
		{URL: "http://upstream", Models: []string{"alpha"}},
	}}
	registry := &recordingRegistry{}
	reconciler := NewReconciler(discoverer)

	if err := reconciler.Update(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	registry.calls = nil

	discoverer.endpoints = []Endpoint{
		{URL: "http://upstream", Models: []string{"beta"}},
	}
	if err := reconciler.Update(context.Background(), registry); err != nil {
		t.Fatal(err)
	}

	want := []registryCall{
		{action: "remove", endpoint: "http://upstream", models: []string{"alpha"}},
		{action: "add", endpoint: "http://upstream", apiKey: DefaultAPIKey, models: []string{"beta"}},
	}
	if !reflect.DeepEqual(registry.calls, want) {
		t.Fatalf("unexpected registry calls\nwant: %#v\n got: %#v", want, registry.calls)
	}
}

func TestReconcilerFailedDiscoveryPreservesSnapshot(t *testing.T) {
	discoverer := &fakeDiscoverer{endpoints: []Endpoint{
		{URL: "http://upstream-a", Models: []string{"alpha"}},
	}}
	registry := &recordingRegistry{}
	reconciler := NewReconciler(discoverer)

	if err := reconciler.Update(context.Background(), registry); err != nil {
		t.Fatal(err)
	}
	registry.calls = nil

	discoverer.err = errors.New("boom")
	if err := reconciler.Update(context.Background(), registry); err == nil {
		t.Fatal("expected discovery error")
	}
	if len(registry.calls) != 0 {
		t.Fatalf("expected no registry mutations on failure, got %#v", registry.calls)
	}

	discoverer.err = nil
	discoverer.endpoints = []Endpoint{
		{URL: "http://upstream-b", Models: []string{"beta"}},
	}
	if err := reconciler.Update(context.Background(), registry); err != nil {
		t.Fatal(err)
	}

	want := []registryCall{
		{action: "remove", endpoint: "http://upstream-a", models: []string{"alpha"}},
		{action: "add", endpoint: "http://upstream-b", apiKey: DefaultAPIKey, models: []string{"beta"}},
	}
	if !reflect.DeepEqual(registry.calls, want) {
		t.Fatalf("unexpected registry calls after recovery\nwant: %#v\n got: %#v", want, registry.calls)
	}
}
