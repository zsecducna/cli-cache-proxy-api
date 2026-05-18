package handlers

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const OpenAICompatibilityProvider = "openai-compatibility"
const CodexProvider = "codex"

type RequestRoute string

const (
	RequestRouteDefault               RequestRoute = ""
	RequestRouteClaudeViaOpenAICompat RequestRoute = "claude_via_openai_compat"
	RequestRouteClaudeMessages        RequestRoute = RequestRoute(coreexecutor.ClaudeMessagesRouteMetadataValue)
)

type requestRoute struct {
	Route          RequestRoute
	RequestedModel string
}

type requestDetails struct {
	Providers       []string
	NormalizedModel string
	RequestedModel  string
	Route           RequestRoute
}

var claudeViaGPTProviders = []string{CodexProvider, OpenAICompatibilityProvider}

func classifyRequestRoute(handlerType, modelName string) requestRoute {
	trimmedModel := strings.TrimSpace(modelName)
	if strings.TrimSpace(handlerType) == constant.Claude && strings.HasPrefix(trimmedModel, "gpt-") {
		return requestRoute{
			Route:          RequestRouteClaudeViaOpenAICompat,
			RequestedModel: trimmedModel,
		}
	}
	return requestRoute{
		Route:          RequestRouteDefault,
		RequestedModel: trimmedModel,
	}
}

func (h *BaseAPIHandler) RequestRouteClass(handlerType, modelName string) RequestRoute {
	return classifyRequestRoute(handlerType, modelName).Route
}
