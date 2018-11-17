package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/italolelis/reception/pkg/config"
	"github.com/italolelis/kit/log"
	"github.com/italolelis/reception/pkg/reception"
	"github.com/italolelis/reception/pkg/coffees"
	"github.com/jmoiron/sqlx"
	"github.com/rafaeljesus/rabbus"
	_ "github.com/lib/pq"
)

func main() {
	// creates a cancel context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// gets the contextual logging
	logger := log.WithContext(ctx)
	defer logger.Sync()

	// loads the configuration from the enviroment
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal(err.Error())
	}
	log.SetLevel(cfg.LogLevel)

	// setup the event stream. In this case is an event broker because we chose rabbitmq
	eventStream, err:=setupEventStream(ctx, cfg.EventStream)
	if err != nil {
		logger.Fatal(err.Error())
	}
	defer func(r *rabbus.Rabbus) {
		if err := r.Close(); err != nil {
			logger.Error(err.Error())
		}
	}(eventStream)

	go func() {
		for {
			select {
			case <-eventStream.EmitOk():
				logger.Debug("message sent")
			case <-eventStream.EmitErr():
				logger.Debug("message was not sent")
			}
		}
	}()

	go eventStream.Run(ctx)

	// connects to the primary datastore
	db, err := sqlx.Connect("postgres", cfg.Database.DSN)
	if err != nil {
		logger.Fatal(err.Error())
	}
	defer db.Close()

	wRepo := reception.NewPostgresWriteRepository(db)
	rRepo := reception.NewPostgresReadRepository(db)

	coffeeReadRepo := coffees.NewPostgresReadRepository(db)

	// creates the router and register the handlers
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Timeout(60 * time.Second))

	r.Route("/orders", func(r chi.Router) {
		r.Post("/", reception.CreateOrderHandler(wRepo, coffeeReadRepo, eventStream))
		r.Get("/{id}", reception.GetOrderHandler(rRepo))
	})

	logger.Infow("service running", "port", cfg.Port)
	logger.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", cfg.Port), chi.ServerBaseContext(ctx, r)))
}

func setupEventStream(ctx context.Context, cfg config.EventStream) (*rabbus.Rabbus, error) {
	logger := log.WithContext(ctx)

	cbStateChangeFunc := func(name, from, to string) {
		logger.Debugw("rabbitmq state changed", "from", from, "to", to)
	}

	return rabbus.New(
		cfg.DSN,
		rabbus.Durable(true),
		rabbus.Attempts(cfg.Attempts),
		rabbus.Sleep(cfg.Backoff),
		rabbus.Threshold(cfg.Threshold),
		rabbus.OnStateChange(cbStateChangeFunc),
	)
}
