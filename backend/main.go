// Command backend serves the playground: it compiles Rust to WebAssembly, runs it
// in a sandbox and shares the result on IPFS.
//
// This is the only entry point of the application. See k8s/README.md sections 4
// and 5 for the routes and the settings.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/api"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/compile"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/config"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/execute"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/ipfs"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/run"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/telemetry"
)

// listenAddress is fixed here and not read from the environment, because the
// Dockerfile, the Service and both probes already state it.
const listenAddress = ":8080"

// readHeaderTimeout bounds how long a client may take to send its headers.
// Without it one idle connection can hold a goroutine for as long as it likes.
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout bounds how long a run in flight may finish after the pod has
// been told to stop. It sits just above the deadline of one whole request, so a
// rollout never cuts a run that would have answered.
const shutdownTimeout = api.RequestTimeout + 5*time.Second

func main() {
	if err := serve(); err != nil {
		fmt.Fprintf(os.Stderr, "lgtm-backend: %v\n", err)
		os.Exit(1)
	}
}

func serve() error {
	configuration, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	// The only logger that writes to standard output, and it writes two lines in
	// the life of a pod. Everything between them goes through the OTel bridge, so
	// that every line carries the trace identifier of its run — see issue #18.
	// This one cannot: no telemetry exists yet, and a pod that fails before the
	// next line failed on its configuration, which main reports on stderr.
	startup := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	startup.Info("starting",
		"service", configuration.OTelServiceName,
		"address", listenAddress,
		"ipfs_api", configuration.IPFSAPIURL)

	// A pod is stopped with SIGTERM. Without this the process dies where it
	// stands, no deferred call runs, and the traces of the last runs are lost on
	// every rollout — which is the shape of a bug that only appears under load.
	ctx, stopListening := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stopListening()

	// Telemetry starts before the server, so the first run of a fresh pod is
	// already traced. A Collector that is absent does not stop this: the exporter
	// connects lazily and retries on its own.
	logger, flush, err := telemetry.Start(ctx,
		configuration.OTelServiceName, configuration.OTelExporterOTLPEndpoint)
	if err != nil {
		return err
	}

	// After telemetry.Start, never before: the instruments are built on the meter
	// provider that call installs, and one built earlier would record into the
	// provider that does nothing.
	metrics, err := run.NewMetrics()
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr: listenAddress,
		Handler: api.NewHandler(&run.Pipeline{
			Compiler: &compile.Compiler{},
			Executor: &execute.Executor{},
			Uploader: &ipfs.Client{
				APIURL:     configuration.IPFSAPIURL,
				GatewayURL: configuration.IPFSGatewayURL,
			},
			Logger:  logger,
			Metrics: metrics,
		}, logger),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	go func() {
		<-ctx.Done()
		logger.Info("stopping")

		closing, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(closing); err != nil {
			logger.Error("the server did not stop cleanly", "error", err)
		}
	}()

	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}

	// After the server, so that nothing is still producing spans. This is the
	// call that makes the last trace of the last run arrive.
	closing, cancel := context.WithTimeout(context.Background(), telemetry.ShutdownTimeout)
	defer cancel()
	// On startup, not on logger: this reports that the log exporter itself is
	// gone, so writing it through that exporter would be the one message
	// guaranteed to be lost.
	if flushErr := flush(closing); flushErr != nil {
		startup.Error("the telemetry did not flush", "error", flushErr)
	}
	return err
}
