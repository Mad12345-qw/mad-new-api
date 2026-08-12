/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  RECHARGE_PROMOTION_AMOUNT_OPTIONS,
  getRechargePromotionQuote,
} from './types'

const allPromotions = {
  new_user_six_eligible: true,
  amount_options: [...RECHARGE_PROMOTION_AMOUNT_OPTIONS],
}

describe('recharge promotion quotes', () => {
  test('calculates every supported promotion tier', () => {
    const expected = [
      [6, 2, 33.3, 8],
      [30, 1.5, 5, 31.5],
      [68, 5.44, 8, 73.44],
      [128, 15.36, 12, 143.36],
      [328, 59.04, 18, 387.04],
      [648, 162, 25, 810],
    ] as const

    for (const [amount, bonus, rate, credited] of expected) {
      assert.deepEqual(getRechargePromotionQuote(allPromotions, amount), {
        amount,
        creditedAmount: credited,
        bonusAmount: bonus,
        bonusRatePercent: rate,
        promotional: true,
      })
    }
  })

  test('removes the first 6 yuan promotion when the server says it was claimed', () => {
    const quote = getRechargePromotionQuote(
      { ...allPromotions, new_user_six_eligible: false },
      6
    )

    assert.deepEqual(quote, {
      amount: 6,
      creditedAmount: 6,
      bonusAmount: 0,
      bonusRatePercent: 0,
      promotional: false,
    })
  })

  test('returns no display quote for an amount outside the server contract', () => {
    assert.equal(getRechargePromotionQuote(allPromotions, 10), null)
  })
})

describe('recharge promotion compatibility', () => {
  test('keeps the legacy wallet display when the backend field is missing', () => {
    assert.equal(getRechargePromotionQuote(undefined, 6), null)
    assert.equal(getRechargePromotionQuote(undefined, 648), null)
  })
})
