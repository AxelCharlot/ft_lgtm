// Package config reads the settings of the backend from the environment.
//
// The backend reads exactly five variables and nothing else, and it refuses to
// start when one of them is missing. See k8s/README.md section 5: a typo in a
// manifest then shows up in `kubectl logs` within seconds, instead of showing up
// days later as a dashboard that draws nothing.
package config

import (
	"fmt"
	"strings"
)

// The names of the five variables. They are exported because the manifests, the
// tests and the error messages all have to agree on the spelling.
const (
	EnvOTelServiceName           = "OTEL_SERVICE_NAME"
	EnvOTelExporterOTLPEndpoint  = "OTEL_EXPORTER_OTLP_ENDPOINT"
	EnvOTelMetricsExemplarFilter = "OTEL_METRICS_EXEMPLAR_FILTER"
	EnvIPFSAPIURL                = "IPFS_API_URL"
	EnvIPFSGatewayURL            = "IPFS_GATEWAY_URL"
)

// ExemplarFilterTraceBased is the only value OTEL_METRICS_EXEMPLAR_FILTER may
// hold. See k8s/README.md section 5.
const ExemplarFilterTraceBased = "trace_based"

// Config holds the five settings. Every field is required.
type Config struct {
	OTelServiceName           string
	OTelExporterOTLPEndpoint  string
	OTelMetricsExemplarFilter string
	IPFSAPIURL                string
	IPFSGatewayURL            string
}

// MissingError names every variable that was absent or blank. It names all of
// them at once, because a manifest with two typos should need one restart to
// find both.
type MissingError struct {
	Names []string
}

func (e *MissingError) Error() string {
	if len(e.Names) == 1 {
		return fmt.Sprintf("the environment variable %s is missing", e.Names[0])
	}
	return fmt.Sprintf("these environment variables are missing: %s",
		strings.Join(e.Names, ", "))
}

// InvalidError names a variable that is present and holds a value the backend
// cannot work with. It is separate from MissingError because the two are fixed
// differently: one manifest line is absent, the other one is wrong.
type InvalidError struct {
	Name  string
	Value string
	Want  string
}

func (e *InvalidError) Error() string {
	return fmt.Sprintf("the environment variable %s is %q, and the only value that works is %q",
		e.Name, e.Value, e.Want)
}

// Load reads the five variables through lookup, which is os.Getenv in the
// program and a map in the tests.
//
// A variable that holds only spaces counts as missing. A manifest that sets a
// value to the empty string is a mistake, and starting anyway would hide it.
func Load(lookup func(string) string) (*Config, error) {
	var missing []string

	read := func(name string) string {
		value := strings.TrimSpace(lookup(name))
		if value == "" {
			missing = append(missing, name)
		}
		return value
	}

	config := &Config{
		OTelServiceName:           read(EnvOTelServiceName),
		OTelExporterOTLPEndpoint:  read(EnvOTelExporterOTLPEndpoint),
		OTelMetricsExemplarFilter: read(EnvOTelMetricsExemplarFilter),
		IPFSAPIURL:                read(EnvIPFSAPIURL),
		IPFSGatewayURL:            read(EnvIPFSGatewayURL),
	}

	if len(missing) > 0 {
		return nil, &MissingError{Names: missing}
	}

	// The only value this file judges. Presence is not enough for this one: any
	// other value leaves the SDK attaching no trace identifier to the histogram,
	// nothing logs an error, and the loss is found at gate B with the dashboards
	// already written. A start that fails is the cheapest place to find it.
	if config.OTelMetricsExemplarFilter != ExemplarFilterTraceBased {
		return nil, &InvalidError{
			Name:  EnvOTelMetricsExemplarFilter,
			Value: config.OTelMetricsExemplarFilter,
			Want:  ExemplarFilterTraceBased,
		}
	}
	return config, nil
}
