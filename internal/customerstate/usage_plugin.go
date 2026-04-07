package customerstate

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

func init() {
	coreusage.RegisterPlugin(NewUsageDebitPlugin())
}

type UsageDebitPlugin struct{}

func NewUsageDebitPlugin() *UsageDebitPlugin { return &UsageDebitPlugin{} }

func (p *UsageDebitPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if record.Failed {
		return
	}
	customerID := record.CustomerID
	if customerID == "" {
		return
	}
	amount := usageTokenAmount(record.Detail)
	if amount <= 0 {
		return
	}
	svc, err := DefaultService()
	if err != nil {
		log.WithError(err).Warn("customer state: usage debit plugin unavailable")
		return
	}
	if _, _, err := svc.RecordUsageDebit(customerID, amount, logging.GetRequestID(ctx), record.Provider, record.Model); err != nil {
		log.WithError(err).WithField("customer_id", customerID).Warn("customer state: failed to record usage debit")
	}
}

func usageTokenAmount(detail coreusage.Detail) int64 {
	if detail.TotalTokens > 0 {
		return detail.TotalTokens
	}
	total := detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	if total > 0 {
		return total
	}
	return detail.CachedTokens
}
