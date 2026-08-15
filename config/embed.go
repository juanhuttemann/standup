// Package defaults embeds the committed config files so a fresh install
// works with zero configuration. The yaml files remain the single home of
// every setting; this package is build glue only.
package defaults

import _ "embed"

//go:embed config.yaml
var ConfigYAML string

//go:embed agent.yaml
var AgentYAML string
