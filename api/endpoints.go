package api

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type EndpointMap struct {
	Bases    Bases                       `yaml:"bases"`
	Bearer   string                      `yaml:"bearer"`
	Features map[string]bool             `yaml:"features"`
	GraphQL  map[string]GraphQLEndpoint  `yaml:"graphql"`
	REST     map[string]RESTEndpoint     `yaml:"rest"`
}

type Bases struct {
	GraphQL string `yaml:"graphql"`
	REST    string `yaml:"rest"`
	API     string `yaml:"api"`
}

type GraphQLEndpoint struct {
	QueryID       string  `yaml:"queryId"`
	OperationName string  `yaml:"operationName"`
	Kind          string  `yaml:"kind"`
	RPS           float64 `yaml:"rps"`
	Burst         int     `yaml:"burst"`
}

type RESTEndpoint struct {
	Path     string        `yaml:"path"`
	Method   string        `yaml:"method"`
	Kind     string        `yaml:"kind"`
	MinGap   time.Duration `yaml:"min_gap"`
	MaxGap   time.Duration `yaml:"max_gap"`
	DailyCap int           `yaml:"daily_cap"`
}

func LoadEndpoints(path string) (*EndpointMap, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read endpoints.yaml: %w", err)
	}
	var m EndpointMap
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse endpoints.yaml: %w", err)
	}
	if m.Bases.GraphQL == "" || m.Bearer == "" {
		return nil, fmt.Errorf("endpoints.yaml missing bases.graphql or bearer")
	}
	// yaml.v3 parses a bare integer on a time.Duration field as nanoseconds,
	// so `min_gap: 8` silently yields 8ns. That would defeat the mutation
	// throttle. Require durations to be at least 1ms for mutation endpoints.
	const minDur = time.Millisecond
	for name, ep := range m.REST {
		if ep.Kind != "mutation" {
			continue
		}
		if ep.MinGap > 0 && ep.MinGap < minDur {
			return nil, fmt.Errorf(
				"endpoints.yaml: rest.%s.min_gap = %v is suspiciously small; "+
					"use a string like \"8s\" not a bare integer",
				name, ep.MinGap)
		}
		if ep.MaxGap > 0 && ep.MaxGap < minDur {
			return nil, fmt.Errorf(
				"endpoints.yaml: rest.%s.max_gap = %v is suspiciously small; "+
					"use a string like \"22s\" not a bare integer",
				name, ep.MaxGap)
		}
	}
	return &m, nil
}
