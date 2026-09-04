package config

import (
	"errors"
	"strings"
	"testing"
)

// completeEnvironment is what the manifests set. Every case below starts from it
// and takes something away, so a test that passes proves the absence mattered.
func completeEnvironment() map[string]string {
	return map[string]string{
		EnvOTelServiceName:           "lgtm-backend",
		EnvOTelExporterOTLPEndpoint:  "http://otel-collector.lgtm.svc.cluster.local:4317",
		EnvOTelMetricsExemplarFilter: "trace_based",
		EnvIPFSAPIURL:                "http://kubo.lgtm.svc.cluster.local:5001",
		EnvIPFSGatewayURL:            "http://ipfs.lgtm.local",
	}
}

func lookupIn(environment map[string]string) func(string) string {
	return func(name string) string { return environment[name] }
}

func TestLoadReadsEveryVariable(t *testing.T) {
	config, err := Load(lookupIn(completeEnvironment()))
	if err != nil {
		t.Fatalf("Load returned %v, want no error", err)
	}

	cases := map[string]struct{ got, want string }{
		EnvOTelServiceName:           {config.OTelServiceName, "lgtm-backend"},
		EnvOTelExporterOTLPEndpoint:  {config.OTelExporterOTLPEndpoint, "http://otel-collector.lgtm.svc.cluster.local:4317"},
		EnvOTelMetricsExemplarFilter: {config.OTelMetricsExemplarFilter, "trace_based"},
		EnvIPFSAPIURL:                {config.IPFSAPIURL, "http://kubo.lgtm.svc.cluster.local:5001"},
		EnvIPFSGatewayURL:            {config.IPFSGatewayURL, "http://ipfs.lgtm.local"},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", name, c.got, c.want)
		}
	}
}

func TestLoadRefusesAMissingVariable(t *testing.T) {
	for _, name := range []string{
		EnvOTelServiceName,
		EnvOTelExporterOTLPEndpoint,
		EnvOTelMetricsExemplarFilter,
		EnvIPFSAPIURL,
		EnvIPFSGatewayURL,
	} {
		t.Run(name, func(t *testing.T) {
			environment := completeEnvironment()
			delete(environment, name)

			_, err := Load(lookupIn(environment))
			if err == nil {
				t.Fatalf("Load succeeded without %s, want an error", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("the message is %q, and it does not name %s", err, name)
			}
		})
	}
}

// A manifest that sets a variable to spaces is a typo, and starting anyway would
// hide it until a dashboard drew nothing.
func TestLoadTreatsBlankAsMissing(t *testing.T) {
	environment := completeEnvironment()
	environment[EnvIPFSAPIURL] = "   "

	_, err := Load(lookupIn(environment))
	if err == nil {
		t.Fatalf("Load accepted a blank %s, want an error", EnvIPFSAPIURL)
	}
	if !strings.Contains(err.Error(), EnvIPFSAPIURL) {
		t.Errorf("the message is %q, and it does not name %s", err, EnvIPFSAPIURL)
	}
}

// Two typos should cost one restart, not two.
func TestLoadNamesEveryMissingVariableAtOnce(t *testing.T) {
	environment := completeEnvironment()
	delete(environment, EnvOTelServiceName)
	delete(environment, EnvIPFSGatewayURL)

	_, err := Load(lookupIn(environment))

	var missing *MissingError
	if !errors.As(err, &missing) {
		t.Fatalf("Load returned %v, want a *MissingError", err)
	}
	if len(missing.Names) != 2 {
		t.Fatalf("MissingError names %v, want both variables", missing.Names)
	}
	for _, name := range []string{EnvOTelServiceName, EnvIPFSGatewayURL} {
		if !strings.Contains(missing.Error(), name) {
			t.Errorf("the message is %q, and it does not name %s", missing, name)
		}
	}
}

// The value that fails in silence. always_on and always_off are the other two
// values the specification allows, and both leave the histogram with no trace
// identifier: the dashboards then draw, the exemplar click does nothing, and
// nothing anywhere says why. Gate B is far too late to learn it.
func TestLoadRefusesAnExemplarFilterThatIsNotTraceBased(t *testing.T) {
	for _, value := range []string{"always_on", "always_off", "trace-based", "TRACE_BASED"} {
		t.Run(value, func(t *testing.T) {
			environment := completeEnvironment()
			environment[EnvOTelMetricsExemplarFilter] = value

			_, err := Load(lookupIn(environment))

			var invalid *InvalidError
			if !errors.As(err, &invalid) {
				t.Fatalf("Load returned %v for %q, want an *InvalidError", err, value)
			}
			if invalid.Name != EnvOTelMetricsExemplarFilter {
				t.Errorf("the error names %s, want %s", invalid.Name, EnvOTelMetricsExemplarFilter)
			}
			// The message has to carry both halves. A reader holding a manifest
			// needs to see what is written and what to write instead.
			for _, part := range []string{value, ExemplarFilterTraceBased} {
				if !strings.Contains(invalid.Error(), part) {
					t.Errorf("the message is %q, and it does not carry %q", invalid, part)
				}
			}
		})
	}
}

// The variable is still required. An absent one is a MissingError, not an
// InvalidError, because the two are fixed by different edits of the manifest.
func TestLoadCallsAnAbsentExemplarFilterMissingAndNotInvalid(t *testing.T) {
	environment := completeEnvironment()
	delete(environment, EnvOTelMetricsExemplarFilter)

	_, err := Load(lookupIn(environment))

	var missing *MissingError
	if !errors.As(err, &missing) {
		t.Fatalf("Load returned %v, want a *MissingError", err)
	}
}
