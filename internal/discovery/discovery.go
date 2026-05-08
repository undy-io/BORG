package discovery

import (
	"context"
	"sort"
)

const DefaultAPIKey = "EMPTY"

type Endpoint struct {
	URL    string
	Models []string
	APIKey string
}

type Discoverer interface {
	Discover(ctx context.Context) ([]Endpoint, error)
}

type Registry interface {
	AddInstance(endpoint string, apiKey string, models []string)
	RemoveInstance(endpoint string, models []string)
}

type Reconciler struct {
	discoverer Discoverer
	snapshot   map[string]map[string]struct{}
}

func NewReconciler(discoverer Discoverer) *Reconciler {
	return &Reconciler{discoverer: discoverer, snapshot: map[string]map[string]struct{}{}}
}

func (r *Reconciler) Update(ctx context.Context, registry Registry) error {
	endpoints, err := r.discoverer.Discover(ctx)
	if err != nil {
		return err
	}

	next, apiKeys := buildSnapshot(endpoints)
	removals := diff(r.snapshot, next)
	additions := diff(next, r.snapshot)

	for _, endpoint := range sortedKeys(removals) {
		registry.RemoveInstance(endpoint, removals[endpoint])
	}
	for _, endpoint := range sortedKeys(additions) {
		registry.AddInstance(endpoint, apiKeys[endpoint], additions[endpoint])
	}

	r.snapshot = next
	return nil
}

func buildSnapshot(endpoints []Endpoint) (map[string]map[string]struct{}, map[string]string) {
	snapshot := make(map[string]map[string]struct{})
	apiKeys := make(map[string]string)

	for _, endpoint := range endpoints {
		if endpoint.URL == "" || len(endpoint.Models) == 0 {
			continue
		}
		apiKey := endpoint.APIKey
		if apiKey == "" {
			apiKey = DefaultAPIKey
		}
		apiKeys[endpoint.URL] = apiKey
		for _, model := range endpoint.Models {
			if model == "" {
				continue
			}
			if snapshot[model] == nil {
				snapshot[model] = make(map[string]struct{})
			}
			snapshot[model][endpoint.URL] = struct{}{}
		}
	}

	return snapshot, apiKeys
}

func diff(a map[string]map[string]struct{}, b map[string]map[string]struct{}) map[string][]string {
	out := make(map[string][]string)
	for model, endpoints := range a {
		other := b[model]
		for endpoint := range endpoints {
			if _, ok := other[endpoint]; ok {
				continue
			}
			out[endpoint] = append(out[endpoint], model)
		}
	}

	for endpoint := range out {
		sort.Strings(out[endpoint])
	}
	return out
}

func sortedKeys(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
