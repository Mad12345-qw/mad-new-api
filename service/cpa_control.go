package service

import (
	"errors"
	"sync"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

const cpaPendingTTL = 15 * time.Minute

var errCPATicketNotFound = errors.New("CPA billing ticket not found or already finalized")

type CPAPendingDispatch struct {
	Ticket    string
	AuthID    string
	RelayInfo *relaycommon.RelayInfo
	Keys      map[string]any
	CreatedAt time.Time
}

type CPAUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	ReasoningTokens     int `json:"reasoning_tokens"`
	CachedTokens        int `json:"cached_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
	TotalTokens         int `json:"total_tokens"`
}

var cpaPending = struct {
	sync.Mutex
	items map[string]*CPAPendingDispatch
}{items: make(map[string]*CPAPendingDispatch)}

func RegisterCPAPending(c *gin.Context, ticket, authID string, info *relaycommon.RelayInfo) {
	entry := &CPAPendingDispatch{
		Ticket: ticket, AuthID: authID, RelayInfo: info, CreatedAt: time.Now(),
		Keys: make(map[string]any),
	}
	if c != nil {
		copyContext := c.Copy()
		for key, value := range copyContext.Keys {
			entry.Keys[key] = value
		}
	}

	var expired []*CPAPendingDispatch
	cpaPending.Lock()
	cpaPending.items[ticket] = entry
	cutoff := time.Now().Add(-cpaPendingTTL)
	for key, candidate := range cpaPending.items {
		if candidate != nil && candidate.CreatedAt.Before(cutoff) {
			expired = append(expired, candidate)
			delete(cpaPending.items, key)
		}
	}
	cpaPending.Unlock()

	for _, candidate := range expired {
		refundCPAPending(c, candidate)
	}
}

func takeCPAPending(ticket string) (*CPAPendingDispatch, error) {
	cpaPending.Lock()
	defer cpaPending.Unlock()
	entry := cpaPending.items[ticket]
	if entry == nil {
		return nil, errCPATicketNotFound
	}
	delete(cpaPending.items, ticket)
	return entry, nil
}

func restoreCPAContext(c *gin.Context, entry *CPAPendingDispatch) {
	if c == nil || entry == nil {
		return
	}
	for key, value := range entry.Keys {
		c.Set(key, value)
	}
}

func refundCPAPending(c *gin.Context, entry *CPAPendingDispatch) {
	if entry == nil || entry.RelayInfo == nil || entry.RelayInfo.Billing == nil {
		return
	}
	restoreCPAContext(c, entry)
	entry.RelayInfo.Billing.Refund(c)
}

func RefundCPADispatch(c *gin.Context, ticket string) error {
	entry, err := takeCPAPending(ticket)
	if err != nil {
		return err
	}
	refundCPAPending(c, entry)
	return nil
}

func SettleCPADispatch(c *gin.Context, ticket string, usage CPAUsage) error {
	entry, err := takeCPAPending(ticket)
	if err != nil {
		return err
	}
	restoreCPAContext(c, entry)
	if entry.RelayInfo == nil {
		return errors.New("CPA billing ticket has no relay information")
	}

	input := usage.InputTokens
	if input == 0 {
		input = usage.TotalTokens - usage.OutputTokens
		if input < 0 {
			input = 0
		}
	}
	billingUsage := &dto.Usage{
		PromptTokens:     input,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.TotalTokens,
		InputTokens:      input,
		OutputTokens:     usage.OutputTokens,
		UsageSource:      "cpa-official-usage",
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         max(usage.CachedTokens, usage.CacheReadTokens),
			CachedCreationTokens: usage.CacheCreationTokens,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: usage.ReasoningTokens},
	}
	PostTextConsumeQuota(c, entry.RelayInfo, billingUsage, nil)
	return nil
}
