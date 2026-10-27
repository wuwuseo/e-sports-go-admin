package esports

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent/esportsrealnameattempt"
	"go-admin/internal/ent/esportsrealnameverification"
	"go-admin/internal/global"
)

// 公开回调处理器：签名正确才落定，重放幂等，坏签名 / 未知流水号 / 未配置渠道分别拒绝。
func TestRealNameCallbackHandler(t *testing.T) {
	db := newRealNameTestDB(t, "esports_realname_callback_handler")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	ctx := t.Context()
	const secret = "callback-secret"
	seedRealNameProviderConfig(t, db, fmt.Sprintf(
		`{"third_party":{"enabled":true,"base_url":"https://verify.example.com","app_id":"studio-esports","app_secret":%q,"notify_url":"https://studio.example.com/api/v1/esports/realname/callback/third_party"}}`,
		secret))

	db.EsportsRealNameVerification.Create().
		SetSubjectType(esportsrealnameverification.SubjectTypeCustomer).SetSubjectID(61).
		SetRealName("钱七").SetIDNumberMasked("110101********1234").
		SetChannel(esportsrealnameverification.ChannelThirdParty).SetMethod(esportsrealnameverification.MethodFace).
		SetStatus(esportsrealnameverification.StatusPending).SetProviderRef("ref-61").SaveX(ctx)
	db.EsportsRealNameAttempt.Create().
		SetSubjectType(esportsrealnameattempt.SubjectTypeCustomer).SetSubjectID(61).
		SetChannel(esportsrealnameattempt.ChannelThirdParty).SetMethod(esportsrealnameattempt.MethodFace).
		SetStatus(esportsrealnameattempt.StatusPending).SetProviderRef("ref-61").SaveX(ctx)

	app := fiber.New()
	app.Post("/realname/callback/:channel", realNameCallbackHandler)

	post := func(channel, body string, signSecret string) (int, string) {
		// 时间戳为 Unix 秒（与收款回调口径一致）；body 原文参与签名。
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		nonce := "n-61"
		// 回调 body 与签名 body 必须是同一串字节；此处固定用传入的可读 JSON 原文。
		signature := realNameHMACHex(signSecret, realNameSignatureCanonical("studio-esports", stamp, nonce, body))
		req := httptest.NewRequest(http.MethodPost, "/realname/callback/"+channel, bytes.NewBufferString(body))
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
		req.Header.Set("X-App-Id", "studio-esports")
		req.Header.Set("X-Timestamp", stamp)
		req.Header.Set("X-Nonce", nonce)
		req.Header.Set("X-Signature", signature)
		resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	body := `{"attempt_ref":"ref-61","passed":true,"message":""}`
	if code, out := post("third_party", body, "wrong-secret"); code != fiber.StatusForbidden {
		t.Fatalf("坏签名应 403，实际 %d: %s", code, out)
	}
	if code, out := post("third_party", body, secret); code != fiber.StatusOK {
		t.Fatalf("正确签名应 200，实际 %d: %s", code, out)
	}
	// 重复回调幂等：不报错、不改变已落定状态。
	if code, out := post("third_party", body, secret); code != fiber.StatusOK {
		t.Fatalf("重复回调应 200，实际 %d: %s", code, out)
	}
	record, err := db.EsportsRealNameVerification.Query().Where(
		esportsrealnameverification.SubjectTypeEQ(esportsrealnameverification.SubjectTypeCustomer),
		esportsrealnameverification.SubjectIDEQ(61),
	).Only(ctx)
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if record.Status != esportsrealnameverification.StatusVerified {
		t.Fatalf("回调通过后档案应 verified，实际 %v", record.Status)
	}
	if record.VerifiedAt == nil {
		t.Fatal("回调通过后应有 verified_at")
	}

	if code, _ := post("third_party", `{"attempt_ref":"unknown","passed":true}`, secret); code != fiber.StatusBadRequest {
		t.Fatalf("未知流水号应 400，实际 %d", code)
	}
	if code, _ := post("wechat", body, secret); code != fiber.StatusServiceUnavailable {
		t.Fatal("未配置渠道应 503")
	}
}

// 列表 / 详情出参只给脱敏证件号，任何字段都不得出现原始证件号。
func TestRealNameRecordViewMasksIDNumber(t *testing.T) {
	db := newRealNameTestDB(t, "esports_realname_view_mask")
	defer db.Close()

	const rawID = "110101199001011234"
	record, err := submitRealName(t.Context(), db, realnameSubjectTypeCustomer, 71, 0, realNameSubmitInput{
		RealName: "孙八", IDNumber: rawID, Channel: realNameChannelManual, Method: realNameMethodManual,
	}, realNameTestNow())
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	row := db.EsportsRealNameVerification.GetX(t.Context(), record.ID)
	payload, err := json.Marshal(realNameRecordView(row, "孙八"))
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if strings.Contains(string(payload), rawID) {
		t.Fatalf("出参泄漏原始证件号: %s", payload)
	}
	if !strings.Contains(string(payload), "110101********1234") {
		t.Fatalf("出参应含脱敏证件号: %s", payload)
	}
}

// 管理端代提交前的主体校验：主体不存在或类型非法一律拒绝。
func TestEnsureRealNameSubjectExists(t *testing.T) {
	db := newRealNameTestDB(t, "esports_realname_subject_exists")
	defer db.Close()
	ctx := t.Context()

	if err := ensureRealNameSubjectExists(ctx, db, realnameSubjectTypePlayer, 999); err == nil {
		t.Fatal("不存在的陪玩应拒绝代提交")
	}
	if err := ensureRealNameSubjectExists(ctx, db, "unknown", 1); err == nil {
		t.Fatal("非法主体类型应拒绝")
	}
	player := db.EsportsPlayer.Create().SetUserID(900).SetDisplayName("测试陪玩").SaveX(ctx)
	if err := ensureRealNameSubjectExists(ctx, db, realnameSubjectTypePlayer, player.ID); err != nil {
		t.Fatalf("存在的陪玩应放行: %v", err)
	}
}