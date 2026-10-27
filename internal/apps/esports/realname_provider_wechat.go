package esports

import (
	"context"
	"fmt"
)

// 微信实名渠道骨架。
//
// 预留两种核验：二要素（姓名 + 身份证号）与实名认证组件（人脸跳转 / 回调）。
// wechatRealNameReady 在商户资质与报文联调完成前恒为 false —— 渠道永不入注册表，
// 用户端不展示；联调时只需改为按配置判断并补全 Start / Query 报文，业务层零改动。
func wechatRealNameReady() bool { return false }

type wechatRealNameProvider struct {
	baseURL   string
	appID     string
	appSecret string
	notifyURL string
}

func newWechatRealNameProvider(cfg realNameProviderConfig) *wechatRealNameProvider {
	return &wechatRealNameProvider{
		baseURL:   cfg.BaseURL,
		appID:     cfg.AppID,
		appSecret: cfg.AppSecret,
		notifyURL: cfg.NotifyURL,
	}
}

func (*wechatRealNameProvider) Code() string { return realNameChannelWechat }

func (*wechatRealNameProvider) Supports(method string) bool {
	return method == realNameMethodTwoFactor || method == realNameMethodFace
}

func (*wechatRealNameProvider) Start(context.Context, realNameProviderInput) (*realNameProviderResult, error) {
	return nil, fmt.Errorf("%w：微信实名渠道尚未开通", errRealNameProviderUnavailable)
}

func (*wechatRealNameProvider) Query(context.Context, string) (*realNameProviderResult, error) {
	return nil, fmt.Errorf("%w：微信实名渠道尚未开通", errRealNameProviderUnavailable)
}
