package esports

import (
	"context"
	"fmt"
)

// 支付宝实名渠道骨架。
//
// 预留两种核验：二要素与实名认证（人脸 / 二维码）。
// alipayRealNameReady 在商户资质与报文联调完成前恒为 false —— 渠道永不入注册表，
// 用户端不展示；联调时只需改为按配置判断并补全 Start / Query 报文，业务层零改动。
func alipayRealNameReady() bool { return false }

type alipayRealNameProvider struct {
	baseURL   string
	appID     string
	appSecret string
	notifyURL string
}

func newAlipayRealNameProvider(cfg realNameProviderConfig) *alipayRealNameProvider {
	return &alipayRealNameProvider{
		baseURL:   cfg.BaseURL,
		appID:     cfg.AppID,
		appSecret: cfg.AppSecret,
		notifyURL: cfg.NotifyURL,
	}
}

func (*alipayRealNameProvider) Code() string { return realNameChannelAlipay }

func (*alipayRealNameProvider) Supports(method string) bool {
	return method == realNameMethodTwoFactor || method == realNameMethodFace
}

func (*alipayRealNameProvider) Start(context.Context, realNameProviderInput) (*realNameProviderResult, error) {
	return nil, fmt.Errorf("%w：支付宝实名渠道尚未开通", errRealNameProviderUnavailable)
}

func (*alipayRealNameProvider) Query(context.Context, string) (*realNameProviderResult, error) {
	return nil, fmt.Errorf("%w：支付宝实名渠道尚未开通", errRealNameProviderUnavailable)
}
