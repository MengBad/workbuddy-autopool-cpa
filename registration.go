// Registration: the capability envelope CPA reads at plugin load, the
// envelope types every handler returns, and the build-time identity vars.
package main

import (
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// -----------------------------------------------------------------------------
// Registration & models
// -----------------------------------------------------------------------------

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type registrationCapability struct {
	ModelProvider         bool                         `json:"model_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	FrontendAuthProvider  bool                         `json:"frontend_auth_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats,omitempty"`
	Scheduler             bool                         `json:"scheduler"`
	ManagementAPI         bool                         `json:"management_api"`
	UsagePlugin           bool                         `json:"usage_plugin"`
}

// Build-time identity overrides are used by the isolated dev CPA build. The
// normal release keeps the production defaults; -ldflags -X can label a test
// binary without changing the provider/auth identifier (workbuddy).
var (
	version = "0.15.0"
	// The provider/auth identifier stays "workbuddy" (credential files, model
	// ids, config keys and CPAMP cards depend on it); only the display name
	// reflects the AutoPool layer.
	pluginRouteID     = providerName
	pluginDisplayName = "WorkBuddy Intl AutoPool CPA"
	pluginMenuName    = "CodeBuddy"
	pluginPanelBrand  = "CodeBuddy"
)

func wbRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginDisplayName,
			Version:          version,
			Author:           "MengBad (maintained fork of zidanefaqih/codebuddy-intl-cpa; based on Sliverkiss/cpa-plugin and workbuddy by lovingfish)",
			GitHubRepository: "https://github.com/MengBad/workbuddy-autopool-cpa",
			Logo:             pluginLogoURL,
			ConfigFields: append([]pluginapi.ConfigField{
				{Name: "checkin_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable daily auto check-in at 09:00 and 21:00 local time for CN accounts (default true)."},
				{Name: "lifecycle_auto", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Auto disable CN / delete Global when credits exhausted; re-enable CN after check-in restores credits (default true)."},
				{Name: "token_keepalive", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable daily access-token refresh at 22:00 local time to prevent Keycloak offline-session expiry (default true)."},
				{Name: "daily_bonus", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable the Global daily credit reward at 08:00/12:00/16:00/20:00 local time: reports one growth activity event so each Global account earns its 30-credit daily Bonus Pack. Idempotent per account per day (default true)."},
				{Name: "usage_report_url", Type: pluginapi.ConfigFieldTypeString, Description: "Optional override of CPAMP usage import URL (default http://cpa-manager-plus:18317/v0/management/usage/import; also env USAGE_REPORT_URL)."},
				{Name: "usage_report_key", Type: pluginapi.ConfigFieldTypeString, Description: "Optional CPAMP admin key override. Prefer auto-detect from env CPAMP_ADMIN_KEY / USAGE_REPORT_KEY or secret file /run/secrets/cpamp_admin_key."},
				{Name: "self_url", Type: pluginapi.ConfigFieldTypeString, Description: "Base URL of this CPA host's management API used for the post-save model-registry live reload (default http://127.0.0.1:8317; also env WB_SELF_URL). Set the actual port on non-default deployments."},
				{Name: "mm_live_reload", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Trigger a host model-registry reload after saving Model Manager settings so /v1/models updates without a CPA restart (default true)."},
				{Name: "oauth_region", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{oauthRegionIntl, oauthRegionCN}, Description: "Which realm issues the OAuth login URL (default intl). intl issues on www.codebuddy.ai and keeps the login page on .ai; cn issues on copilot.tencent.com and serves the .cn login page. Also env WB_OAUTH_REGION."},
			}, poolConfigFields()...),
		},
		Capabilities: registrationCapability{
			ModelProvider:         true,
			AuthProvider:          true,
			FrontendAuthProvider:  false,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{"chat-completions"},
			ExecutorOutputFormats: []string{"chat-completions"},
			ManagementAPI:         true,
			Scheduler:             false,
			UsagePlugin:           true,
		},
	}
}
