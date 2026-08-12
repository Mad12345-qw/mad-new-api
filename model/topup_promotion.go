package model

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const NewUserSixPromotionCode = "new_user_6_to_8"

var ErrTopUpAmountInvalid = errors.New("invalid topup amount")

type TopUpPromotionClaim struct {
	Id            int    `json:"id"`
	UserId        int    `json:"user_id" gorm:"uniqueIndex:idx_topup_promotion_user_code"`
	PromotionCode string `json:"promotion_code" gorm:"type:varchar(64);uniqueIndex:idx_topup_promotion_user_code"`
	TradeNo       string `json:"trade_no" gorm:"type:varchar(255);uniqueIndex"`
	CreateTime    int64  `json:"create_time"`
}

type TopUpPromotionQuote struct {
	CreditedQuota int64
	BonusQuota    int64
	PromotionCode string
}

type TopUpCompletion struct {
	TopUp            *TopUp
	CreditedQuota    int64
	BonusQuota       int64
	AlreadyCompleted bool
}

func quotaForTopUpAmount(amount int64, multiplier string) int64 {
	return decimal.NewFromInt(amount).
		Mul(decimal.RequireFromString(multiplier)).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
		IntPart()
}

func baseQuotaForTopUpAmount(amount int64) int64 {
	return quotaForTopUpAmount(amount, "1")
}

func HasTopUpPromotionClaim(userId int, promotionCode string) (bool, error) {
	var count int64
	err := DB.Model(&TopUpPromotionClaim{}).
		Where("user_id = ? AND promotion_code = ?", userId, promotionCode).
		Count(&count).Error
	return count > 0, err
}

func topUpPromotionQuoteForAmount(amount int64, includeSixPromotion bool) (TopUpPromotionQuote, error) {
	if amount < 1 {
		return TopUpPromotionQuote{}, ErrTopUpAmountInvalid
	}

	baseQuota := baseQuotaForTopUpAmount(amount)
	quote := TopUpPromotionQuote{CreditedQuota: baseQuota}

	switch amount {
	case 6:
		if includeSixPromotion {
			quote.CreditedQuota = quotaForTopUpAmount(8, "1")
			quote.BonusQuota = quote.CreditedQuota - baseQuota
			quote.PromotionCode = NewUserSixPromotionCode
		}
	case 30:
		quote.CreditedQuota = quotaForTopUpAmount(amount, "1.05")
	case 68:
		quote.CreditedQuota = quotaForTopUpAmount(amount, "1.08")
	case 128:
		quote.CreditedQuota = quotaForTopUpAmount(amount, "1.12")
	case 328:
		quote.CreditedQuota = quotaForTopUpAmount(amount, "1.18")
	case 648:
		quote.CreditedQuota = quotaForTopUpAmount(amount, "1.25")
	}

	if quote.BonusQuota == 0 {
		quote.BonusQuota = quote.CreditedQuota - baseQuota
	}
	return quote, nil
}

func GetTopUpPromotionQuote(userId int, amount int64) (TopUpPromotionQuote, error) {
	includeSixPromotion := false
	if amount == 6 {
		claimed, err := HasTopUpPromotionClaim(userId, NewUserSixPromotionCode)
		if err != nil {
			return TopUpPromotionQuote{}, err
		}
		includeSixPromotion = !claimed
	}
	return topUpPromotionQuoteForAmount(amount, includeSixPromotion)
}

func CompleteEpayTopUp(tradeNo string, actualPaymentMethod string) (*TopUpCompletion, error) {
	if tradeNo == "" {
		return nil, ErrTopUpNotFound
	}

	refCol := "`trade_no`"
	if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
		refCol = `"trade_no"`
	}

	result := &TopUpCompletion{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		topUp := &TopUp{}
		if err := lockForUpdate(tx).Where(refCol+" = ?", tradeNo).First(topUp).Error; err != nil {
			return ErrTopUpNotFound
		}
		if topUp.PaymentProvider != PaymentProviderEpay {
			return ErrPaymentMethodMismatch
		}
		if topUp.Status == common.TopUpStatusSuccess {
			result.TopUp = topUp
			result.CreditedQuota = topUp.CreditedQuota
			result.BonusQuota = topUp.BonusQuota
			result.AlreadyCompleted = true
			return nil
		}
		if topUp.Status != common.TopUpStatusPending {
			return ErrTopUpStatusInvalid
		}

		baseQuota := baseQuotaForTopUpAmount(topUp.Amount)
		creditedQuota := topUp.CreditedQuota
		bonusQuota := topUp.BonusQuota
		promotionCode := topUp.PromotionCode
		if creditedQuota <= 0 {
			quote, err := topUpPromotionQuoteForAmount(topUp.Amount, topUp.Amount == 6)
			if err != nil {
				return err
			}
			creditedQuota = quote.CreditedQuota
			bonusQuota = quote.BonusQuota
			promotionCode = quote.PromotionCode
		}

		if promotionCode != "" {
			claim := &TopUpPromotionClaim{
				UserId:        topUp.UserId,
				PromotionCode: promotionCode,
				TradeNo:       topUp.TradeNo,
				CreateTime:    common.GetTimestamp(),
			}
			claimResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(claim)
			if claimResult.Error != nil {
				return claimResult.Error
			}
			if claimResult.RowsAffected == 0 {
				creditedQuota = baseQuota
				bonusQuota = 0
				promotionCode = ""
			}
		}

		if creditedQuota <= 0 {
			return ErrTopUpAmountInvalid
		}
		if actualPaymentMethod != "" {
			topUp.PaymentMethod = actualPaymentMethod
		}
		topUp.CreditedQuota = creditedQuota
		topUp.BonusQuota = bonusQuota
		topUp.PromotionCode = promotionCode
		topUp.CompleteTime = common.GetTimestamp()
		topUp.Status = common.TopUpStatusSuccess
		if err := tx.Save(topUp).Error; err != nil {
			return err
		}
		if err := tx.Model(&User{}).
			Where("id = ?", topUp.UserId).
			Update("quota", gorm.Expr("quota + ?", creditedQuota)).Error; err != nil {
			return err
		}

		result.TopUp = topUp
		result.CreditedQuota = creditedQuota
		result.BonusQuota = bonusQuota
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
