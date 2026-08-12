package model

import (
	"fmt"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createPromotionTestUser(t *testing.T, username string) *User {
	t.Helper()
	user := &User{Username: username, Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(user).Error)
	return user
}

func createPromotionTestTopUp(t *testing.T, userId int, tradeNo string, amount int64) *TopUp {
	t.Helper()
	quote, err := GetTopUpPromotionQuote(userId, amount)
	require.NoError(t, err)
	topUp := &TopUp{
		UserId:          userId,
		Amount:          amount,
		Money:           float64(amount),
		CreditedQuota:   quote.CreditedQuota,
		BonusQuota:      quote.BonusQuota,
		PromotionCode:   quote.PromotionCode,
		TradeNo:         tradeNo,
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
		CreateTime:      common.GetTimestamp(),
	}
	require.NoError(t, topUp.Insert())
	return topUp
}

func readPromotionTestQuota(t *testing.T, userId int) int {
	t.Helper()
	var user User
	require.NoError(t, DB.First(&user, userId).Error)
	return user.Quota
}

func TestNewUserSixPromotionOnlyAppliesOnce(t *testing.T) {
	truncateTables(t)
	user := createPromotionTestUser(t, "six-once")

	first := createPromotionTestTopUp(t, user.Id, "six-first", 6)
	assert.Equal(t, NewUserSixPromotionCode, first.PromotionCode)
	firstCompletion, err := CompleteEpayTopUp(first.TradeNo, "alipay")
	require.NoError(t, err)
	assert.Equal(t, quotaForTopUpAmount(8, "1"), firstCompletion.CreditedQuota)
	assert.Equal(t, quotaForTopUpAmount(2, "1"), firstCompletion.BonusQuota)

	second := createPromotionTestTopUp(t, user.Id, "six-second", 6)
	assert.Empty(t, second.PromotionCode)
	secondCompletion, err := CompleteEpayTopUp(second.TradeNo, "alipay")
	require.NoError(t, err)
	assert.Equal(t, baseQuotaForTopUpAmount(6), secondCompletion.CreditedQuota)
	assert.Zero(t, secondCompletion.BonusQuota)
	assert.Equal(t, quotaForTopUpAmount(14, "1"), int64(readPromotionTestQuota(t, user.Id)))
}

func TestCompleteEpayTopUpIsIdempotent(t *testing.T) {
	truncateTables(t)
	user := createPromotionTestUser(t, "idempotent")
	topUp := createPromotionTestTopUp(t, user.Id, "duplicate-callback", 30)

	first, err := CompleteEpayTopUp(topUp.TradeNo, "alipay")
	require.NoError(t, err)
	second, err := CompleteEpayTopUp(topUp.TradeNo, "alipay")
	require.NoError(t, err)
	assert.False(t, first.AlreadyCompleted)
	assert.True(t, second.AlreadyCompleted)
	assert.Equal(t, int(first.CreditedQuota), readPromotionTestQuota(t, user.Id))
}

func TestConcurrentSixPromotionClaimsOnlyOneBonus(t *testing.T) {
	truncateTables(t)
	user := createPromotionTestUser(t, "six-concurrent")
	first := createPromotionTestTopUp(t, user.Id, "six-concurrent-a", 6)
	second := createPromotionTestTopUp(t, user.Id, "six-concurrent-b", 6)

	orders := []*TopUp{first, second}
	completions := make([]*TopUpCompletion, len(orders))
	errs := make([]error, len(orders))
	var wg sync.WaitGroup
	for index, order := range orders {
		wg.Add(1)
		go func(index int, tradeNo string) {
			defer wg.Done()
			completions[index], errs[index] = CompleteEpayTopUp(tradeNo, "alipay")
		}(index, order.TradeNo)
	}
	wg.Wait()

	bonusCount := 0
	for index, err := range errs {
		require.NoError(t, err)
		if completions[index].BonusQuota > 0 {
			bonusCount++
		}
	}
	assert.Equal(t, 1, bonusCount)
	assert.Equal(t, quotaForTopUpAmount(14, "1"), int64(readPromotionTestQuota(t, user.Id)))
}

func TestTopUpPromotionTierCalculations(t *testing.T) {
	truncateTables(t)
	user := createPromotionTestUser(t, "tier-calculations")
	tests := []struct {
		amount     int64
		multiplier string
	}{
		{30, "1.05"},
		{68, "1.08"},
		{128, "1.12"},
		{328, "1.18"},
		{648, "1.25"},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("amount_%d", test.amount), func(t *testing.T) {
			quote, err := GetTopUpPromotionQuote(user.Id, test.amount)
			require.NoError(t, err)
			assert.Equal(t, quotaForTopUpAmount(test.amount, test.multiplier), quote.CreditedQuota)
			assert.Equal(t, quote.CreditedQuota-baseQuotaForTopUpAmount(test.amount), quote.BonusQuota)
		})
	}
}

func TestCustomAmountsUnderThirtyDoNotReceiveBonus(t *testing.T) {
	truncateTables(t)
	user := createPromotionTestUser(t, "custom-no-bonus")
	for amount := int64(1); amount < 30; amount++ {
		if amount == 6 {
			continue
		}
		quote, err := GetTopUpPromotionQuote(user.Id, amount)
		require.NoError(t, err)
		assert.Equal(t, baseQuotaForTopUpAmount(amount), quote.CreditedQuota, "amount=%d", amount)
		assert.Zero(t, quote.BonusQuota, "amount=%d", amount)
		assert.Empty(t, quote.PromotionCode, "amount=%d", amount)
	}
}
