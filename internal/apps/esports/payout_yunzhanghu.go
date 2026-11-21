package esports

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportswithdrawalpayout"
	"go-admin/internal/global"
)

// 云账户（灵活用工）代发适配器与打款回调。
//
// ponytail: 待签串口径按云账户公开文档通用规则实现（参数名升序 k=v&k=v，剔除 sign 与空值），
// 请求用商户 RSA 私钥 SHA256WithRSA(PKCS1v15) 签名，响应 / 回调用云账户公钥验签；
// 未与真实商户联调，接口路径与报文外壳按公开文档口径集中在本文件，
// 联调时只需改本文件的常量与 post / VerifyNotification 两处，调用方不受影响。
//
// 密钥只从系统配置读取（PEM 单独成键）：日志与错误信息只输出脱敏后的服务商返回码与摘要。

const (
	// yunzhanghuPayoutPrivateKeyConfigKey / PublicKeyConfigKey 云账户商户 RSA 私钥与云账户公钥 PEM。
	// 2048 位 RSA PEM 超出 SystemConfig.value 单行上限，故单独成键；接口与日志永不回显。
	yunzhanghuPayoutPrivateKeyConfigKey = "app.esports.payout_provider_yunzhanghu_private_key"
	yunzhanghuPayoutPublicKeyConfigKey  = "app.esports.payout_provider_yunzhanghu_public_key"

	yunzhanghuPayoutBatchPath         = "/api/payment/v1/orders"
	yunzhanghuPayoutQueryPath         = "/api/payment/v1/query/order"
	yunzhanghuPayoutTimeout           = 15 * time.Second
	yunzhanghuPayoutMaxResponseBytes  = 1 << 20
	yunzhanghuPayoutNotifySignHeader  = "X-Yzh-Sign"
	yunzhanghuPayoutPaidStatus        = "paid"
	yunzhanghuPayoutFailedStatus      = "failed"
	yunzhanghuPayoutPendingStatus     = "pending"
	yunzhanghuPayoutFailReasonMaxRune = 255
	yunzhanghuPayoutFailCodeMaxLen    = 64
)

var (
	// errPayoutProviderUnknown 代发结果未知（请求失败 / 超时 / 响应读失败 / 验签失败），
	// 不可判为失败，必须按「处理中」重试或查单收敛。
	errPayoutProviderUnknown = errors.New("代发服务商结果未知")
	// errPayoutProviderRejected 代发服务商明确拒绝受理，可按失败处理。
	errPayoutProviderRejected = errors.New("代发服务商明确拒绝受理")
	// errPayoutProviderDisabled 当前打款渠道为人工打款，未启用代发适配器。
	errPayoutProviderDisabled = errors.New("代发服务商未启用")
)

// ── 适配器契约与数据结构 ──

// payoutProvider 代发服务商适配器接口：提交批次、查单、验签通知与应答。
type payoutProvider interface {
	Name() string
	SubmitBatch(ctx context.Context, req payoutBatchRequest) (*payoutBatchResult, error)
	QueryOrder(ctx context.Context, orderNo string) (*payoutOrderResult, error)
	VerifyNotification(header map[string]string, body []byte) (*payoutNotification, error)
}

// payoutBatchItem 批次内一笔打款明细。
type payoutBatchItem struct {
	OrderNo     string
	SignNo      string
	AmountCents int64
	AccountType string
	AccountName string
	Remark      string
}

// payoutBatchRequest 提交批次入参。
type payoutBatchRequest struct {
	BatchNo  string
	DealerID string
	Items    []payoutBatchItem
}

// payoutAcceptedItem 服务商已受理的明细。
type payoutAcceptedItem struct {
	OrderNo string
}

// payoutRejectedItem 服务商明确拒绝的明细。
type payoutRejectedItem struct {
	OrderNo string
	Code    string
	Msg     string
}

// payoutBatchResult 批次受理结果：逐笔区分已受理 / 明确拒绝 / 结果未知。
type payoutBatchResult struct {
	ProviderBatchNo string
	Accepted        []payoutAcceptedItem
	Rejected        []payoutRejectedItem
	Unknown         []string
}

// payoutOrderResult 查单结果：Status 只取 paid / failed / pending。
type payoutOrderResult struct {
	OrderNo  string
	Status   string
	FailCode string
	FailMsg  string
	PaidAt   time.Time
}

// payoutNotification 代发回调结果：Status 只取 paid / failed / pending。
type payoutNotification struct {
	OrderNo  string
	Status   string
	FailCode string
	Msg      string
	PaidAt   time.Time
}

// yunzhanghuProvider 云账户代发适配器。密钥缺失时仍可构造（回调验签前会明确报配置不完整），
// 但 SubmitBatch / QueryOrder / VerifyNotification 一律先做就绪检查，绝不发起请求。
type yunzhanghuProvider struct {
	cfg        yunzhanghuPayoutConfig
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	client     *http.Client
	now        func() time.Time
}

// newYunzhanghuProvider 读取配置并解析密钥；PEM 损坏返回明确中文错误（不回显密钥内容）。
func newYunzhanghuProvider(ctx context.Context, db *ent.Client) (*yunzhanghuProvider, error) {
	p := &yunzhanghuProvider{
		cfg:    loadYunzhanghuPayoutConfig(ctx, db),
		client: &http.Client{Timeout: yunzhanghuPayoutTimeout},
		now:    time.Now,
	}
	if raw := systemConfigValue(ctx, db, yunzhanghuPayoutPrivateKeyConfigKey); raw != "" {
		key, err := parseRSAPrivateKeyPEM(raw)
		if err != nil {
			return nil, fmt.Errorf("%w：云账户商户私钥无法解析", errPayoutProviderDisabled)
		}
		p.privateKey = key
	}
	if raw := systemConfigValue(ctx, db, yunzhanghuPayoutPublicKeyConfigKey); raw != "" {
		key, err := parseRSAPublicKeyPEM(raw)
		if err != nil {
			return nil, fmt.Errorf("%w：云账户公钥无法解析", errPayoutProviderDisabled)
		}
		p.publicKey = key
	}
	return p, nil
}

// newPayoutProvider 按系统配置的打款渠道构造适配器；人工打款返回 errPayoutProviderDisabled。
func newPayoutProvider(ctx context.Context, db *ent.Client) (payoutProvider, error) {
	switch payoutProviderKind(ctx, db) {
	case payoutProviderManual:
		return nil, errPayoutProviderDisabled
	case payoutProviderYunzhanghu:
		return newYunzhanghuProvider(ctx, db)
	default:
		return nil, errPayoutProviderDisabled
	}
}

func (p *yunzhanghuProvider) Name() string { return payoutProviderYunzhanghu }

// ensureReady 配置完整性检查：未启用或字段缺失一律明确失败，绝不发起请求。
func (p *yunzhanghuProvider) ensureReady() error {
	if p == nil || !p.cfg.Enabled ||
		strings.TrimSpace(p.cfg.DealerID) == "" ||
		strings.TrimSpace(p.cfg.AppKey) == "" ||
		strings.TrimSpace(p.cfg.BaseURL) == "" ||
		p.privateKey == nil || p.publicKey == nil {
		return fmt.Errorf("%w：云账户代发未启用或配置不完整", errPayoutProviderDisabled)
	}
	return nil
}

// ── 签名与报文 ──

// yunzhanghuCanonical 云账户签名待签串：参数名 ASCII 升序，剔除 sign / sign_type 与空值，
// 以 key=value 用 & 连接。非标量（嵌套对象）不参与签名，避免序列化差异导致验签漂移。
func yunzhanghuCanonical(fields map[string]any) string {
	keys := make([]string, 0, len(fields))
	for key, value := range fields {
		switch value.(type) {
		case string, json.Number, float64, bool, nil:
		default:
			continue
		}
		switch key {
		case "sign", "sign_type":
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(fmt.Sprint(fields[key]))
		if value == "" {
			continue
		}
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, "&")
}

// yunzhanghuSign 商户私钥签名：待签串复用 yunzhanghuCanonical（参数名升序 k=v&k=v，剔除 sign 与空值）。
func yunzhanghuSign(fields map[string]any, key *rsa.PrivateKey) (string, error) {
	digest := sha256.Sum256([]byte(yunzhanghuCanonical(fields)))
	raw, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// yunzhanghuVerify 云账户公钥验签；失败统一返回 errForbidden，便于回调直接拒绝落账。
func yunzhanghuVerify(fields map[string]any, signature string, key *rsa.PublicKey) error {
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return fmt.Errorf("%w：云账户内容缺少签名", errForbidden)
	}
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("%w：云账户签名不是合法 Base64", errForbidden)
	}
	digest := sha256.Sum256([]byte(yunzhanghuCanonical(fields)))
	if err = rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], raw); err != nil {
		return fmt.Errorf("%w：云账户签名校验失败", errForbidden)
	}
	return nil
}

// yunzhanghuEnvelope 云账户统一报文外壳：data 是被整体签名的 JSON 字符串，
// 保证业务载荷（打款状态、金额）不可被篡改。
type yunzhanghuEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
	Sign    string `json:"sign"`
}

func (e yunzhanghuEnvelope) signedFields() map[string]any {
	return map[string]any{"code": e.Code, "message": e.Message, "data": e.Data}
}

// post 发送已签名请求：网络失败 / 超时 / 响应读失败 / 验签失败一律归为「结果未知」。
// 业务失败码由调用方各自解释（提交批次按 rejected，查单映射为 failed）。
func (p *yunzhanghuProvider) post(ctx context.Context, path string, payload map[string]any) (*yunzhanghuEnvelope, map[string]any, error) {
	fields := map[string]any{}
	for key, value := range payload {
		fields[key] = value
	}
	signature, err := yunzhanghuSign(fields, p.privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("%w：云账户请求签名失败", errPayoutProviderUnknown)
	}
	fields["sign"] = signature
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("%w：云账户请求序列化失败", errPayoutProviderUnknown)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(strings.TrimSpace(p.cfg.BaseURL), "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("%w：云账户请求构造失败", errPayoutProviderUnknown)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w：云账户请求失败 %s", errPayoutProviderUnknown, truncateText(err.Error(), 200))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, yunzhanghuPayoutMaxResponseBytes))
	if err != nil {
		return nil, nil, fmt.Errorf("%w：云账户响应读取失败", errPayoutProviderUnknown)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, fmt.Errorf("%w：云账户接口 HTTP %d %s", errPayoutProviderUnknown,
			resp.StatusCode, truncateText(string(raw), 200))
	}
	var envelope yunzhanghuEnvelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return nil, nil, fmt.Errorf("%w：云账户响应不是合法 JSON", errPayoutProviderUnknown)
	}
	if err = yunzhanghuVerify(envelope.signedFields(), envelope.Sign, p.publicKey); err != nil {
		return nil, nil, fmt.Errorf("%w：云账户响应验签失败", errPayoutProviderUnknown)
	}
	data := map[string]any{}
	if strings.TrimSpace(envelope.Data) != "" {
		if err = json.Unmarshal([]byte(envelope.Data), &data); err != nil {
			return nil, nil, fmt.Errorf("%w：云账户业务载荷不是合法 JSON", errPayoutProviderUnknown)
		}
	}
	return &envelope, data, nil
}

// yunzhanghuSuccessCode 服务商成功码。
func yunzhanghuSuccessCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "0000", "success", "200", "ok":
		return true
	default:
		return false
	}
}

// yunzhanghuUnknownCode 语义为「结果未知」的失败码；其余非成功码按明确拒绝处理。
func yunzhanghuUnknownCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "", "system_error", "unknown", "timeout", "9999":
		return true
	default:
		return false
	}
}

// yunzhanghuStatus 归一化服务商状态：只保留 paid / failed / pending。
func yunzhanghuStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "paid", "success", "succeeded", "successful", "2":
		return yunzhanghuPayoutPaidStatus
	case "failed", "fail", "failure", "error", "-1":
		return yunzhanghuPayoutFailedStatus
	default:
		return yunzhanghuPayoutPendingStatus
	}
}

// yunzhanghuTime 解析服务商时间串；非法 / 空值回落到 fallback。
func yunzhanghuTime(raw string, fallback time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "20060102150405"} {
		if parsed, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return parsed.UTC()
		}
	}
	return fallback
}

// yunzhanghuMapString 从业务载荷读取字符串字段（空值返回空串）。
func yunzhanghuMapString(data map[string]any, key string) string {
	value, _ := data[key].(string)
	return strings.TrimSpace(value)
}

// ── 提交批次 / 查单 ──

// yunzhanghuWireItem 批次明细的线上字段（金额单位为分，与本站口径一致）。
type yunzhanghuWireItem struct {
	OrderNo     string `json:"order_no"`
	SignNo      string `json:"sign_no"`
	AmountCents int64  `json:"amount_cents"`
	AccountType string `json:"account_type"`
	AccountName string `json:"account_name"`
	Remark      string `json:"remark"`
}

// yunzhanghuBatchItemResult 批次响应中的逐笔结果。
type yunzhanghuBatchItemResult struct {
	OrderNo string `json:"order_no"`
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SubmitBatch 提交代发批次：逐笔区分已受理 / 明确拒绝 / 结果未知。
func (p *yunzhanghuProvider) SubmitBatch(ctx context.Context, req payoutBatchRequest) (*payoutBatchResult, error) {
	if err := p.ensureReady(); err != nil {
		return nil, err
	}
	items := make([]yunzhanghuWireItem, 0, len(req.Items))
	for _, item := range req.Items {
		items = append(items, yunzhanghuWireItem{
			OrderNo: item.OrderNo, SignNo: item.SignNo, AmountCents: item.AmountCents,
			AccountType: item.AccountType, AccountName: item.AccountName, Remark: item.Remark,
		})
	}
	payloadItems, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("%w：云账户批次明细序列化失败", errPayoutProviderUnknown)
	}
	dealerID := strings.TrimSpace(req.DealerID)
	if dealerID == "" {
		dealerID = strings.TrimSpace(p.cfg.DealerID)
	}
	envelope, data, err := p.post(ctx, yunzhanghuPayoutBatchPath, map[string]any{
		"dealer_id": dealerID,
		"batch_no":  strings.TrimSpace(req.BatchNo),
		"data":      string(payloadItems),
	})
	if err != nil {
		return nil, err
	}
	if !yunzhanghuSuccessCode(envelope.Code) {
		if yunzhanghuUnknownCode(envelope.Code) {
			return nil, fmt.Errorf("%w：云账户返回未知状态 %s %s", errPayoutProviderUnknown,
				truncateText(envelope.Code, 32), truncateText(envelope.Message, 120))
		}
		return nil, fmt.Errorf("%w：云账户拒绝受理 %s %s", errPayoutProviderRejected,
			truncateText(envelope.Code, 32), truncateText(envelope.Message, 120))
	}
	result := &payoutBatchResult{
		ProviderBatchNo: yunzhanghuMapString(data, "provider_batch_no"),
		Accepted:        make([]payoutAcceptedItem, 0, len(req.Items)),
		Rejected:        make([]payoutRejectedItem, 0),
		Unknown:         make([]string, 0),
	}
	// 逐笔结果的 data 是数组：重新按原始结构解析，避免 map 断言噪音。
	var batchData struct {
		ProviderBatchNo string                      `json:"provider_batch_no"`
		Items           []yunzhanghuBatchItemResult `json:"items"`
	}
	rawData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("%w：云账户批次结果无法解析", errPayoutProviderUnknown)
	}
	if err = json.Unmarshal(rawData, &batchData); err != nil {
		return nil, fmt.Errorf("%w：云账户批次结果无法解析", errPayoutProviderUnknown)
	}
	if batchData.ProviderBatchNo != "" {
		result.ProviderBatchNo = strings.TrimSpace(batchData.ProviderBatchNo)
	}
	for _, item := range batchData.Items {
		orderNo := strings.TrimSpace(item.OrderNo)
		if orderNo == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "accepted", "success", "succeeded", "paid":
			result.Accepted = append(result.Accepted, payoutAcceptedItem{OrderNo: orderNo})
		case "rejected", "failed", "fail", "error":
			result.Rejected = append(result.Rejected, payoutRejectedItem{
				OrderNo: orderNo, Code: truncateText(item.Code, yunzhanghuPayoutFailCodeMaxLen),
				Msg: truncateText(item.Message, 200),
			})
		default:
			result.Unknown = append(result.Unknown, orderNo)
		}
	}
	return result, nil
}

// QueryOrder 查单：明确失败码 / 失败状态 → failed，成功 → paid，其余 → pending。
// 网络 / 验签异常返回 errPayoutProviderUnknown（不可判失败）。
func (p *yunzhanghuProvider) QueryOrder(ctx context.Context, orderNo string) (*payoutOrderResult, error) {
	if err := p.ensureReady(); err != nil {
		return nil, err
	}
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return nil, errInvalidInput
	}
	envelope, data, err := p.post(ctx, yunzhanghuPayoutQueryPath, map[string]any{
		"dealer_id": strings.TrimSpace(p.cfg.DealerID),
		"order_no":  orderNo,
	})
	if err != nil {
		return nil, err
	}
	if !yunzhanghuSuccessCode(envelope.Code) {
		// 明确失败码归 failed；语义为「结果未知」的码归 pending，避免误判失败。
		status := yunzhanghuPayoutFailedStatus
		if yunzhanghuUnknownCode(envelope.Code) {
			status = yunzhanghuPayoutPendingStatus
		}
		return &payoutOrderResult{
			OrderNo: orderNo, Status: status,
			FailCode: truncateText(envelope.Code, yunzhanghuPayoutFailCodeMaxLen),
			FailMsg:  truncateText(envelope.Message, 200),
		}, nil
	}
	result := &payoutOrderResult{
		OrderNo:  orderNo,
		Status:   yunzhanghuStatus(yunzhanghuMapString(data, "status")),
		FailCode: truncateText(yunzhanghuMapString(data, "fail_code"), yunzhanghuPayoutFailCodeMaxLen),
		FailMsg:  truncateText(yunzhanghuMapString(data, "fail_msg"), 200),
	}
	if got := yunzhanghuMapString(data, "order_no"); got != "" {
		result.OrderNo = got
	}
	if result.Status == yunzhanghuPayoutPaidStatus {
		result.PaidAt = yunzhanghuTime(yunzhanghuMapString(data, "paid_at"), p.now().UTC())
	}
	return result, nil
}

// ── 回调验签 / 应答 ──

// verifiedEnvelope 验签云账户回调原始 body（签名可放报文 sign 字段或 X-Yzh-Sign 头），
// 返回报文外壳与业务载荷原文；配置 / 签名 / JSON 问题返回错误，调用方不得修改业务状态。
// 代发回调与签约回调共用，避免两处各写一遍签名校验。
func (p *yunzhanghuProvider) verifiedEnvelope(header map[string]string, body []byte) (*yunzhanghuEnvelope, string, error) {
	if err := p.ensureReady(); err != nil {
		return nil, "", err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, "", errInvalidInput
	}
	var envelope yunzhanghuEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, "", fmt.Errorf("%w：云账户回调不是合法 JSON", errInvalidInput)
	}
	signature := yunzhanghuHeaderValue(header, yunzhanghuPayoutNotifySignHeader)
	if signature == "" {
		signature = envelope.Sign
	}
	if err := yunzhanghuVerify(envelope.signedFields(), signature, p.publicKey); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(envelope.Data) == "" {
		return nil, "", fmt.Errorf("%w：云账户回调缺少业务载荷", errInvalidInput)
	}
	return &envelope, envelope.Data, nil
}

// VerifyNotification 验签代发回调：签名可放报文 sign 字段或 X-Yzh-Sign 头；
// 验签失败返回错误，调用方不得修改任何业务状态。
func (p *yunzhanghuProvider) VerifyNotification(header map[string]string, body []byte) (*payoutNotification, error) {
	envelope, dataRaw, err := p.verifiedEnvelope(header, body)
	if err != nil {
		return nil, err
	}
	var data struct {
		OrderNo  string `json:"order_no"`
		Status   string `json:"status"`
		FailCode string `json:"fail_code"`
		Msg      string `json:"msg"`
		Message  string `json:"message"`
		PaidAt   string `json:"paid_at"`
	}
	if err := json.Unmarshal([]byte(dataRaw), &data); err != nil {
		return nil, fmt.Errorf("%w：云账户回调用载荷不是合法 JSON", errInvalidInput)
	}
	orderNo := strings.TrimSpace(data.OrderNo)
	if orderNo == "" {
		return nil, fmt.Errorf("%w：云账户回调缺少打款单号", errInvalidInput)
	}
	msg := data.Msg
	if strings.TrimSpace(msg) == "" {
		msg = data.Message
	}
	if strings.TrimSpace(msg) == "" {
		msg = envelope.Message
	}
	notice := &payoutNotification{
		OrderNo:  orderNo,
		Status:   yunzhanghuStatus(data.Status),
		FailCode: truncateText(data.FailCode, yunzhanghuPayoutFailCodeMaxLen),
		Msg:      truncateText(msg, 200),
	}
	if notice.Status == yunzhanghuPayoutPaidStatus {
		notice.PaidAt = yunzhanghuTime(data.PaidAt, p.now().UTC())
	}
	return notice, nil
}

// payoutSignNotification 签约结果回调载荷：Status 归一化为 paid（成功）/ failed / pending。
type payoutSignNotification struct {
	PlayerID    int
	AccountType string
	SignNo      string
	AccountName string
	AccountNo   string
	Status      string
}

// yunzhanghuIntValue 读取 JSON 数字或数字串（player_id 两种口径都兼容）。
func yunzhanghuIntValue(value any) int {
	switch v := value.(type) {
	case float64:
		if v < 1 || v != float64(int(v)) {
			return 0
		}
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 1 {
			return 0
		}
		return n
	default:
		return 0
	}
}

// parseYunzhanghuSignNotification 解析签约载荷：JSON 非法返回 nil，字段缺失留零值，
// 由 handler 记日志并应答成功，避免业务错误触发服务商重试。
func parseYunzhanghuSignNotification(data string) *payoutSignNotification {
	var raw struct {
		PlayerID        any    `json:"player_id"`
		AccountType     string `json:"account_type"`
		SignNo          string `json:"sign_no"`
		AccountName     string `json:"account_name"`
		AccountNo       string `json:"account_no"`
		AccountNoMasked string `json:"account_no_masked"`
		Status          string `json:"status"`
	}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil
	}
	accountNo := strings.TrimSpace(raw.AccountNo)
	if accountNo == "" {
		accountNo = strings.TrimSpace(raw.AccountNoMasked)
	}
	return &payoutSignNotification{
		PlayerID:    yunzhanghuIntValue(raw.PlayerID),
		AccountType: strings.TrimSpace(raw.AccountType),
		SignNo:      strings.TrimSpace(raw.SignNo),
		AccountName: strings.TrimSpace(raw.AccountName),
		AccountNo:   accountNo,
		Status:      yunzhanghuStatus(raw.Status),
	}
}

// verifySignNotification 验签签约结果回调（复用 verifiedEnvelope 的原始 body 验签）：
// 签名 / 配置问题返回错误，handler 应答失败且不修改任何业务状态。
func (p *yunzhanghuProvider) verifySignNotification(header map[string]string, body []byte) (*payoutSignNotification, error) {
	_, dataRaw, err := p.verifiedEnvelope(header, body)
	if err != nil {
		return nil, err
	}
	return parseYunzhanghuSignNotification(dataRaw), nil
}

// yunzhanghuHeaderValue 大小写不敏感读取回调签名头。
func yunzhanghuHeaderValue(header map[string]string, key string) string {
	for name, value := range header {
		if strings.EqualFold(name, key) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// yunzhanghuNotifyAck 回调应答：成功 HTTP 200 {"code":"SUCCESS","message":"成功"}；
// 失败 HTTP 400 {"code":"FAIL","message":"..."} 由服务商重试。
// 应答口径按云账户公开文档实现，若联调口径不同只改本函数。
func yunzhanghuNotifyAck(ok bool) (int, string) {
	if ok {
		return http.StatusOK, `{"code":"SUCCESS","message":"成功"}`
	}
	return http.StatusBadRequest, `{"code":"FAIL","message":"回调处理失败"}`
}

// ── 回调落账 ──

// settleYunzhanghuPayoutNotification 按 provider_order_no 幂等落账：
//   - 找不到：返回 ent 未找到错误，由 handler 记日志并应答成功（避免无限重试）；
//   - 已 paid：直接返回，不重复落账；
//   - 成功：同一事务把 payout 置 paid、回填服务商状态与流水号、提现单 approved → paid；
//   - 明确失败：payout 置 pay_failed + fail_code，提现单保持 approved；
//   - pending / 未知：不改任何状态。
func settleYunzhanghuPayoutNotification(ctx context.Context, db *ent.Client,
	notice *payoutNotification) error {
	if notice == nil || strings.TrimSpace(notice.OrderNo) == "" {
		return errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	payout, err := tx.EsportsWithdrawalPayout.Query().Where(
		esportswithdrawalpayout.ProviderEQ(payoutProviderYunzhanghu),
		esportswithdrawalpayout.ProviderOrderNoEQ(notice.OrderNo),
	).Only(ctx)
	if err != nil {
		return err
	}
	switch notice.Status {
	case yunzhanghuPayoutPaidStatus:
		if payout.Status != esportswithdrawalpayout.StatusPayRequested {
			// 已是 paid（重复回调）或 pay_failed（人工重试中）：都不自动改账。
			return nil
		}
		// 回填服务商状态与代发流水号；不 AddVersion，版本号留给落账核心做并发守卫。
		updated, saveErr := tx.EsportsWithdrawalPayout.UpdateOne(payout).
			SetProviderStatus(notice.Status).
			SetTransactionNo(notice.OrderNo).
			SetFailCode("").Save(ctx)
		if saveErr != nil {
			return saveErr
		}
		paidAt := notice.PaidAt
		if paidAt.IsZero() {
			paidAt = time.Now().UTC()
		}
		if err = settleWithdrawalPayoutPaid(ctx, tx, updated, paidAt, 0, ""); err != nil {
			return err
		}
		return tx.Commit()
	case yunzhanghuPayoutFailedStatus:
		if payout.Status != esportswithdrawalpayout.StatusPayRequested {
			return nil
		}
		changed, saveErr := tx.EsportsWithdrawalPayout.Update().Where(
			esportswithdrawalpayout.IDEQ(payout.ID),
			esportswithdrawalpayout.StatusEQ(esportswithdrawalpayout.StatusPayRequested),
		).SetStatus(esportswithdrawalpayout.StatusPayFailed).
			SetProviderStatus(notice.Status).
			SetFailCode(truncateText(notice.FailCode, yunzhanghuPayoutFailCodeMaxLen)).
			SetFailReason(truncateText(notice.Msg, yunzhanghuPayoutFailReasonMaxRune)).
			AddVersion(1).Save(ctx)
		if saveErr != nil {
			return saveErr
		}
		if changed != 1 {
			return nil
		}
		return tx.Commit()
	default:
		// pending / 未知：什么都不改，应答成功避免服务商反复重试。
		return nil
	}
}

// handleYunzhanghuPayoutNotify POST /payment-notify/yunzhanghu
// 验签失败不修改业务状态；找不到打款记录记日志并应答成功；其余按通知结果幂等落账。
func handleYunzhanghuPayoutNotify(c fiber.Ctx) error {
	provider, err := newYunzhanghuProvider(c.Context(), global.ORM)
	if err != nil {
		log.Printf("[esports][payout][yunzhanghu] 回调配置不可用：%v", err)
		return sendYunzhanghuNotifyAck(c, false)
	}
	headers := map[string]string{}
	c.Request().Header.VisitAll(func(key, value []byte) {
		headers[string(key)] = string(value)
	})
	notice, err := provider.VerifyNotification(headers, c.Body())
	if err != nil {
		log.Printf("[esports][payout][yunzhanghu] 回调验签失败：%v", err)
		return sendYunzhanghuNotifyAck(c, false)
	}
	if err = settleYunzhanghuPayoutNotification(c.Context(), global.ORM, notice); err != nil {
		if ent.IsNotFound(err) {
			log.Printf("[esports][payout][yunzhanghu] 回调单号未匹配打款记录 order=%s，应答成功停止重试",
				notice.OrderNo)
			return sendYunzhanghuNotifyAck(c, true)
		}
		log.Printf("[esports][payout][yunzhanghu] 回调落账失败 order=%s status=%s err=%v",
			notice.OrderNo, notice.Status, err)
		return sendYunzhanghuNotifyAck(c, false)
	}
	return sendYunzhanghuNotifyAck(c, true)
}

func sendYunzhanghuNotifyAck(c fiber.Ctx, ok bool) error {
	status, body := yunzhanghuNotifyAck(ok)
	c.Set(fiber.HeaderContentType, "application/json; charset=utf-8")
	return c.Status(status).SendString(body)
}

// handleYunzhanghuSignNotify POST /payment-notify/yunzhanghu/sign
// 签约结果以服务商服务端回调 + 验签为准（前端回跳参数不可信）：
// 验签失败不改任何业务状态；非 success、非法账户类型、player_id 不存在等业务错误
// 一律记日志并应答成功，避免服务商重试风暴。
func handleYunzhanghuSignNotify(c fiber.Ctx) error {
	provider, err := newYunzhanghuProvider(c.Context(), global.ORM)
	if err != nil {
		log.Printf("[esports][payout][yunzhanghu][sign] 回调配置不可用：%v", err)
		return sendYunzhanghuNotifyAck(c, false)
	}
	headers := map[string]string{}
	c.Request().Header.VisitAll(func(key, value []byte) {
		headers[string(key)] = string(value)
	})
	notice, err := provider.verifySignNotification(headers, c.Body())
	if err != nil {
		log.Printf("[esports][payout][yunzhanghu][sign] 回调验签失败：%v", err)
		return sendYunzhanghuNotifyAck(c, false)
	}
	if notice == nil {
		log.Printf("[esports][payout][yunzhanghu][sign] 回调载荷无法解析，应答成功停止重试")
		return sendYunzhanghuNotifyAck(c, true)
	}
	if notice.Status != yunzhanghuPayoutPaidStatus {
		log.Printf("[esports][payout][yunzhanghu][sign] 签约未成功 player=%d type=%s status=%s，不落库",
			notice.PlayerID, notice.AccountType, notice.Status)
		return sendYunzhanghuNotifyAck(c, true)
	}
	if !validPayoutAccountType(notice.AccountType) || notice.PlayerID < 1 || notice.SignNo == "" {
		log.Printf("[esports][payout][yunzhanghu][sign] 签约载荷不合法 player=%d type=%s，应答成功停止重试",
			notice.PlayerID, notice.AccountType)
		return sendYunzhanghuNotifyAck(c, true)
	}
	if err = activatePlayerPayoutAccount(c.Context(), global.ORM, notice.PlayerID,
		notice.AccountType, notice.SignNo, notice.AccountName, notice.AccountNo); err != nil {
		if ent.IsNotFound(err) || errors.Is(err, errInvalidInput) {
			log.Printf("[esports][payout][yunzhanghu][sign] 签约落库被拒 player=%d type=%s err=%v，应答成功停止重试",
				notice.PlayerID, notice.AccountType, err)
			return sendYunzhanghuNotifyAck(c, true)
		}
		log.Printf("[esports][payout][yunzhanghu][sign] 签约落库失败 player=%d type=%s err=%v",
			notice.PlayerID, notice.AccountType, err)
		return sendYunzhanghuNotifyAck(c, false)
	}
	return sendYunzhanghuNotifyAck(c, true)
}
