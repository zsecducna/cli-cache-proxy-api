package customerstate

import (
	"context"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func init() {
	coreusage.RegisterPlugin(NewUsageDebitPlugin())
}

type UsageDebitPlugin struct{}

func NewUsageDebitPlugin() *UsageDebitPlugin { return &UsageDebitPlugin{} }

func (p *UsageDebitPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	_ = ctx
	_ = record
}
