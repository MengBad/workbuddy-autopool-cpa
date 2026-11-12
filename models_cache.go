// Model caches: the per-account dynamic model list and the alias table. CPA
// re-queries model.static / model.for_auth on every config reload, and neither
// should fan out to upstream each time.
package main

import (
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// dynamicModelsCacheTTL bounds how long a fetched model list is reused.
// model.static / model.for_auth are re-invoked by CPA on every config reload
// and on each models query; without caching, every reload fans out to one
// upstream call per account.
const dynamicModelsCacheTTL = 5 * time.Minute

type dynamicModelsCacheEntry struct {
	models  []pluginapi.ModelInfo
	fetched time.Time
}

var dynamicModelsCache struct {
	sync.RWMutex
	byKey map[string]dynamicModelsCacheEntry
}

// dynamicModelCall is an in-flight upstream model discovery for one cache key.
// Concurrent callers for the same account wait on the same call instead of
// fanning out duplicate /models requests to CodeBuddy (thundering herd).
type dynamicModelCall struct {
	done   chan struct{}
	models []pluginapi.ModelInfo
}

var dynamicModelFlights sync.Map // cache key (string) -> *dynamicModelCall

//
// CPA applies oauth-model-alias to the models this plugin registers, so the
// gateway may route a request whose model ID is an alias (e.g.
// "point/deepseek-v4-flash") to this executor. The upstream only knows the
// real model IDs, so the plugin must map the alias back before forwarding.
//
// ExecutorRequest carries no host config, so the alias table is cached from
// the AuthModelRequest.Host summary every time the host asks for models
// (model.static / model.for_auth are re-queried by CPA on config reload,
// keeping this cache in sync with oauth-model-alias changes). Auth-level
// attribute overrides ("model_alias"/"model-alias"/"oauth-model-alias")
// are parsed per request and take precedence over the global table.

var modelAliasCache struct {
	sync.RWMutex
	byAlias map[string]string
}
