package app

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/undy-io/BORG/internal/auth"
	"github.com/undy-io/BORG/internal/config"
	"github.com/undy-io/BORG/internal/discovery"
	k8sdiscovery "github.com/undy-io/BORG/internal/discovery/k8s"
	"github.com/undy-io/BORG/internal/httpapi"
	"github.com/undy-io/BORG/internal/proxy"
)

type DiscoveryFactory func([]config.DiscoverySelector) (discovery.Discoverer, error)

type Options struct {
	DiscoveryFactory DiscoveryFactory
}

type App struct {
	Config  *config.Runtime
	Auth    *auth.Authenticator
	Proxy   *proxy.Service
	Handler http.Handler

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func New(configPath string) (*App, error) {
	return NewWithOptions(configPath, Options{})
}

func NewWithOptions(configPath string, opts Options) (*App, error) {
	runtime, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}

	authenticator, err := auth.New(runtime.AuthKey, runtime.AuthPrefix)
	if err != nil {
		return nil, err
	}

	proxyService := proxy.New()
	for _, inst := range runtime.Instances {
		proxyService.AddInstance(inst.Endpoint, inst.APIKey, inst.Models)
	}

	borgApp := &App{
		Config:  runtime,
		Auth:    authenticator,
		Proxy:   proxyService,
		Handler: httpapi.New(authenticator, proxyService),
	}
	borgApp.startDiscovery(runtime, opts.discoveryFactory())

	return borgApp, nil
}

func (o Options) discoveryFactory() DiscoveryFactory {
	if o.DiscoveryFactory != nil {
		return o.DiscoveryFactory
	}
	return func(selectors []config.DiscoverySelector) (discovery.Discoverer, error) {
		return k8sdiscovery.New(selectors)
	}
}

func (a *App) startDiscovery(runtime *config.Runtime, factory DiscoveryFactory) {
	if runtime.UpdateInterval <= 0 || len(runtime.K8SDiscover) == 0 {
		return
	}

	discoverer, err := factory(runtime.K8SDiscover)
	if err != nil {
		log.Printf("Failed to load k8s discovery service: %v", err)
		return
	}
	if discoverer == nil {
		log.Printf("Failed to load k8s discovery service: discovery factory returned nil")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	reconciler := discovery.NewReconciler(discoverer)
	interval := time.Duration(runtime.UpdateInterval) * time.Second

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()

		run := func() {
			if err := reconciler.Update(ctx, a.Proxy); err != nil && ctx.Err() == nil {
				log.Printf("Discovery refresh failed; preserving previous endpoint snapshot: %v", err)
			}
		}

		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

func (a *App) Close() {
	a.closeOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		a.wg.Wait()
	})
}
