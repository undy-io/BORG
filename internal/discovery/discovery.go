package discovery

import (
	"context"
)

const (
	DefaultAPIKey  = "EMPTY"
	StaticSourceID = "static"
)

type Endpoint struct {
	URL    string
	Models []string
	APIKey string
}

type Discoverer interface {
	Discover(ctx context.Context) ([]Endpoint, error)
}

type Source struct {
	ID         string
	Discoverer Discoverer
}

type Registry interface {
	ReplaceSource(sourceID string, endpoints []Endpoint) error
}

type Reconciler struct {
	sourceID   string
	discoverer Discoverer
}

func NewReconciler(sourceID string, discoverer Discoverer) *Reconciler {
	return &Reconciler{sourceID: sourceID, discoverer: discoverer}
}

func (r *Reconciler) Update(ctx context.Context, registry Registry) error {
	endpoints, err := r.discoverer.Discover(ctx)
	if err != nil {
		return err
	}

	return registry.ReplaceSource(r.sourceID, endpoints)
}
