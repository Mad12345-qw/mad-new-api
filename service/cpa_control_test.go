package service

import (
	"sync/atomic"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cpaTestBilling struct {
	refunds atomic.Int32
}

func (*cpaTestBilling) Settle(int) error                { return nil }
func (b *cpaTestBilling) Refund(*gin.Context)           { b.refunds.Add(1) }
func (*cpaTestBilling) NeedsRefund() bool               { return true }
func (*cpaTestBilling) GetPreConsumedQuota() int        { return 1 }
func (*cpaTestBilling) Reserve(int) error               { return nil }

func TestCPAControlTicketRefundIsExactlyOnce(t *testing.T) {
	billing := &cpaTestBilling{}
	ctx, _ := gin.CreateTestContext(nil)
	ticket := "test-refund-exactly-once"
	RegisterCPAPending(ctx, ticket, "auth-1", &relaycommon.RelayInfo{Billing: billing})
	require.NoError(t, RefundCPADispatch(ctx, ticket))
	require.Error(t, RefundCPADispatch(ctx, ticket))
	require.Equal(t, int32(1), billing.refunds.Load())
}
