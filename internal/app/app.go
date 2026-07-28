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

type DiscoveryFactory func(*config.Runtime) ([]discovery.Source, error)

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

	proxyService := proxy.New(proxy.WithBackendHealth(proxy.BackendHealthConfig{
		Enabled:          runtime.BackendHealth.Enabled,
		FailureThreshold: runtime.BackendHealth.FailureThreshold,
		Cooldown:         time.Duration(runtime.BackendHealth.CooldownSeconds) * time.Second,
		EjectOn500:       runtime.BackendHealth.EjectOn500,
	}), proxy.WithResponseHeaderTimeout(time.Duration(runtime.Upstream.ResponseHeaderTimeoutSeconds)*time.Second))
	staticEndpoints := make([]discovery.Endpoint, 0, len(runtime.Instances))
	for _, inst := range runtime.Instances {
		staticEndpoints = append(staticEndpoints, discovery.Endpoint{
			URL:    inst.Endpoint,
			APIKey: inst.APIKey,
			Models: inst.Models,
		})
	}
	if err := proxyService.ReplaceSource(proxy.StaticSourceID, staticEndpoints); err != nil {
		return nil, err
	}

	borgApp := &App{
		Config:  runtime,
		Auth:    authenticator,
		Proxy:   proxyService,
		Handler: httpapi.New(authenticator, proxyService, httpapi.WithMaxRequestBodyBytes(runtime.MaxRequestBodyBytes)),
	}
	borgApp.startDiscovery(runtime, opts.discoveryFactory())

	return borgApp, nil
}

func (o Options) discoveryFactory() DiscoveryFactory {
	if o.DiscoveryFactory != nil {
		return o.DiscoveryFactory
	}
	return func(runtime *config.Runtime) ([]discovery.Source, error) {
		return k8sdiscovery.NewSources(runtime)
	}
}

func (a *App) startDiscovery(runtime *config.Runtime, factory DiscoveryFactory) {
	if runtime.UpdateInterval <= 0 || len(runtime.K8SDiscover)+len(runtime.K8SServiceDiscover) == 0 {
		return
	}

	sources, err := factory(runtime)
	if err != nil {
		log.Printf("Failed to load k8s discovery service: %v", err)
		return
	}
	if len(sources) == 0 {
		log.Printf("Failed to load k8s discovery service: discovery factory returned no sources")
		return
	}
	type namedReconciler struct {
		id         string
		reconciler *discovery.Reconciler
	}
	reconcilers := make([]namedReconciler, 0, len(sources))
	for _, source := range sources {
		if source.ID == "" || source.Discoverer == nil {
			log.Printf("Skipping invalid Kubernetes discovery source %q", source.ID)
			continue
		}
		reconcilers = append(reconcilers, namedReconciler{
			id:         source.ID,
			reconciler: discovery.NewReconciler(source.ID, source.Discoverer),
		})
	}
	if len(reconcilers) == 0 {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	interval := time.Duration(runtime.UpdateInterval) * time.Second

	for _, source := range reconcilers {
		source := source
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()

			for {
				if err := source.reconciler.Update(ctx, a.Proxy); err != nil && ctx.Err() == nil {
					log.Printf("Discovery source %q refresh failed; preserving its previous endpoint snapshot: %v", source.id, err)
				}

				timer := time.NewTimer(interval)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				case <-timer.C:
				}
			}
		}()
	}
}

func (a *App) Close() {
	a.closeOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		a.wg.Wait()
	})
}
