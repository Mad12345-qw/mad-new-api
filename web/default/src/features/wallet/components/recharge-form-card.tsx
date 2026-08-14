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
import {
  Check,
  Gift,
  ExternalLink,
  JapaneseYen,
  Loader2,
  Receipt,
} from "lucide-react";
import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { IconBadge } from "@/components/ui/icon-badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { TitledCard } from "@/components/ui/titled-card";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { formatNumber } from "@/lib/format";
import { cn } from "@/lib/utils";

import { formatCurrency, getPaymentIcon, getMinTopupAmount } from "../lib";
import type {
  PaymentMethod,
  PresetAmount,
  TopupInfo,
  CreemProduct,
  WaffoPayMethod,
} from "../types";
import { CreemProductsSection } from "./creem-products-section";

interface RechargeFormCardProps {
  topupInfo: TopupInfo | null;
  presetAmounts: PresetAmount[];
  selectedPreset: number | null;
  onSelectPreset: (preset: PresetAmount) => void;
  topupAmount: number;
  onTopupAmountChange: (amount: number) => void;
  paymentAmount: number;
  calculating: boolean;
  onPaymentMethodSelect: (method: PaymentMethod) => void;
  paymentLoading: string | null;
  redemptionCode: string;
  onRedemptionCodeChange: (code: string) => void;
  onRedeem: () => void;
  redeeming: boolean;
  topupLink?: string;
  loading?: boolean;
  priceRatio?: number;
  usdExchangeRate?: number;
  onOpenBilling?: () => void;
  creemProducts?: CreemProduct[];
  enableCreemTopup?: boolean;
  onCreemProductSelect?: (product: CreemProduct) => void;
  enableWaffoTopup?: boolean;
  waffoPayMethods?: WaffoPayMethod[];
  waffoMinTopup?: number;
  onWaffoMethodSelect?: (method: WaffoPayMethod, index: number) => void;
  enableWaffoPancakeTopup?: boolean;
}

const PROMOTION_RATES: Record<number, number> = {
  30: 0.05,
  68: 0.08,
  128: 0.12,
  328: 0.18,
  648: 0.25,
};

function getPromotionDetails(amount: number, newUserSixEligible: boolean) {
  if (amount === 6 && newUserSixEligible) {
    return { bonus: 2, credited: 8, rate: 2 / 6 };
  }
  const rate = PROMOTION_RATES[amount] || 0;
  const bonus = amount * rate;
  return { bonus, credited: amount + bonus, rate };
}

export function RechargeFormCard({
  topupInfo,
  presetAmounts,
  selectedPreset,
  onSelectPreset,
  topupAmount,
  onTopupAmountChange,
  paymentAmount,
  calculating,
  onPaymentMethodSelect,
  paymentLoading,
  redemptionCode,
  onRedemptionCodeChange,
  onRedeem,
  redeeming,
  topupLink,
  loading,
  priceRatio = 1,
  usdExchangeRate = 1,
  onOpenBilling,
  creemProducts,
  enableCreemTopup,
  onCreemProductSelect,
  enableWaffoTopup,
  waffoPayMethods,
  waffoMinTopup,
  onWaffoMethodSelect,
  enableWaffoPancakeTopup,
}: RechargeFormCardProps) {
  const { t } = useTranslation();
  const [localAmount, setLocalAmount] = useState("");

  useEffect(() => {
    setLocalAmount(selectedPreset === null ? topupAmount.toString() : "");
  }, [selectedPreset, topupAmount]);

  const handleAmountChange = (value: string) => {
    setLocalAmount(value);
    const numValue = Number.parseInt(value) || 0;
    if (numValue >= 0) {
      onTopupAmountChange(numValue);
    }
  };

  const hasConfigurableTopup =
    topupInfo?.enable_online_topup ||
    topupInfo?.enable_stripe_topup ||
    enableWaffoTopup ||
    enableWaffoPancakeTopup;
  const hasAnyTopup = hasConfigurableTopup || enableCreemTopup;
  const hasStandardPaymentMethods =
    Array.isArray(topupInfo?.pay_methods) && topupInfo.pay_methods.length > 0;
  const hasWaffoPaymentMethods =
    Array.isArray(waffoPayMethods) && waffoPayMethods.length > 0;
  const minTopup = getMinTopupAmount(topupInfo);
  const redemptionEnabled = topupInfo?.enable_redemption !== false;
  const newUserSixEligible =
    topupInfo?.recharge_promotion?.new_user_six_eligible !== false;
  const currentPromotion = getPromotionDetails(topupAmount, newUserSixEligible);
  const defaultPaymentMethod = topupInfo?.pay_methods?.[0];
  const quickTopupDisabled =
    !defaultPaymentMethod ||
    topupAmount < minTopup ||
    (defaultPaymentMethod.min_topup || 0) > topupAmount ||
    !!paymentLoading;
  const nextPromotionTier = presetAmounts.find(
    (preset) => preset.value > topupAmount,
  );
  const upgradeMessage = (() => {
    if (topupAmount === 6 && newUserSixEligible) {
      return "新客体验每个账户仅限 1 次 · 其他档位均可重复充值";
    }
    if (topupAmount === 648) {
      return "已解锁最高 25% 加赠 · 本档可重复充值，单次到账 ¥810";
    }
    if (nextPromotionTier) {
      const nextPromotion = getPromotionDetails(
        nextPromotionTier.value,
        newUserSixEligible,
      );
      const amountDifference = nextPromotionTier.value - topupAmount;
      if (
        currentPromotion.bonus > 0 &&
        nextPromotion.bonus > currentPromotion.bonus
      ) {
        const bonusDifference =
          nextPromotion.bonus - currentPromotion.bonus;
        const marginalRate = (bonusDifference / amountDifference) * 100;
        return `升至 ¥${nextPromotionTier.value}，再多充 ¥${formatNumber(amountDifference)} · 多得 ¥${formatNumber(bonusDifference)}，增量加赠约 ${formatNumber(marginalRate)}%`;
      }
      return `再充 ¥${formatNumber(amountDifference)} 升至 ¥${nextPromotionTier.value}，可获得 ${Math.round(nextPromotion.rate * 100)}% 加赠`;
    }
    return "当前金额按实付到账 · 选择活动档位可获得额外赠送";
  })();

  if (loading) {
    return (
      <Card data-card-hover="false" className="gap-0 overflow-hidden py-0">
        <CardHeader className="border-b p-3 !pb-3 sm:p-5 sm:!pb-5">
          <Skeleton className="h-6 w-32" />
          <Skeleton className="mt-2 h-4 w-48" />
        </CardHeader>
        <CardContent className="space-y-4 p-3 sm:space-y-6 sm:p-5">
          <div className="space-y-4 sm:space-y-6">
            {/* Preset Amounts Skeleton */}
            <div className="space-y-3">
              <Skeleton className="h-3 w-16" />
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {Array.from({ length: 6 }, (_, index) => `preset-${index}`).map(
                  (key) => (
                    <Skeleton key={key} className="h-[72px] rounded-lg" />
                  ),
                )}
              </div>
            </div>

            {/* Custom Amount Input Skeleton */}
            <div className="space-y-3">
              <Skeleton className="h-3 w-28" />
              <Skeleton className="h-[42px] w-full" />
            </div>

            {/* Payment Methods Skeleton */}
            <div className="space-y-3">
              <Skeleton className="h-3 w-32" />
              <div className="flex flex-wrap gap-3">
                {["primary", "secondary", "tertiary"].map((key) => (
                  <Skeleton key={key} className="h-10 w-24 rounded-lg" />
                ))}
              </div>
            </div>
          </div>

          {/* Redemption Code Section Skeleton */}
          <div className="space-y-3 border-t pt-8">
            <Skeleton className="h-3 w-24" />
            <div className="flex gap-2">
              <Skeleton className="h-10 flex-1" />
              <Skeleton className="h-10 w-20" />
            </div>
          </div>
        </CardContent>
      </Card>
    );
  }

  return (
    <TitledCard
      title={t("Add Funds")}
      description={t("Choose an amount and payment method")}
      icon={<JapaneseYen className="h-4 w-4" />}
      iconTone="success"
      iconClassName="bg-[#17140f] text-[#d6a84b]"
      titleClassName="text-sm sm:text-base"
      descriptionClassName="text-[10px] sm:text-xs"
      disableHoverEffect
      headerClassName="p-3 !pb-3 sm:p-4 sm:!pb-4"
      action={
        onOpenBilling ? (
          <Button
            variant="outline"
            size="sm"
            onClick={onOpenBilling}
            className="w-full gap-2 text-xs sm:w-auto"
          >
            <Receipt className="h-4 w-4" />
            {t("Order History")}
          </Button>
        ) : null
      }
      contentClassName="space-y-3 p-3 sm:space-y-4 sm:p-4"
    >
      {/* Online Topup Section */}
      {hasAnyTopup ? (
        <div className="space-y-3 sm:space-y-4">
          {hasConfigurableTopup && (
            <>
              {presetAmounts.length > 0 && (
                <div className="space-y-2">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <Label className="text-muted-foreground text-[10px] font-medium tracking-wider uppercase">
                      {t("Amount")}
                    </Label>
                    <span className="text-[10px] font-semibold text-[#d6b66c]">
                      最高加赠 25%，升档越多额度越高
                    </span>
                  </div>
                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 sm:gap-3 lg:grid-cols-3">
                    {presetAmounts.map((preset) => {
                      const promotion = getPromotionDetails(
                        preset.value,
                        newUserSixEligible,
                      );
                      const badge =
                        preset.value === 6
                          ? newUserSixEligible
                            ? "新客体验 · 加赠 33%"
                            : "体验礼已使用"
                          : preset.value === 128
                            ? "最受欢迎 · 加赠 12%"
                            : preset.value === 648
                              ? "最划算 · 加赠 25%"
                              : `加赠 ${Math.round(promotion.rate * 100)}%`;
                      return (
                        <Button
                          key={preset.value}
                          variant="outline"
                          className={cn(
                            "relative flex min-h-28 flex-col items-start justify-between overflow-hidden rounded-lg px-3 py-3 text-left whitespace-normal sm:min-h-28 sm:p-3.5",
                            selectedPreset === preset.value
                              ? "border-[#9b7b38] bg-[#d7b96f]/8 shadow-[inset_0_1px_0_rgba(255,255,255,0.04),0_8px_22px_rgba(0,0,0,0.14)]"
                              : "border-muted hover:border-[#8b6a2c]/65",
                          )}
                          onClick={() => onSelectPreset(preset)}
                        >
                          {selectedPreset === preset.value && (
                            <span className="absolute top-2 right-2 grid size-5 place-items-center rounded-full bg-[#d9ba6f] text-[#17140f]">
                              <Check className="size-3.5" />
                            </span>
                          )}
                          <div className="flex w-full items-start justify-between gap-2">
                            <div>
                              <div className="text-xs font-semibold sm:text-sm">
                                ¥{preset.value}
                              </div>
                              <div className="text-muted-foreground mt-1 text-[10px]">
                                实付 ¥
                                {formatCurrency(preset.value * priceRatio)}
                              </div>
                            </div>
                            {badge && (
                              <span
                                className={cn(
                                  "min-h-[29px] shrink-0 whitespace-nowrap rounded-full border border-[#8b6a2c]/55 bg-[#17140f] px-3 py-[7px] text-[11px] leading-none font-semibold text-[#e0bd70]",
                                  selectedPreset === preset.value && "mr-6",
                                )}
                              >
                                {badge}
                              </span>
                            )}
                          </div>
                          <div className="mt-2.5 grid w-full grid-cols-[auto_1fr_auto] items-end gap-1.5">
                            {promotion.bonus > 0 ? (
                              <span className="whitespace-nowrap text-xs font-bold text-[#e0bd70] [text-shadow:0_1px_0_#000] sm:text-sm">
                                赠 ¥
                                {formatNumber(
                                  promotion.bonus * usdExchangeRate,
                                )}
                              </span>
                            ) : (
                              <span className="text-muted-foreground text-[10px]">
                                无赠送
                              </span>
                            )}
                            <span className="whitespace-nowrap text-center text-[8px] font-semibold text-[#f0dca8] sm:text-[10px]">
                              {preset.value === 6 && newUserSixEligible
                                ? "每个账户限 1 次"
                                : ""}
                            </span>
                            <span className="whitespace-nowrap text-[10px] font-semibold text-foreground sm:text-xs">
                              到账 ¥
                              {formatNumber(
                                promotion.credited * usdExchangeRate,
                              )}
                            </span>
                          </div>
                        </Button>
                      );
                    })}
                  </div>
                  <div className="mt-2 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-[#8b6a2c]/45 bg-[#17140f] px-3 py-2 text-[10px] sm:text-xs">
                    <div className="flex flex-wrap items-center gap-3">
                      <span className="rounded-full border border-[#8b6a2c]/55 px-2 py-1 text-[9px] font-semibold text-[#e0bd70]">
                        升档建议
                      </span>
                      <span className="font-medium text-foreground">
                        {topupAmount === 6 && newUserSixEligible ? (
                          <>
                            新客体验档实付 ¥6，直接到账{" "}
                            <strong className="text-[#e0bd70]">¥8</strong>
                          </>
                        ) : currentPromotion.bonus > 0 ? (
                          <>
                            当前档位实付 ¥{topupAmount}，到账{" "}
                            <strong className="text-[#e0bd70]">
                              ¥{formatNumber(currentPromotion.credited)}
                            </strong>
                          </>
                        ) : (
                          <>当前金额按实付到账，优惠从 ¥30 档开始</>
                        )}
                      </span>
                    </div>
                    <span className="font-semibold text-[#e0bd70]">
                      {upgradeMessage}
                    </span>
                  </div>
                </div>
              )}

              <div className="space-y-2">
                <Label
                  htmlFor="topup-amount"
                  className="text-muted-foreground text-[10px] font-medium tracking-wider uppercase"
                >
                  {t("Custom Amount")}
                </Label>
                <div className="grid grid-cols-1 gap-2 lg:grid-cols-[minmax(0,0.8fr)_minmax(520px,1.2fr)] lg:items-stretch">
                  <div className="relative">
                    <span className="absolute top-1/2 left-4 -translate-y-1/2 text-sm font-semibold text-[#d6b66c]">
                      ¥
                    </span>
                    <Input
                      id="topup-amount"
                      type="number"
                      value={localAmount}
                      onChange={(e) => handleAmountChange(e.target.value)}
                      min={minTopup}
                      placeholder={`最低充值 ${minTopup} 元`}
                      className="h-full min-h-16 pl-10 text-sm sm:text-base"
                    />
                  </div>
                  <div className="bg-muted/30 grid min-h-16 grid-cols-3 items-center gap-3 rounded-md border px-4 py-2 sm:grid-cols-[repeat(3,minmax(0,1fr))_auto] sm:gap-4">
                    <div>
                      <span className="text-muted-foreground block text-[10px] font-medium sm:text-xs">
                        实付金额
                      </span>
                      {calculating ? (
                        <Skeleton className="h-5 w-16" />
                      ) : (
                        <span className="whitespace-nowrap text-xs font-bold sm:text-sm">
                          ¥{formatCurrency(paymentAmount)}
                        </span>
                      )}
                    </div>
                    <div>
                      <span className="text-muted-foreground block text-[10px] font-medium sm:text-xs">
                        赠送额度
                      </span>
                      <span className="whitespace-nowrap text-base font-bold text-[#e0bd70] sm:text-lg">
                        +¥
                        {formatNumber(currentPromotion.bonus * usdExchangeRate)}
                      </span>
                    </div>
                    <div>
                      <span className="text-muted-foreground block text-[10px] font-medium sm:text-xs">
                        预计到账
                      </span>
                      <span className="whitespace-nowrap text-xs font-bold sm:text-sm">
                        ¥
                        {formatNumber(
                          currentPromotion.credited * usdExchangeRate,
                        )}
                      </span>
                    </div>
                    <Button
                      type="button"
                      disabled={quickTopupDisabled}
                      onClick={() =>
                        defaultPaymentMethod &&
                        onPaymentMethodSelect(defaultPaymentMethod)
                      }
                      className="col-span-3 h-12 w-full min-w-40 border border-[#e0bd70] bg-[#d9ba6f] px-7 text-sm font-bold text-[#17140f] shadow-[0_8px_20px_rgba(217,186,111,0.16)] hover:bg-[#e7c979] sm:col-span-1 sm:w-auto"
                    >
                      {paymentLoading === defaultPaymentMethod?.type ? (
                        <Loader2 className="size-4 animate-spin" />
                      ) : (
                        "立即充值"
                      )}
                    </Button>
                  </div>
                </div>
              </div>

              <div className="space-y-2">
                <Label className="text-muted-foreground text-[10px] font-medium tracking-wider uppercase">
                  {t("Payment Method")}
                </Label>
                {hasStandardPaymentMethods ? (
                  <div className="grid grid-cols-2 gap-1.5 sm:gap-3 lg:grid-cols-3">
                    {topupInfo?.pay_methods?.map((method) => {
                      const minTopup = method.min_topup || 0;
                      const disabled = minTopup > topupAmount;
                      const disabledReason = disabled
                        ? t("Minimum topup amount: {{amount}}", {
                            amount: minTopup,
                          })
                        : undefined;
                      const disabledLabel = disabled
                        ? `${t("Minimum:")} ${minTopup}`
                        : undefined;

                      const button = (
                        <Button
                          key={method.type}
                          variant="outline"
                          onClick={() => onPaymentMethodSelect(method)}
                          disabled={disabled || !!paymentLoading}
                          title={disabledReason}
                          aria-label={
                            disabledReason
                              ? `${method.name}. ${disabledReason}`
                              : method.name
                          }
                          className="min-h-12 min-w-0 justify-start gap-2 rounded-lg px-3 py-2 text-left text-xs"
                        >
                          {paymentLoading === method.type ? (
                            <Loader2 className="h-4 w-4 animate-spin" />
                          ) : (
                            getPaymentIcon(
                              method.type,
                              "h-4 w-4",
                              method.icon,
                              method.name,
                            )
                          )}
                          <span className="flex min-w-0 flex-col items-start gap-0.5">
                            <span className="max-w-full truncate">
                              {method.name}
                            </span>
                            {disabledLabel && (
                              <span className="text-muted-foreground max-w-full truncate text-[11px] leading-4 font-normal">
                                {disabledLabel}
                              </span>
                            )}
                          </span>
                        </Button>
                      );

                      return disabled ? (
                        <TooltipProvider key={method.type}>
                          <Tooltip>
                            <TooltipTrigger render={button} />
                            <TooltipContent>{disabledReason}</TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      ) : (
                        button
                      );
                    })}
                  </div>
                ) : null}
                {!hasStandardPaymentMethods && !hasWaffoPaymentMethods && (
                  <Alert>
                    <AlertDescription>
                      {t(
                        "No payment methods available. Please contact administrator.",
                      )}
                    </AlertDescription>
                  </Alert>
                )}
              </div>

              {enableWaffoTopup &&
                hasWaffoPaymentMethods &&
                onWaffoMethodSelect && (
                  <div className="space-y-2.5 sm:space-y-3">
                    <Label className="text-muted-foreground text-xs font-medium tracking-wider uppercase">
                      {t("Waffo Payment")}
                    </Label>
                    <div className="grid grid-cols-2 gap-1.5 sm:gap-3 lg:grid-cols-3">
                      {waffoPayMethods?.map((method, index) => {
                        const loadingKey = `waffo-${index}`;
                        const methodKey = `${method.payMethodType ?? "unknown"}-${method.payMethodName ?? method.name}`;
                        const waffoMin = waffoMinTopup || 0;
                        const belowMin = waffoMin > topupAmount;
                        const disabledReason = belowMin
                          ? t("Minimum topup amount: {{amount}}", {
                              amount: waffoMin,
                            })
                          : undefined;
                        const disabledLabel = belowMin
                          ? `${t("Minimum:")} ${waffoMin}`
                          : undefined;

                        let methodIcon = getPaymentIcon("waffo");
                        if (paymentLoading === loadingKey) {
                          methodIcon = (
                            <Loader2 className="h-4 w-4 animate-spin" />
                          );
                        } else if (method.icon) {
                          methodIcon = (
                            <img
                              src={method.icon}
                              alt={method.name}
                              className="h-4 w-4 object-contain"
                            />
                          );
                        }

                        const button = (
                          <Button
                            key={methodKey}
                            variant="outline"
                            onClick={() => onWaffoMethodSelect(method, index)}
                            disabled={belowMin || !!paymentLoading}
                            title={disabledReason}
                            aria-label={
                              disabledReason
                                ? `${method.name}. ${disabledReason}`
                                : method.name
                            }
                            className="min-h-14 min-w-0 justify-start gap-2 rounded-lg px-3 py-2 text-left"
                          >
                            {methodIcon}
                            <span className="flex min-w-0 flex-col items-start gap-0.5">
                              <span className="max-w-full truncate">
                                {method.name}
                              </span>
                              {disabledLabel && (
                                <span className="text-muted-foreground max-w-full truncate text-[11px] leading-4 font-normal">
                                  {disabledLabel}
                                </span>
                              )}
                            </span>
                          </Button>
                        );

                        return belowMin ? (
                          <TooltipProvider key={methodKey}>
                            <Tooltip>
                              <TooltipTrigger render={button} />
                              <TooltipContent>{disabledReason}</TooltipContent>
                            </Tooltip>
                          </TooltipProvider>
                        ) : (
                          button
                        );
                      })}
                    </div>
                  </div>
                )}
            </>
          )}
        </div>
      ) : (
        <Alert>
          <AlertDescription>
            {t(
              "Online topup is not enabled. Please use redemption code or contact administrator.",
            )}
          </AlertDescription>
        </Alert>
      )}

      {/* Creem Products Section */}
      {enableCreemTopup &&
        Array.isArray(creemProducts) &&
        creemProducts.length > 0 &&
        onCreemProductSelect && (
          <div className="space-y-2.5 border-t pt-4 sm:space-y-3 sm:pt-6">
            <Label className="text-muted-foreground text-xs font-medium tracking-wider uppercase">
              {t("Creem Payment")}
            </Label>
            <CreemProductsSection
              products={creemProducts}
              onProductSelect={onCreemProductSelect}
            />
          </div>
        )}

      {/* Redemption Code Section */}
      {redemptionEnabled ? (
        <div className="space-y-2.5 border-t pt-4 sm:space-y-3 sm:pt-6">
          <div className="flex items-center gap-2">
            <IconBadge tone="warning" size="xs">
              <Gift />
            </IconBadge>
            <Label
              htmlFor="redemption-code"
              className="text-muted-foreground text-xs font-medium tracking-wider uppercase"
            >
              {t("Have a Code?")}
            </Label>
          </div>
          <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-2">
            <Input
              id="redemption-code"
              value={redemptionCode}
              onChange={(e) => onRedemptionCodeChange(e.target.value)}
              placeholder={t("Enter your redemption code")}
              className="h-9 min-w-0"
            />
            <Button
              onClick={onRedeem}
              disabled={redeeming}
              variant="outline"
              className="h-9 px-4"
            >
              {redeeming && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              {t("Redeem")}
            </Button>
          </div>
          {topupLink && (
            <p className="text-muted-foreground text-xs">
              {t("Need a redemption code?")}{" "}
              <a
                href={topupLink}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 underline-offset-4 hover:underline"
              >
                {t("Get one here")}
                <ExternalLink className="h-3 w-3" />
              </a>
            </p>
          )}
        </div>
      ) : (
        <Alert className="border-t">
          <AlertDescription>
            {t(
              "Redemption codes are disabled until the administrator confirms compliance terms.",
            )}
          </AlertDescription>
        </Alert>
      )}
    </TitledCard>
  );
}
