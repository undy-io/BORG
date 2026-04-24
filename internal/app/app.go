package app

import (
	"net/http"

	"github.com/undy-io/BORG/internal/auth"
	"github.com/undy-io/BORG/internal/config"
	"github.com/undy-io/BORG/internal/httpapi"
	"github.com/undy-io/BORG/internal/proxy"
)

type App struct {
	Config  *config.Runtime
	Auth    *auth.Authenticator
	Proxy   *proxy.Service
	Handler http.Handler
}

func New(configPath string) (*App, error) {
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

	return &App{
		Config:  runtime,
		Auth:    authenticator,
		Proxy:   proxyService,
		Handler: httpapi.New(authenticator, proxyService),
	}, nil
}
