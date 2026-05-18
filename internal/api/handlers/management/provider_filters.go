package management

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
)

func providerNamesForUsageFilter(cfg *config.Config, provider string) []string {
	return usage.ProviderNamesForFilter(provider, openAICompatibleProviderNames(cfg))
}

func openAICompatibleProviderNames(cfg *config.Config) []string {
	if cfg == nil || len(cfg.OpenAICompatibility) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.OpenAICompatibility))
	for i := range cfg.OpenAICompatibility {
		name := strings.ToLower(strings.TrimSpace(cfg.OpenAICompatibility[i].Name))
		if name == "" {
			name = "openai-compatibility"
		}
		names = append(names, name)
	}
	return names
}
