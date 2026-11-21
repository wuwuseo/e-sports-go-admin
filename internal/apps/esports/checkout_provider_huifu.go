package esports

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 斗拱 V4 聚合支付；同步报文按排序 JSON 验签，异步 resp_data 按原文验签。
// https://paas.huifu.com/docs/lightning/
// https://github.com/huifurepo/dg-payment-skills/blob/main/huifu-pay-integration/references/shared-signing-v2.md
type huifuProvider struct {
	baseURL, merchantNo, sysID, productID, notifyBase string
	skillSource                                       string
	privateKey                                        *rsa.PrivateKey
	publicKey                                         *rsa.PublicKey
	client                                            *http.Client
	now                                               func() time.Time
}

type checkoutClientIPKey struct{}

func checkoutClientIP(ctx context.Context) string {
	value, _ := ctx.Value(checkoutClientIPKey{}).(string)
	return value
}

// 斗拱微信需 JSAPI / 小程序等授权参数，接口没有 W_NATIVE 枚举。
// 当前无 openid 授权链，汇付仅开放已实现的支付宝正扫；收钱吧保留两种方式。
func checkoutProviderSupports(provider, channel string) bool {
	return (provider == checkoutProviderShouqianba && (channel == "wechat" || channel == "alipay")) ||
		(provider == checkoutProviderHuifu && channel == "alipay")
}

func newHuifuProvider(cfg huifuProviderConfig) (*huifuProvider, error) {
	if !cfg.configured() {
		return nil, fmt.Errorf("%w：汇付天下配置不完整（需商户号、sys_id、product_id、密钥与回调地址）", errPaymentGatewayUnavailable)
	}
	privateKey, err := parseRSAPrivateKeyPEM(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("%w：汇付天下商户私钥无法解析", errPaymentGatewayUnavailable)
	}
	publicKey, err := parseRSAPublicKeyPEM(cfg.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w：汇付天下公钥无法解析", errPaymentGatewayUnavailable)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultHuifuBaseURL
	}
	notifyBase := strings.TrimRight(strings.TrimSpace(cfg.NotifyBase), "/")
	for _, address := range []string{baseURL, notifyBase} {
		parsed, parseErr := url.Parse(address)
		if parseErr != nil || parsed.Hostname() == "" || parsed.User != nil ||
			(parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("%w：汇付网关或回调地址不是有效的 HTTP(S) 地址", errPaymentGatewayUnavailable)
		}
	}
	return &huifuProvider{
		baseURL: baseURL, merchantNo: strings.TrimSpace(cfg.MerchantNo),
		sysID: strings.TrimSpace(cfg.SysID), productID: strings.TrimSpace(cfg.ProductID),
		skillSource: strings.TrimSpace(cfg.SkillSource),
		privateKey:  privateKey, publicKey: publicKey, notifyBase: notifyBase,
		client: &http.Client{Timeout: checkoutProviderTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}, now: time.Now,
	}, nil
}

func (p *huifuProvider) Name() string { return checkoutProviderHuifu }

func (p *huifuProvider) notifyURL() string { return p.notifyBase + huifuNotifyPath }

// 兼容控台/官方 SDK 导出的无 PEM 头尾 Base64，以及已有的 PEM 配置。
func huifuKeyDER(raw string) ([]byte, error) {
	if block, _ := pem.Decode([]byte(strings.TrimSpace(raw))); block != nil {
		return block.Bytes, nil
	}
	return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(raw), ""))
}

func parseRSAPrivateKeyPEM(raw string) (*rsa.PrivateKey, error) {
	der, err := huifuKeyDER(raw)
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("私钥不是 RSA")
	}
	return key, nil
}

func parseRSAPublicKeyPEM(raw string) (*rsa.PublicKey, error) {
	der, err := huifuKeyDER(raw)
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKIXPublicKey(der); err == nil {
		if rsaKey, ok := key.(*rsa.PublicKey); ok {
			return rsaKey, nil
		}
		return nil, errors.New("公钥不是 RSA")
	}
	if key, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return key, nil
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	key, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("证书公钥不是 RSA")
	}
	return key, nil
}

func huifuSign(data []byte, key *rsa.PrivateKey) (string, error) {
	digest := sha256.Sum256(data)
	signature, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func huifuVerify(data []byte, signature string, key *rsa.PublicKey) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil || len(raw) == 0 {
		return fmt.Errorf("%w：汇付签名缺失或不是合法 Base64", errForbidden)
	}
	digest := sha256.Sum256(data)
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], raw) != nil {
		return fmt.Errorf("%w：汇付签名校验失败", errForbidden)
	}
	return nil
}

type huifuEnvelope struct {
	RespCode string          `json:"resp_code"`
	Sign     string          `json:"sign"`
	Data     json.RawMessage `json:"data"`
	RespData json.RawMessage `json:"resp_data"`
}

// map 的键由 encoding/json 排序；关闭 HTML 转义，保留中文及 < > & 原字符。
func huifuJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func (p *huifuProvider) parseSignedEnvelope(raw []byte, notification bool) (map[string]any, error) {
	var envelope huifuEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w：汇付响应不是合法 JSON", errInvalidInput)
	}
	data := bytes.TrimSpace(envelope.Data)
	if notification {
		if len(data) != 0 || len(envelope.RespData) == 0 {
			return nil, fmt.Errorf("%w：汇付支付通知必须使用 resp_data", errInvalidInput)
		}
		var text string
		if err := json.Unmarshal(envelope.RespData, &text); err != nil {
			return nil, errInvalidInput
		}
		data = []byte(text)
	} else if len(envelope.RespData) != 0 || len(data) == 0 || data[0] != '{' {
		return nil, fmt.Errorf("%w：汇付同步响应 data 必须为对象", errInvalidInput)
	}
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil || fields == nil || !json.Valid(data) {
		return nil, fmt.Errorf("%w：汇付业务报文不是合法 JSON 对象", errInvalidInput)
	}
	signed := data
	if !notification {
		signed, _ = huifuJSON(fields)
	}
	if err := huifuVerify(signed, envelope.Sign, p.publicKey); err != nil {
		return nil, err
	}
	return fields, nil
}

func (p *huifuProvider) checkMerchant(data map[string]any) error {
	if huifuString(data, "huifu_id") != p.merchantNo {
		return fmt.Errorf("%w：汇付响应商户不匹配", errForbidden)
	}
	for key, expected := range map[string]string{"sys_id": p.sysID, "product_id": p.productID} {
		if value := huifuString(data, key); value != "" && value != expected {
			return fmt.Errorf("%w：汇付响应接入方不匹配", errForbidden)
		}
	}
	return nil
}

func (p *huifuProvider) call(ctx context.Context, path string, data map[string]any) (map[string]any, error) {
	payload, err := huifuJSON(data)
	if err != nil {
		return nil, err
	}
	signature, err := huifuSign(payload, p.privateKey)
	if err != nil {
		return nil, err
	}
	body, err := huifuJSON(map[string]any{"sys_id": p.sysID, "product_id": p.productID, "sign": signature, "data": json.RawMessage(payload)})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, value := range map[string]string{"Content-Type": "application/json", "Accept": "application/json"} {
		req.Header.Set(key, value)
	}
	if p.skillSource != "" {
		req.Header.Set("jpt-x-skill-source", p.skillSource)
		req.Header.Set("jpt-x-skill-huifu_id", p.merchantNo)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w：汇付请求未获得确定结果", errPaymentGatewayUnavailable)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w：汇付响应 HTTP %d", errPaymentGatewayUnavailable, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, checkoutProviderMaxResponseBytes+1))
	if err != nil || len(raw) > checkoutProviderMaxResponseBytes {
		return nil, fmt.Errorf("%w：汇付响应读取失败或超出限制", errPaymentGatewayUnavailable)
	}
	// 未验签的错误、未知返回码和重复交易均不得触发改投，避免重复收款。
	fields, err := p.parseSignedEnvelope(raw, false)
	if err != nil {
		return nil, err
	}
	switch code := huifuString(fields, "resp_code"); code {
	case "00000000", "00000100":
		if err := p.checkMerchant(fields); err != nil {
			return nil, err
		}
		return fields, nil
	case "10000000", "21000000", "22000000", "22000002", "22000003", "22000004", "22000005", "23000003", "23000004", "90000000":
		return nil, fmt.Errorf("%w：汇付业务拒绝（代码 %s）", errCheckoutProviderRejected, truncateText(code, 32))
	default:
		return nil, fmt.Errorf("%w：汇付交易结果待查（代码 %s）", errPaymentGatewayUnavailable, truncateText(code, 32))
	}
}

func huifuString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := data[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func huifuAmountString(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }

// 拒绝负数、溢出及多于两位小数的金额，不通过截断把非法金额变成可入账金额。
func huifuAmountCents(raw string) int64 {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0
	}
	for _, part := range parts {
		if part == "" {
			return 0
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return 0
			}
		}
	}
	yuan, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0
	}
	fen := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) > 2 {
			return 0
		}
		fen, _ = strconv.ParseInt(parts[1]+strings.Repeat("0", 2-len(parts[1])), 10, 64)
	}
	if yuan > ((1<<63-1)-fen)/100 {
		return 0
	}
	return yuan*100 + fen
}

func huifuPayChannel(raw string) string {
	switch strings.ToUpper(raw) {
	case "A_NATIVE", "A_JSAPI", "A_MICROPAY":
		return "alipay"
	case "T_JSAPI", "T_MINIAPP", "T_MICROPAY", "T_NATIVE", "T_APP", "T_H5":
		return "wechat"
	}
	return ""
}

func (p *huifuProvider) CreatePayment(ctx context.Context, req checkoutPaymentRequest) (*checkoutPaymentResult, error) {
	if req.MerchantNo == "" || req.AmountCents <= 0 {
		return nil, errInvalidInput
	}
	// 以下三项是与时间、网络无关的确定性校验：同一请求重试多少次都不会成功，
	// 必须按「服务商明确拒绝」返回，调用方才会把尝试置 failed 并释放有效键；
	// 若误报为 errPaymentGatewayUnavailable，会留下永远不可能成功的 creating 尝试，
	// 在有效期内把客户锁死在这家服务商上。
	if !checkoutProviderSupports(p.Name(), req.PayChannel) {
		return nil, fmt.Errorf("%w：汇付当前仅接入支付宝正扫，微信需先接入授权支付", errCheckoutProviderRejected)
	}
	if net.ParseIP(req.ClientIP) == nil {
		return nil, fmt.Errorf("%w：汇付风控信息缺少有效客户 IP", errCheckoutProviderRejected)
	}
	if !req.ExpiresAt.After(p.now().Add(time.Minute)) {
		return nil, fmt.Errorf("%w：汇付支付有效期需超过 60 秒", errCheckoutProviderRejected)
	}
	requestedAt := req.RequestedAt
	if requestedAt.IsZero() {
		requestedAt = p.now()
	}
	subject := truncateText(req.Subject, 120)
	method, _ := huifuJSON(map[string]string{"merchant_order_no": req.MerchantNo, "subject": subject})
	device, _ := huifuJSON(map[string]string{"device_ip": req.ClientIP})
	data, err := p.call(ctx, "/v4/trade/payment/create", map[string]any{
		"req_seq_id": req.MerchantNo, "req_date": requestedAt.In(cstZone).Format("20060102"),
		"huifu_id": p.merchantNo, "trade_type": "A_NATIVE", "method_expand": string(method),
		"trans_amt": huifuAmountString(req.AmountCents), "goods_desc": subject,
		"terminal_device_data": string(device), "notify_url": p.notifyURL(),
		"time_expire": req.ExpiresAt.In(cstZone).Format("20060102150405"), "remark": "esports",
	})
	if err != nil {
		return nil, err
	}
	if huifuString(data, "req_seq_id") != req.MerchantNo || huifuString(data, "req_date") != requestedAt.In(cstZone).Format("20060102") {
		return nil, fmt.Errorf("%w：汇付响应订单号不匹配", errForbidden)
	}
	qr := huifuString(data, "qr_code")
	if qr == "" {
		return nil, fmt.Errorf("%w：汇付未返回正扫二维码", errPaymentGatewayUnavailable)
	}
	return &checkoutPaymentResult{ProviderTradeNo: huifuString(data, "hf_seq_id"), QRPayload: qr}, nil
}

func (p *huifuProvider) QueryPayment(ctx context.Context, merchantNo string, requestedAt time.Time) (*checkoutNotification, error) {
	if merchantNo == "" || requestedAt.IsZero() {
		return nil, errInvalidInput
	}
	data, err := p.call(ctx, "/v4/trade/payment/scanpay/query", map[string]any{
		"req_seq_id": merchantNo, "req_date": requestedAt.In(cstZone).Format("20060102"),
		"huifu_id": p.merchantNo,
	})
	if err != nil {
		return nil, err
	}
	if huifuString(data, "req_seq_id") != merchantNo || huifuString(data, "req_date") != requestedAt.In(cstZone).Format("20060102") {
		return nil, fmt.Errorf("%w：汇付查单返回的订单号不匹配", errForbidden)
	}
	return huifuNotificationOf(data, p.now()), nil
}

func huifuNotificationOf(data map[string]any, now time.Time) *checkoutNotification {
	status := huifuString(data, "trans_stat")
	notice := &checkoutNotification{
		Provider: checkoutProviderHuifu, RequestID: huifuString(data, "req_seq_id"),
		MerchantNo: huifuString(data, "req_seq_id"), ProviderTradeNo: huifuString(data, "hf_seq_id"),
		PayChannel: huifuPayChannel(huifuString(data, "trade_type", "trans_type")), Status: status,
		Paid: huifuString(data, "resp_code") == "00000000" && status == "S", PaidAt: now,
		AmountCents: huifuAmountCents(huifuString(data, "trans_amt")),
	}
	for _, value := range []string{huifuString(data, "end_time"), huifuString(data, "trans_time")} {
		if len(value) == 6 {
			value = huifuString(data, "req_date") + value
		}
		if parsed, err := time.ParseInLocation("20060102150405", value, cstZone); err == nil {
			notice.PaidAt = parsed
			break
		}
	}
	return notice
}

func (p *huifuProvider) VerifyNotification(_ context.Context, _ map[string]string, body []byte) (*checkoutNotification, error) {
	raw := bytes.TrimSpace(body)
	if !bytes.HasPrefix(raw, []byte("{")) {
		form, err := url.ParseQuery(string(raw))
		if err != nil || len(form["sign"]) != 1 || len(form["resp_data"]) != 1 || len(form["data"]) > 0 {
			return nil, errInvalidInput
		}
		raw, _ = json.Marshal(map[string]string{"sign": form.Get("sign"), "resp_data": form.Get("resp_data")})
	}
	data, err := p.parseSignedEnvelope(raw, true)
	if err != nil {
		return nil, err
	}
	if err := p.checkMerchant(data); err != nil {
		return nil, err
	}
	notice := huifuNotificationOf(data, p.now())
	if notice.RequestID == "" || notice.RequestID != notice.MerchantNo ||
		(notice.Paid && (notice.ProviderTradeNo == "" || notice.AmountCents <= 0)) {
		return nil, errInvalidInput
	}
	return notice, nil
}

func (p *huifuProvider) NotifyAck(ok bool, requestID string) (int, string) {
	if ok && requestID != "" {
		return http.StatusOK, "RECV_ORD_ID_" + requestID
	}
	return http.StatusInternalServerError, "FAIL"
}
