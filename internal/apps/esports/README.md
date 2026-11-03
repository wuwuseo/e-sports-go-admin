# 陪玩报单源码功能集

本功能集通过 `app.go` 注册到 `/api/v1/esports`；管理端页面位于 `go-admin-web/src/apps/esports`。Ent Schema 只放在本目录的 `schema/`，由 `go generate ./internal/apps` 汇入主 Schema，再由 `go generate ./internal/ent` 生成客户端。不要直接修改生成文件。

## 当前状态

本节保留逐阶段开发记录。**2026-10-17 已按当前代码逐条复核并改写早期的「未实现 / 未接入」表述**：已成项的直接标注「已实现」并指向后文对应章节，仅保留真正仍未实现的条项，避免早期清单继续误导。请以当前代码与 `陪玩报单系统-功能列表.md` 的 P0 条项为准；是否可上线仍须按实际通道、端到端流程和权限在业务环境验收，`GET /admin/readiness` 的 ready 只代表运行配置齐备，不代表功能已验收。

功能集默认关闭：模型可迁移，但不播种权限、不挂载路由，也不注册超时任务。**当前生产启用条件**为环境变量 `ESPORTS_EXPERIMENTAL=1`（已不再要求 `CMF_APP_ENV=development`），且必须先把 `app.esports.payment_deadline_minutes` 配成非零值，否则待付款订单无法支付。当前代码仅提供首版交易底座的一部分：商品模块（产品分类维护、固定价 SKU 的创建/编辑/上下架）、客服名下客户、即时待付款报单、按份数原子占用名额、幂等创建、超时释放、客户本人确认报价，以及供**已验签通道适配器**调用的到账裁决。订单操作补齐改价/改份数（先作废旧支付意图并抬升报价版本）、改期、取消（待付款释放名额，已付款转退款申请）与余额核销（转待派单）。新增**加钟（§3.3 / §6.2 / §12.5 首版）**：客服对**服务中**订单发起加钟（追加金额与工时，至少一项为正，同一订单只允许一条待确认），陪玩确认后按**确认时点锁定的分成规则**生成 `kind=addon` 的加钟分成明细，并把追加金额与服务量累加到订单（`addon_cents` / `addon_units`，**不改写原报价快照 `payable_cents`**）；存在待确认加钟时禁止结单。**加钟收款（§3.3 首版）**：确认后的追加金额由客服在订单页按**客户余额**收单（`POST /admin/order-addons/:id/pay-by-balance`，扣减顺序同订单核销、含批次逐笔分摊），加钟单记录已收金额 / 收款人 / 时间，并落 `order.addon_paid` 事件；**加钟退款分摊**已接入——售后退款按比例同时冲回 `kind=service` 与 `kind=addon` 两类分成明细（`加钟退款分摊` 备注）；**加钟已收款并入可退额度**（§12.5-02：可退上界 = 服务应付 + 加钟已收款，加钟已收由只增流水求和派生），冲回分母统一为「结单净服务额 + 加钟已收款」，加钟款经 `after_sale_addon` 流水原路退回；分成与冲回明细另带逐最小服务单位留痕（§15.5-04）。加钟目前只支持客户余额收单，在线支付意图与真实通道仍未接入。钱包模块提供客户余额与陪玩猫粮两类账户，余额不落库、一律由只增流水求和派生，本金与赠送分列；管理员可充值、调整并查看流水（**陪玩猫粮的充值 / 调整仅限超级管理员**，客户余额按归属客服校验归属）。新增年龄资格核验：客服提交本人上传的私有附件、另一名员工独立复核、批准后原子更新客户资格；被核验引用的附件不可通过普通删除接口移除。客户确认要求已验证的 User 绑定和成年资格，保存完整报价版本；未确认、停售、迟到及多付的真实到账不直接进入可派单金额，异常部分记入暂收来源流水。新增**排单模块**（管理端/客服工作台）：订单按 `required_seats` 生成逐条席位，支持指派（校验陪玩资格与在线状态）、撤回、公共池认领、转单、催单与排单看板，邀请到期由系统任务回收并回退订单。并落 §5.9 时间线埋点（首次入池 / 首次派出 / 首次接单 / 开工各记一次、永不改写）。账本与幂等记录由 Ent Hook 禁止更新或删除。新增**陪玩模块**：管理端维护陪玩档案（`user_id`、昵称、资格 `pending/qualified/suspended`、在线状态 `offline/online/protected_abnormal`）；陪玩端自助接口（供 uni-app 客户端）覆盖上下线、我的邀请与接单/拒单、抢单大厅与抢单、我的订单、服务开始/结束（写入服务段，记录陪玩上报服务量与系统点击间隔秒数两个来源）。事务外盒已有条件认领、租约、退避重试和死信逻辑，并已接入实际投递器（站内信 + `notification_push_webhook` 推送，失败退避至 dead 计入客服失败待办）；仍缺 dead 事件的人工重发 / 处置入口。新增**结单（§3.4 首版）**：客服手动结单，事务内校验“服务凭证不少于 1 张、服务时长不为零”后把 `pending_close` 订单条件更新为 `completed` 并留 `closed_at` 与 `order.closed` 事件；新增**结单规则（§3.4 条件自定义）**：可配置「凭证不少于 N 张、服务时长不为零、无未处理加钟 / 售后」与金额复核阈值，按 全局 / 游戏分区 / SKU 绑定、版本化管理（草稿 → 生效 / 回滚，改规则生成新版本）；金额达阈值时订单进入 `pending_close_review` **待复核**，由非负责客服的管理员复核通过（`close-approve` → `completed` 并计提）或驳回（`close-reject` → 回退待结单，不得自审）；无规则回落内置默认（凭证 ≥1、时长 >0、无未处理加钟 / 售后）。结单表单（字段库 + 模板 + 提交规则）与串行审批流节点已实现（见下文「结单表单、提交规则与审批流（§3.4）」）；拖拽式表单设计器、并行 / 条件分支审批、自动结单与客户确认结单仍未实现。新增**售后与退款（§13 首版核心切片）**：售后单覆盖受理 → 处理方案 → 审批 → 执行状态机，审批与执行分人、不得自审；赔付支持“退原核销款”（仅限原订单以客户余额核销的退款，按“先本金后赠送”还原本金 / 赠送构成）与“发放补偿权益”两种，钱包流水与订单退款额度在同一事务内落账；退款额度以订单 `refunded_cents + refund_reserved_cents <= payable_cents` 的条件表达式预占与扣减，退款流水以 `(source_type, source_id, entry_type)` 唯一索引保证幂等。补履约（补钟 / 换人）已实现（见下「售后补履约任务」）。新增**售后补履约任务（§13.6 首版）**：方案动作项新增「补钟 / 换人」参数（承接陪玩、席位、补足服务量、公司承担报酬），提交方案时校验承接陪玩资质与席位范围；审批通过后由执行动作在同一事务内创建补履约任务，**不退款、不冲回分成与成长值、也不改原订单实收与成交额**；任务由客服「开始服务 → 验收」两态流转（`pending → servicing → completed`），一张售后单只创建一个任务（`after_sale_id` 唯一索引兜底）。外部渠道退款、「待客户确认」节点与客户自助端确认入口均已实现（见「关联资源权限与售后客户确认」「已核销收款的渠道撤销 / 拒付」）。新增**分成与结算（§12 首版核心切片）**：分成规则按 `player > game > global` 优先级解析（同层取最高版本，无命中则不分成），结单时在同事务内为订单服务成员生成分成明细、售后退款在执行时按比例冲回负数明细，均以唯一 `idem_key` 保证幂等；结算单按周期聚合待结算明细，锁定后把应结金额写入陪玩猫粮账户流水，明细批量转为已结算。规则灰度、陪玩分成阶梯与团队分成仍未实现；定时自动结算（陪玩分成 + 客服提成各一张结算单）与基础提现已实现（见后文「基础提现」）。新增**礼物（§9 首版核心切片）**：礼物目录（价格档位、礼物自带分成与订单分成独立）、代客送礼（校验身份绑定 / 本人送礼 / 未成年 / 付款来源 / 大额二次确认，事务内原子扣减客户余额本金、生成陪玩礼物收入并计入客户成长值），礼物收入并入既有分成总账一起结算（`kind=gift` 明细与订单 `service` 明细区分），含防刷榜标记与人工复核、退款冲回分成与成长值、陪玩收礼 / 客户贡献日周月榜；外部直付通道与抽奖类礼物仍未实现（原「储值卡本金支付」随储值卡功能于 2026-10-20 整体移除）。新增**考官端（§7 首版核心切片）**：考核类型（入职 / 升级 / 抽查复核 / 复考）与六维度权重可配置（基点合计须 10000，未配置时回落等权）；考核任务状态机 `pending → scheduled → passed/failed`，分配考官后逐维度打分（评语与依据必填），按权重加权总分与全局通过线（系统配置 `app.esports.assessment_pass_score`，默认 60）判定；入职 / 升级 / 复考通过即按目标等级定级并开通接单权限（`qualification=qualified`），抽查不通过降一档并暂停接单（`qualification=suspended`）；陪玩对已出结论的考核可发起申诉（每任务一次），管理员复核成立时改判结论并重放定级动作；客户评价按「订单 + 客户 + 陪玩」唯一保留一份有效评价，修改递增 `version` 并追加只增修订历史，客服代录标记未验证不计入综合评分，综合评分仅纳入已验证且状态正常的评价并含样本数与规则版本（样本不足显示提示），异常刷评可复核为 `disputed/invalid` 剔除。新增**页面装修（§16 首版核心切片）**：运营在后台以结构化组件（纵向模块编排）配置 uni-app 客户自助端首页草稿，支持组件增删 / 排序 / 显隐 / 复制与属性 JSON 编辑；保存草稿走 `draft_version` 乐观锁，发布生成不可变快照并把旧生效版本转历史（一次性版本切换），支持定时发布与「以历史快照重新发布」式回滚，发布前校验组件类型 / 数量上限，客户端走公开接口读已发布配置并在未装修时回落系统默认模板；配套素材库登记受控资源地址。拖拽画布、素材上传裁剪、域名白名单强校验、客户端能力兼容快照未实现。新增**客服端（§8 首版核心切片）**：PC 工作台 / 移动客服端 / 企微侧边栏三个入口共用同一套接口与业务规则（快捷报单复用报单、订单跟进与待办复用订单与售后、业绩看板复用 `cs_performance`、代客送礼复用礼物），本模块新增客户档案扩展（手机号 / 消费偏好 / 客户标签 / 建档来源 / 跟进状态 / 最近联系）与档案汇总（累计充值 / 消费 / 退款 / 送礼、余额本金与赠送分列、成长值、订单数）、只增的客户跟进记录、企微「企业主体 + 外部联系人标识」幂等自动建档、话术库（乐观锁编辑与启停）以及工作台聚合（我的客户数 + 按状态分桶的订单池与公共池摘要 + 待办提醒 + 本人业绩），非超级管理员一律只看归属本人数据。公共池客户 / 金额掩码、客服提成、企微侧边栏 H5 骨架均已实现（侧边栏页见 `e-sports-uniapp/src/pages/wecom/`，接 `GET /wecom/sidebar-config` 下发接入模式，签名走 `POST /admin/wecom/js-config`）；企微「发送到会话」通道、`access_token → jsapi_ticket` 自动拉取（当前只能后台预置 ticket）与个性化陪玩推荐（首页 `player_recommend` 目前取公开 `/players` 列表）仍未实现。**储值卡（§4.4 / §11.3）已于 2026-10-20 由产品决定整体移除**：早期交付的开卡 / 用余额购卡（账户间划转）/ 储值卡核销 / 退卡试算与财务审核 / 套餐 / 到期任务 / 礼物可用卡内本金等能力全部下线，卡表、路由、权限码、菜单与两个定时任务均已删除，详见后文「储值卡功能移除（2026-10-20）」一节；扣减顺序可配置与批次级逐笔扣减保留，只服务客户余额。新增**客服提成（§12.2 首版核心切片）**：与陪玩分成同结构不同主体，支持全局 / 指定客服的提成规则（报单金额 / 订单贡献 / 单量的基数 × 按比例含阶梯 / 每单固定额），结单同事务计提、**结单前退款从基数扣除、结单后部分退款按比例冲回、全额退款冲至零**，并受「陪玩分成 + 客服提成 ≤ 可分基数」上限约束；提成按规则周期生成**独立结算单且不写猫粮钱包**，锁定后由财务打款并留外部凭证；`cs_performance` 报表与客服工作台展示提成列。渠道成本与工作室剩余反算、尾差单列、提成结算单自动生成均已实现（见「客服提成（§12.2）」）。新增**基础提现（§6.3 / §12.6 首版核心切片）**：只有猫粮（已结算收入）可提现，陪玩自助申请时同事务扣减猫粮（`entry_type=withdraw`）避免重复申请超额，支持提现门槛、提现频率、手续费与代扣税费的配置化试算并给出申请金额 / 手续费 / 税费 / 实际到账四个口径，银行卡 / 微信 / 支付宝三种方式；首版一律由财务审核，通过后登记外部打款凭证与完税凭证、到账时间与打款人，驳回则原额退回猫粮；陪玩工作台补可提现余额。提现次数上限与阶梯费率已实现（见「基础提现」）；差异化税率与打款对接真实通道仍未实现。新增**优先派单权（§5.4 / §5.5 首版，业务口径为独占优先通知）**：按游戏分区 + 周期（日 / 周 / 半月 / 月）授予名单，每游戏每期名额可配置，支持人工指定与排名 / 积分 / 达标 / 轮换五种获得方式、切换点前预生成下一期名单、中途人工增减，周期任务自动过期与转生效；订单进入待派单时若该游戏分区有生效名单，则**只通知名单内陪玩**（其他人在抢单大厅看不到该单），名单内**全部拒绝**或**超过配置的通知时长**后订单转 `grabbing` **释放到大厅**给所有人先到先得，客服亦可提前释放；派单候选按窗口可见性过滤（窗口内只列名单内、窗口外列全部合格在线）后再按评分与在途席位均衡排序。备注：此处的「独占」口径已按业务方明确要求实现，并**已回改功能列表文档**——§2.1 / §5.2 / §5.4 / §5.5 / §5.6 / §5.8 / §5.9 / §6.1 均已同步为「独占优先通知」，原文「排序加权、非独占、抢单大厅不受影响」已废弃。同时补齐 §5.6 的独立发布动作：新增**发布到抢单大厅**，把没有优先名单（或窗口已闭合）的已付款待派单**单陪玩**订单直接转 `grabbing`，发布前把被撤回 / 过期的席位重置为 `open`。并支持**客户指定陪玩**（§5.4）：订单 `preferred_player_id` 有值时直接走指派流程、**不进优先通知窗口**，报单时校验陪玩资质 `qualified`。游戏分区权限、技能等级门槛与接单上限已实现（见下文「接单上限」「游戏分区权限 + 技能等级」）；排名 / 积分 / 达标制的自动计算与历史合作、客户偏好等加分项仍未实现；真实推送通道已接 `notification_push_webhook`，未配置时只有站内信 + 陪玩端轮询优先通知列表。新增**陪玩转单与催单（§5.7 首版核心切片）**：陪玩接单后未开始服务时可发起转单申请（带幂等键、同一席位只允许一条待确认申请），**客服确认前席位与订单状态都不变**，确认通过才把席位释放为待补位并按剩余席位把订单退回待接单或待派单（退回待派单时复用 §5.4 的开窗 / 释放逻辑，不会绕开优先通知）；驳回须填原因且保持原状，发起方可在确认前撤回。催单时间阶梯可配置（`app.esports.remind_ladder_minutes`，默认 5/15/30 分钟），`esports_remind_dispatch_ladder` 任务对超时未接单的邀请按档位逐级提醒——第 1 档提醒在线陪玩、第 2 档起升级提醒客服；服务超时提醒由 `app.esports.service_overtime_minutes` 配置阈值，`esports_check_service_overtime` 任务在服务段有效时长超阈值时提醒客服确认是否加钟；两者的提醒都写事务外盒与订单事件，幂等靠事务外盒 `event_key` 唯一索引，任务可安全重复运行。已开始服务的转单由售后补履约（补钟 / 换人）承接、加钟确认流程已实现；企微应用消息与电话 / 语音渠道仍未接入。真实收款已有验签回调 `payment-notify` 与 `payment_gateways` 渠道配置，但商户密钥与回调地址未提供，对外仍以人工核验闭环为主。

启用前仍需完成的是**验收与真实通道**，不再是功能缺失：身份绑定与真实登录、支付意图与服务商适配、预约与派单、服务、售后与退款、对账报表、四类移动角色与企微侧边栏**均已有代码实现**（对应章节见下），仍须业务方按功能规划 A01–A125 做端到端验收与角色权限配置。年龄证据使用现有附件系统，生产前还须复核附件列表及访问地址权限对私有核验文件的隔离。uni-app 客户端已具备 customer / player / cs 三身份会话与订单、钱包、礼物、售后等页面；企微侧边栏缺宿主 JS-SDK 签名，退化为账号登录 + 手动识别。未取得业务签收的支付时限等参数默认 `0`，阻断可支付订单。不得仅将 `featureEnabled` 改为 `true` 作为上线方式。真实的商户收款 / 打款 / 退款密钥与回调地址、企微自建应用凭据仍待业务方提供，当前按人工核验闭环运行。

## 管理端菜单

一个模块一个菜单，均为顶层菜单（当前 19 个）：**商品**（`/esports/products`，产品分类与商品/规格维护）、**订单**（`/esports/orders`，订单列表、详情与改价/改期/取消/余额核销/结单）、**报单**（`/esports/reports`，客服报单、客户、年龄资格复核）、**钱包**（`/esports/wallets`，客户余额与陪玩猫粮账户、充值/调整/流水）、**排单**（`/esports/dispatch`，排单看板、席位指派/撤回、公共池认领、转单、催单、优先派单权名单与独占优先通知、陪玩转单确认）、**陪玩**（`/esports/players`，陪玩档案创建与编辑/上下线）、**售后**（`/esports/aftersales`，售后单受理/方案/审批/执行/退款）、**结算**（`/esports/settlements`，分成规则/分成明细/结算单/客服提成规则/客服提成明细与发放/提现审核/批量代发）、**充值活动**（`/esports/campaigns`，充值活动配置与状态管理，第 9 个顶层菜单，SortOrder 48）、**会员等级**（`/esports/members`，会员等级配置与客户会员查询/手动调级，第 10 个顶层菜单，SortOrder 49）、**礼物**（`/esports/gifts`，礼物目录、代客送礼、单据复核/退款、收礼/贡献榜单，第 11 个顶层菜单，SortOrder 50）、**考核**（`/esports/assessments`，考核权重配置、考核任务打分定级、申诉复核、客户评价与综合评分，第 12 个顶层菜单，SortOrder 51）、**报表**（`/esports/stats`，五类经营统计报表入口，第 13 个顶层菜单，SortOrder 52）、**页面装修**（`/esports/pages`，uni-app 客户自助端首页装修、发布版本与素材库，第 14 个顶层菜单，SortOrder 53）、**客服工作台**（`/esports/cs`，客服端客户档案与跟进、企微自动建档、话术库、订单池与待办聚合，第 15 个顶层菜单，SortOrder 54）、**结单表单**（`/esports/close-forms`，字段库 / 模板 / 提交规则 / 审批流，第 16 个顶层菜单）、**收款管理**（`/esports/payments`，收款渠道与账户、支付回执核销、渠道撤销 / 拒付、上线自检，第 17 个顶层菜单）、**风险客户**（`/esports/risk-customers`，被标记客户列表与风险标记解除，第 18 个顶层菜单）、**实名认证**（`/esports/realnames`，客户 / 陪玩统一实名档案列表、详情、人工审核与代提交，第 19 个顶层菜单）。菜单权限码分别为 `esports:product:menu`、`esports:order:menu`、`esports:report:menu`、`esports:wallet:menu`、`esports:dispatch:menu`、`esports:player:menu`、`esports:aftersale:menu`、`esports:settlement:menu`、`esports:close_form:menu`、`esports:campaign:menu`、`esports:member:menu`、`esports:gift:menu`、`esports:payment:menu`、`esports:assessment:menu`、`esports:stats:menu`、`esports:page:menu`、`esports:cs:menu`、`esports:risk:menu`、`esports:realname:menu`，子页面按钮沿用 `esports:skus:*`、`esports:categories:*`、`esports:orders:*`、`esports:customers:*`、`esports:age_checks:*`、`esports:wallets:*`、`esports:dispatch:*`、`esports:players:*`、`esports:aftersales:*`、`esports:settlements:*`、`esports:campaigns:*`、`esports:members:*`、`esports:gifts:*`、`esports:assessments:*`、`esports:stats:*`、`esports:scripts:*`、`esports:pages:*`、`esports:close_forms:*`、`esports:payments:*`、`esports:risk:*`、`esports:realnames:*`。陪玩端自助接口属独立客户端（uni-app），不挂管理端菜单，其按钮权限统一归到 `esports:players:manage`。

### 商品与报价（§15）

商品采用单层目录。公开接口为 `GET /products`，管理接口为 `GET/POST /admin/products`、`PUT /admin/products/:id`；商品停服使用 `/admin/products/:id/suspensions` 与 `/admin/product-suspensions`。分类接口保持不变。每件商品只有一个价格、服务量、结单规则与库存，不再有规格组、子 SKU 或商品级席位配置。新建商品固定为一人服务；存量订单的席位和服务量快照保留，供履约与历史报表读取。

启用功能集时会幂等播种两条默认游戏分区（商品分类 `GAME_DELTA_FORCE` 三角洲行动 / `GAME_DELTA_FORCE_MOBILE` 三角洲行动手游，均为启用状态）：只补建缺失的 `code`，已有分类（含运营改名、排序、停用）一律保留，重复启动不新增也不覆盖。

迁移时把旧 `esports_skus` 原行和原 ID 改为 `esports_products` 商品行，旧父商品与规格组表删除；订单、名额、确认、停服和售后规则的关联列改为 `product_id`，订单的编号与名称快照列改为 `product_code` / `product_name`。订单 ID 与分成明细的 `order_id` 不重新编号。已有非超管角色的旧商品与停服接口授权迁移到新路径。

`required_level` 创建时默认 `novice`，编辑未传时保持原值。`cost_cents` 编辑时未传表示保持、`null` 表示清空、数值表示覆盖。`GET /admin/products?quote_customer_id=&quote_quantity=` 按商品逐条试算会员折扣、应付额和承诺服务量；单件商品不能报价时标记 `not_orderable`，只有指定 `quote_product_id` 才对该件商品返回明确错误。

**商品编码自动生成（2026-10）**：新建商品时前端不再手填编码，`POST /admin/products` 不传 `code`（或传空串）即由服务端生成 `前缀-全局流水`（如 `SKU-000001`）。前缀与流水位数取系统配置 `app.esports.product_code_prefix`（默认 `SKU`）与 `app.esports.product_code_serial_width`（默认 6，合法 1~12）；流水取同前缀现有编码的最大序号 +1，并发撞唯一索引时最多重试 5 次后报「商品编码自动生成冲突」。显式传 `code` 仍按原样落库（兼容导入与测试）。

### 充值活动与赠送（§4.5）

启用功能集时，`Provision` 会在活动表为空时播种一条“满100送10（示例）”草稿（赠送有效期 30 天、互斥、适用全部客户）。运营确认规则与活动期并手动启用后才会对充值生效；重复启动不会新增或覆盖已有活动。

后台新建充值活动时，`code` 由服务端自动生成，前端无需填写；已有活动的编码仍用于流水快照和关联查询，编辑时不可修改。

充值活动通过 `GET/POST /admin/campaigns`、`PUT /admin/campaigns/:id` 与 `POST /admin/campaigns/:id/status` 维护，落库字段包括：活动类型 `activity_type`（`full_gift` 满赠 / `ladder` 阶梯 / `limited` 限时）、阶梯档位 `tiers`（元素 `{threshold_cents, bonus_cents}`，按 `threshold_cents` **严格递增**，门槛与赠送额均须为正）、叠加规则 `stack_rule`（`stack` 可叠加 / `exclusive` 互斥）、适用客户 `audience`（`all` 全部 / `new_customer` 新客 / `specified` 指定，指定时 `audience_customer_ids` 不得为空）、活动期 `starts_at ~ ends_at`（须早于结束）、赠送有效期 `bonus_valid_days`。其中 `bonus_valid_days = 0` 表示**赠送永久有效**，永不被到期作废。状态流转为 `draft ↔ active ↔ paused`，任意非 `ended` 都可转 `ended`，`ended` 不可回退；`code` 创建后不可改，编辑走 `version` 乐观锁，`code` 重复或版本不一致返回冲突。新建活动状态固定 `draft`。

按活动充值走 `POST /admin/wallets/:id/recharge-campaigns?account_type=...`：`account_type` **必须显式给出**，只接受 `customer_balance`（客户余额，按归属客服校验）与 `player_catfood`（陪玩猫粮，陪玩无归属客服模型，只校验档案存在，与 `POST /admin/wallets/{id}/recharge` 的猫粮口径一致）；缺失或未知取值（如 `player_gold`）一律 400 并点名列出的支持值。活动自身的目标账户类型 `target_account_type` 同口径（留空按客户余额兼容存量），管理端活动视图回显 `target_account_type` 与 `audience_player_ids` 供表单回填。后端按实充金额 `principal_cents` 自动匹配当前生效中活动：阶梯取**最高**命中档，命中多个活动时，只要其中有 `exclusive` 活动就只取赠送额最大的一个互斥活动，否则把各命中活动赠送额求和。赠送写入的是**一条**带活动快照的充值流水（本金 / 赠送分列），快照 `rule_snapshot` 含命中活动列表 `campaigns`（`id/code/name/activity_type/stack_rule/version/matched_threshold_cents/bonus_cents/audience/bonus_valid_days`）、赠送合计 `total_bonus_cents` 与赠送到期 `bonus_expires_at`。

赠送到期作废由系统任务处理，按**只增不改**写入作废流水（不改写原充值流水）；只要命中活动中含 1 个 `bonus_valid_days = 0` 的永久赠送，整笔赠送即视为永久、到期任务不处理。重复幂等键返回冲突。

### 会员等级（§10）

等级配置通过 `GET/POST /admin/member-levels`、`PUT /admin/member-levels/:id`、`POST /admin/member-levels/:id/status` 与 `DELETE /admin/member-levels/:id`（2026-10-19 新增，权限码 `esports:members:level_delete`）维护，按成长值门槛 `growth_threshold` 分层（单位与订单金额一致，即分；`10000` = 累计消费满 100 元）；折扣以基点 `discount_bps` 表示（`10000` = 原价、`9500` = 9.5 折），另有会员订单优先 `priority_dispatch`、储值卡赠送比例 `prepay_bonus_bps`（**2026-10-20 起已废弃**：字段保留只为回显历史值，无任何消费点）、专属客服 `dedicated_cs`（开关）与优先预约 `priority_booking`（开关）。`code` 创建后不可改，编辑走 `version` 乐观锁，`code` 重复或版本不一致返回冲突；状态为 `enabled / disabled`。删除有两条闸门：**启用中的等级拒绝删除**（提示先停用）、**仍被客户 `member_level_code` 或 `member_pending_level_code` 引用拒绝删除**（提示改用停用），两者均为 409 中文冲突；只有「已停用且无人引用」才按 `id + status=disabled` 条件删除并校验影响行数。

客户会员详情走 `GET /admin/customers/:id/member`，返回当前等级、折扣、累计成长值 `growth_total`、待降级等级 `pending_level_code`（空串表示无）与降级到期 `downgrade_after`，以及成长值流水 `ledger`（`source_type` 为 `order` 结单计提 / `after_sale` 售后退款冲回 / `manual` 手动 / `gift` 礼物打赏）与手动调级覆盖记录 `overrides`。成长值来源为**结单计提**（§10.2-01：只计**核销明细的本金**，用赠送权益支付的部分**不计**；流水落 `payment_source` 标明现金 / 预付本金 / 赠送权益 / 混合）、**礼物打赏**（按礼物金额）与**售后退款 / 礼物退款冲回**（按原核销构成**只冲本金份额**）；升级即时生效，降级走可配置保护期（系统配置键 `app.esports.member_downgrade_guard_days`，默认 30 天），到期由定时任务 `esports_apply_member_downgrade` 执行降级。报单优惠顺序为「规格价 → 会员折扣 → 人工调整」。手动调级走 `POST /admin/customers/:id/member/level`（body `{level_code, reason}`），仅留覆盖记录、不改成长值流水。**覆盖期可按次指定（2026-10-19）**：body 另可选 `override_days`（1–3650 天）与 `override_until`（RFC3339 到期时刻）**二选一**，都不传沿用系统配置 `app.esports.member_override_guard_days`；同时给出、越界、到期时刻不晚于当前或超过 3650 天一律 400 并给中文提示（`override_days 与 override_until 只能二选一`、`override_days 须在 1–3650 天之间`、`override_until 必须晚于当前时间`、`override_until 不得超过当前时间 3650 天`、`override_until 须为 RFC3339 时间格式`）。覆盖记录仍是**只增审计记录、`created_at` 记真实调级时刻**，逐次到期时刻作为该次调级的属性随 `member.level_manual` 事件快照（`override_until` / `override_id`）；`override_expires_at` 优先取该快照，缺失时回落配置天数，到期回落与惰性判定口径不变（`applyMemberOverrideExpiries` 改为按覆盖判定逐客户到期，不再用「`created_at` + 配置天数」粗筛）。**覆盖期内的人工等级是权威来源（2026-10 起可升可降）**：重算成长值等级时不再「取最高」，人工设定的等级直接取代成长值口径结果，下一次重算不会把人工降级顶回去；覆盖到期后回落成长值口径——回落有两条腿，**读取会员详情时的惰性判定**（`GET /admin/customers/:id/member`，到期且当前等级仍停在人工设定上就立即重算回落）与可选任务 `esports_expire_member_level_overrides`（每日 00:25，按覆盖天数批量回落，重复执行幂等）。会员详情新增 `override_level_code` / `override_expires_at` / `override_active`，客服可据此判断当前等级是否来自覆盖期内的手动调级。

已实现**排单池会员订单置顶**（§10.4）：`GET /admin/dispatch/board` 的 `pending_dispatch` 与 `public_pool` 会把「当前等级开启 `priority_dispatch`」客户的订单稳定置顶（等级须 `enabled`），并在响应新增字段 `member_priority_order_ids` 中给出这些订单 ID（两池按订单去重）供前端标记；只改展示顺序，**不绕过资质、档期与接单上限**等硬性条件。客户未定级或其等级关闭 `priority_dispatch` 时不置顶、不标记。

已实现**专属通道参与派单排序（§10.4 / 2026-10）**：`dedicated_channel` 权益作为**二级置顶**消费——排序分三档「命中 `priority_dispatch`（一级置顶）→ 命中 `dedicated_channel` 且未命中优先派单（二级置顶）→ 普通订单」，双权益等级只进第一档、桶内保持原有 ID 倒序；看板新增 `member_dedicated_channel_order_ids`（`pending_dispatch` 与 `public_pool` 按订单去重合并）单列这些订单 ID，与 `member_priority_order_ids` 同风格，便于前端并排展示「会员优先」与「专属通道」两个徽标。等级停用或两权益均关闭时顺序与接入前逐位一致。专属通道仍只影响**展示顺序**，不绕过资质、档期与接单上限。

**储值卡赠送比例（§10.4）已于 2026-10-20 随储值卡功能移除**：原「开卡时按 `本金 × prepay_bonus_bps / 10000` 自动算赠送」的链路（未选套餐且未手填赠送额时生效）随开卡流程一并删除。`member_levels.prepay_bonus_bps` 字段与 `member_privilege` 快照里的同名键**保留**，但只回显库中历史值、无任何消费点，schema 注释已标废弃。

已实现**生日礼遇（§10.4）**：客户档案新增 `birthday`（月-日，如 `03-15`，空表示未填），客服工作台待办新增「今日生日客户」——按业务时区（UTC+8）匹配今日生日的归属客户，提醒客服跟进；档案编辑校验 `MM-DD` 格式（月 01-12、日 01-31），非法格式被拒。

已实现**专属客服 / 专属通道（§10.4）**：规则为「会员等级开启 `dedicated_cs` 权益时，客户绑定固定负责客服、不参与轮转」。系统当前已用 `owner_admin_id` 承载归属客服，且轮转尚未实现，因此这条天然满足；`dedicated_cs` 仅作权益开关与展示标识。客户档案 / 工作台 / 企微侧边栏客户卡片（`GET /admin/customers/:id/profile`）新增 `member_level_name`、`dedicated_cs`（是否开启专属客服）与 `owner_admin_name`（负责客服姓名，昵称优先回落用户名），身份标识同时返回 `member_level_code` / `member_level_name` 供前端渲染等级徽标。该档案响应另含 `member_privilege` 快照（`level_code / level_name / active / discount_bps / prepay_bonus_bps（已废弃，仅历史值回显）/ dedicated_cs / dedicated_channel / offline_event_priority / priority_booking / priority_dispatch`，来源 `memberPrivilegeFlags`，等级停用或未定级时 `active=false` 且权益全 false）——权益页、客户档案、下单折扣与派单优先级共用同一份判定数据，不再各自读规则；下单与报价已消费该快照，并在 `order.created` 事件写 `member_dispatch_priority: {dedicated_channel, priority_dispatch}` 留痕。

已实现**专属陪玩预约 / 优先预约（§10.4）**：会员等级新增 `priority_booking` 权益开关；启用该等级的客户在自助端（`POST /user/priority-bookings`）可对「指定陪玩」提交预约意向（陪玩 + 期望时段 + 备注），自助资料 `GET /user/profile` 返回 `priority_booking_enabled` 供前端显隐预约入口。意向落独立轻量实体 `EsportsPriorityBookingIntent`（不含 SKU 与金额，与订单 / 排单 / 报表解耦，客服确认后再走既有报单链路）；客服工作台待办新增「会员优先预约」（按归属客服隔离统计），待办列表 `GET /admin/priority-bookings` 按会员等级置顶（门槛越高越靠前，未定级 / 停用等级排最后）。无权益等级或未定级客户提交返回 403，期望时段须为未来时间。

意向可**主动处理**也可**过期消解（2026-10）**：`POST /admin/priority-bookings/{id}/process`（body `{status: processed|cancelled}`，权限码 `esports:members:priority_booking_process`）以条件更新把 `pending` 转 `processed` / `cancelled` 并记 `processed_by_admin_id` / `processed_at`，重复处理返回冲突、非超管只能处理归属本人客户的意向（403）、超管可处理任意意向；期望时段已过、或未填期望时段且提交超过 30 天仍 `pending` 的意向，在读工作台或跑任务 `esports_expire_priority_booking_intents` 时按 `cancelled` 消解（schema 无意向 `expired` 枚举，故与客户主动取消在数据上不可区分）。工作台待办只统计 `pending`，因此「已处理」与「已过期」都会让待办下降；意向处理暂不写外盒、不通知客户。

调出 P0 → P2（首版不实现，明确不落字段）：生日自动发券、线下活动优先、自动发券等非核心权益。

### 礼物（§9）

礼物目录通过 `GET/POST /admin/gifts`、`PUT /admin/gifts/:id` 与 `POST /admin/gifts/:id/status` 维护。落库字段：`code`（创建后不可改）、名称、图标、价格 `price_cents`、分类、**礼物自带分成**（与订单分成规则相互独立，`commission_mode` 为 `percent`（`commission_bp` 万分比）或 `fixed`（`commission_cents` 每笔固定））、高额档 `high_value`、年龄限制 `require_age`、排序与状态（`draft / on_sale / off_sale`）。编辑走 `version` 乐观锁，`code` 重复或版本不一致返回冲突；新建固定 `draft`。

送礼走 `POST /admin/gift-orders`（代客送礼），body 含 `customer_id / gift_id / player_id / quantity / order_id（仅追溯，可不填）/ confirmed / client_key`。事务内按序校验：客户须已绑定登录身份（`UserID` 非空）、陪玩存在且**不得为客户本人**、客户年龄为成年（未成年人 / 未核验拦截）、礼物为 `on_sale`、金额不溢出；**付款来源首版仅允许客户余额本金**（拒绝赠送权益、无外部直付通道），余额不足返回中文提示。**大额二次确认**：礼物为高额档或金额达到系统配置阈值（键 `app.esports.gift_high_value_threshold_cents`，默认 1000 元）时，`confirmed` 必须为 `true`。**每笔礼物仅一名接收陪玩，且支付前快照收礼方名称**。支付成功时在同一事务内原子扣减余额（`entry_type=consume, source_type=gift`）、按礼物自带分成生成陪玩礼物收入、写入成长值（`source_type=gift`，按礼物金额）并即时重算客户等级；幂等键 `send_gift` 保证重复提交返回既有单据。**余额支付的礼物现在同时记批次账本（2026-10）**：客户余额付款时除钱包消费流水外，另写一条 `entry_role=balance_gift_consume` 的负向批次条目（`source_type=wallet_tx`、`source_id` 取本次礼物钱包流水号，与余额核销订单同口径）并登记批次分摊明细（`consume_source_type=gift` / `consume_source_id=礼物单号`）；外部收款两者都不写，两条路径互斥不重复——修复前该条目恒不可达（分支条件重复），礼物资金批次不可追溯且每笔余额礼物都会被账本核验判成 `wallet_outflow_unbooked`。

礼物收入**并入既有分成总账一起结算**：礼物收入写 `EsportsCommissionEntry`（`kind=gift`、`order_id` 可空、`status=pending_settlement`），`settlePlayerCommission` 按陪玩 + 状态聚合时会自动并入，明细 `kind` 与订单分成（`service`）保持区分。退款走 `POST /admin/gift-orders/:id/refund`（仅 `paid` 且未退款的单据）：回退客户本金，冲回陪玩分成（仅当该笔已生成分成明细时才写 `kind=gift_reversal` 负数明细，避免为尚未释放分成的挂起礼物凭空造出负数）、冲回成长值（`source_id` 加 `:refund` 后缀）。

**榜单**走 `GET /admin/gift-leaderboard?board=player|customer&period=daily|weekly|monthly`，分别给出陪玩收礼榜与客户贡献榜，已全额退款的不计入。**防刷榜**：同一「客户 → 陪玩」在 24 小时内笔数达到 5 笔即标记该笔为 `review_status=flagged` 且**不生成可提现分成**（收入挂起），由 `POST /admin/gift-orders/:id/review`（`approve` 补建分成并置 `approved` / `reject` 置 `rejected`）人工复核。

未实现的 `ponytail` 项：外部直付 / 真实收款通道（当前付款来源为客户余额本金一种）、跨身份「往返送礼」检测（防刷仅按同向笔数启发式）、抽奖类礼物（P2）。后续阶段已落地、不再属于未实现项的有：礼物独立争议 / 退款申请与审核（`/admin/gift-disputes`、`/user/gift-disputes`，支持部分退款）、客服提成计入礼物的开关（`include_gifts`）。（原「储值卡本金送礼」能力已于 2026-10-20 随储值卡功能整体移除，不再列为能力项。）

### 考官端与客户评价（§7）

考核权重通过 `GET/POST /admin/assessment-rules` 维护，按考核类型（`entry` 入职 / `upgrade` 升级 / `spot_check` 抽查复核 / `retry` 复考）分组，覆盖六个维度（`game_skill 游戏技能 / teaching 教学能力 / attitude 服务态度 / communication 沟通能力 / compliance 合规意识 / stability 稳定性`），权重以基点 `weight_bp` 表示。写入为**覆盖式**：须覆盖全部六个维度、每维权重为正且合计**恰为 10000**，否则返回参数错误；未配置任何权重时按等权回落（`10000/6` 取整，余数分给靠前维度，合计仍为 10000）。

考核任务走 `GET/POST /admin/assessments`、`GET /admin/assessments/:id`。新建时 `task_no` 由服务端生成，入职默认目标等级 `novice`，非入职类型必须指定目标等级；状态机为 `pending`（待分配考官）→ `scheduled`（已约考核，`POST /admin/assessments/:id/assign` 写入考官与约定时间）→ `passed / failed`。打分走 `POST /admin/assessments/:id/score`（仅分配到的考官可打分，未分配时任意管理员可打），body 逐维度给出 `{dimension, score(0~100), comment(必填)}`，**须覆盖全部权重为正的维度**；服务端按各维权重快照加权得总分（`Σ score*weight_bp/10000`），与全局通过线（系统配置键 `app.esports.assessment_pass_score`，默认 60）比较判定通过。结论落地：入职 / 升级 / 复考通过时按目标等级写入 `level`、置 `qualification=qualified` 并刷新 `level_appraised_at`；抽查通过仅刷新考核时间，抽查不通过则降一档（`mentor→senior→skilled→novice`）并置 `qualification=suspended` 暂停接单。`GET /admin/assessments/:id` 返回任务 + 逐维度打分 + 申诉记录，构成完整考核记录。

申诉走 `POST /user/player/assessments/:id/appeal`（陪玩端自助，body `{reason}`，仅已出结论的考核可发、每任务仅一次）与 `POST /admin/assessments/:id/appeal/review`（`decision` 为 `approve` 改判并重放定级 / `reject` 维持原结论）。

客户评价：已完成订单的已核验身份客户可走 `POST /user/orders/:id/reviews`（body `{player_id, rating(1~5), comment}`）对**实际服务成员**逐人评价；客服代录走 `POST /admin/player-reviews`（`verified=false`，不计入综合评分）。同「订单 + 客户 + 陪玩」仅保留一份当前有效评价（唯一索引），修改时递增 `version` 并向只增表 `EsportsPlayerReviewRevision` 追加一条修订历史。综合评分走 `GET /admin/players/:id/rating`，仅纳入 `verified=true` 且 `status=normal` 的评价，返回平均分、样本数、是否样本不足（低于系统配置键 `app.esports.review_min_samples`，默认 5）与规则版本（`app.esports.review_rule_version`，默认 `v1`）。异常刷评 / 评价申诉由管理员走 `POST /admin/player-reviews/:id/status` 复核为 `normal / disputed / invalid`，`disputed / invalid` 不计入评分。

未实现的 `ponytail` 项：等级 → 分成率的自动换算（等级与接单权限联动已实现，具体分成率仍按既有「按陪玩」分成规则维护）、按考核类型的差异化通过线（当前统一用 `assessment_pass_score`）、复考的关联与自动触发、申诉多次发起（每任务仅一次）、考核记录与结论的专用导出。

### 经营统计报表（§17）

报表入口走单一路由 `GET /admin/reports/:type`，`:type` 为 `owner_overview`（老板经营总览，订单量/营收/退款/净额 + 按周期分段 + 下钻明细）、`cs_performance`（客服个人业绩，按负责客服 = 业绩归属分组并按净额倒序）、`dispatch_efficiency`（派单效率，席位邀请/接单/拒单/过期计数、平均接单时长，以及按 §5.9 时间线埋点派生的四类平均等待时长）、`aftersale_quality`（售后质量，状态分布、赔付金额与按类型/责任方拆分）、`finance`（财务资金与结算，钱包流水按账户与流水类型汇总、结算单汇总（**排除作废单并单列 `settlement.void`**）、提现单汇总、支付回执按支付方式聚合、分成对账与工作室抽成，响应附 `data_scope=full`）之一，均为**只读聚合**，不新增落库表。

筛选参数：`period`（`daily / weekly / monthly`）、`start` 与 `end`（自定义区间）、`responsible_admin_id`（负责客服 / 售后质量里即「处理客服」）、`created_by_admin_id`（业绩归属客服，即报单客服）、`channel_source`（客户建档渠道快照）、`entry_point`（报单入口快照）、`sku_id` 与 `game`（游戏/商品）、`appointment_type`（即时/预约）、`seat_type`（单人/多人）、`payment_channel`（支付方式）、`kind` 与 `fault_party`（售后类型与责任方）、`account_type` / `entry_type` / `settlement_status`（三个资金维度，仅 `finance` 支持）、`limit`（下钻明细上限）。区间缺省时按 `period` 取当前自然日/周/月；财务总表的独立资金不能按订单渠道归属，因此 `finance` 拒绝 `channel_source` 与 `entry_point`。**未实现或不适用于该报表的维度一律显式 400 并说明**（如 `dispatch_efficiency` 的 `created_by_admin_id`——需求只列负责客服、`aftersale_quality` 的 `created_by_admin_id` / `sku_id` / `game` / `payment_channel` / `appointment_type` / `seat_type`、`finance` 的全部订单归属维度即 `responsible_admin_id` / `created_by_admin_id` / `sku_id` / `game` / `appointment_type` / `seat_type` / `kind` / `fault_party`、以及 `owner_overview` / `cs_performance` 的售后类型 / 责任方与三个资金维度），不再「收下后静默忽略」；未登记的 query key（如缓存穿透参数 `_t`）不做 400。

权限与数据边界（2026-10 统一）：`owner_overview / cs_performance / dispatch_efficiency / aftersale_quality` 对管理员开放，非超级管理员（客服）被收敛到**本人可见订单 / 本人处理的售后**，可见范围的唯一构建点是 `orderVisibleToAdminPredicate`：**本人负责 OR 存在协作授权行 OR（负责客服为空时）本人报单（`created_by_admin_id`）兜底**——因此「负责客服为空」的历史单 / 补录单会回到原报单人名下，不会再对所有客服都不可见；`data_scope` 随响应返回 `owner` / `full` 供前端与审计核对。`finance` 需要超级管理员或命中 `app.esports.finance_report_role_names` 的启用角色（其余 403），且**恒为账户级显式全量域**：钱包流水、结算单、提现单、支付回执、分成对账与工作室抽成全部同一口径，不做订单局部收敛，响应返回 `data_scope=full`。报表聚合与下钻明细同在一次响应内返回，下钻到原单据沿用既有订单/售后详情接口；导出与查看走同一套谓词（`aftersale_quality` 的导出已与页面同源），导出审计快照记录筛选维度与 `data_scope` 对象。

报表口径补充（2026-10）：`owner_overview` 的 `summary.revenue_cents` / `net_cents`、时间轴分段与 `cs_performance.rows[].revenue_cents` 均**已计入已收款加钟**并单列 `addon_revenue_cents`；`finance` 的结算聚合与下钻明细**排除作废单**（`status=void` 不计入 `gross / net`），并新增 `settlement.void`（`count / gross_cents / net_cents`）单列供对账——显式传 `settlement_status=void` 时主口径为空、作废数据只从该块取；三个资金维度真正下推：`account_type` / `entry_type` 作用于钱包流水，`settlement_status` 作用于结算单汇总与下钻明细（提现单状态是另一套枚举，不混用）。

未实现的 `ponytail` 项：报表导出与查看凭证的独立审计表（当前依赖管理端操作日志中间件留痕，与商品编辑同一口径）、财务总表尚不能按订单渠道来源归属独立钱包与结算资金、报表快照表（`ponytail:` 应用层内存聚合，`reportScanLimit=5000`，量级到十万级时改为 SQL 分组聚合）、报表级缓存与定时预聚合。

### 页面装修（§16）

首版仅支持运营人员在后台配置 **uni-app 客户自助端首页**（渠道固定 `uni_customer_home`，陪玩端与客服移动端首页不在装修范围）。装修配置只决定展示位置、样式与筛选规则，**不复制价格、余额等资金数据**，也不得绕过商品上架、陪玩接单资格、未成年人限制、价格与支付等服务端规则。

配置读写走 `GET /admin/pages`（列出渠道文档）、`GET /admin/pages/:channel`（读取工作副本与草稿组件，未装修时回系统默认模板）、`PUT /admin/pages/:channel/draft`（保存草稿，body `{components, expected_version}`，`expected_version` 与 `draft_version` 不符返回冲突，避免多人覆盖）。组件为纵向模块编排（`pageComponent` = `type` + `visible` + 自由 `props`），不开放任意坐标、脚本或自定义 HTML；首版允许类型为 `search / carousel / notice / quick_nav / category / product_list / player_recommend / activity / member_entry / support_entry`，数量上限 20；首页入口组件（客服 / 订单 / 售后）由运营自行取舍，不做必需性校验。

发布走 `POST /admin/pages/:channel/publish`（body `{mode, scheduled_at, note}`）：**提交后一律落 `pending_review` 待审核**，由另一名管理员走 `POST /admin/pages/:channel/publish-review/:versionId/approve`（生效）或 `.../reject`（驳回）后才能上线——`mode=immediate` 审批通过即生成**不可变快照**并把旧生效版本转 `history`，`document.published_version_no` 指向新版本；`mode=scheduled` 要求 `scheduled_at` 晚于当前时间，审批通过后由系统任务 `esports_publish_scheduled_pages` 到点激活。发布前校验草稿配置（组件类型合法、数量上限）；发布状态为 `草稿 draft → 待发布 pending → 待审核 pending_review → 已发布 published → 历史版本 history`，同渠道同一时刻只生效一版，同时只能有一条待审核版本。

回滚走 `POST /admin/pages/:channel/rollback`（body `{version_no}`）：以历史快照为模板**重新发布为新版本**（记录 `rollback_from_version_no`），不原地修改旧记录。发布记录走 `GET /admin/pages/:channel/versions`。素材库走 `GET/POST /admin/page-assets` 与 `DELETE /admin/page-assets/:id`（只有新增与删除，**没有更新接口**）：地址仅允许 `https://`、附件表同款 `public/...` 存储路径或站内相对路径（旧数据兼容 `/uploads/...`），且 **`mime` 与 `size_bytes` 必填**——`size_bytes` 须 > 0 且 ≤ 10 MiB（10485760 字节），`mime` 只允许 `image/jpeg / image/png / image/webp / image/gif`；缺失、超限、非图片类型一律不落库（已删除「未声明 MIME 时按后缀推断」的旁路，`width` / `height` 仍可省略或传 0）。客户端读取走 **公开** `GET /page?channel=uni_customer_home`（AuthPublic），只返回已发布配置与版本号，不含任何个人数据；未发布或读取失败时回系统默认模板（含搜索、快捷导航、商品列表与客服入口）。

未实现的 `ponytail` 项：拖拽画布与任意坐标、素材字节存储 / 上传 / 裁剪（`/admin/page-assets` 只登记受控地址，不做上传）、按客户端能力下发的兼容快照（当前按单一渠道下发同一份配置）、多渠道配置。**已实现、不再属于未实现项**：外部跳转域名白名单强校验（`app.esports.page_allowed_domains`，`url/link/href/jump/jump_url/target_url` 仅允许白名单 HTTPS 域名或站内相对路径）、发布前图片可访问性与商品 / 活动 / SKU 有效性校验（`page_resource_check.go`）、发布「提交 → 异人审核」（`publish-review/:versionId/approve|reject`，见下条）。

**底部导航（tabBar）可编辑（2026-10-19 放开）**：`GET/PUT /admin/pages/:channel/tabbar`（权限码 `esports:pages:tabbar_detail` / `esports:pages:tabbar_update`）不再要求「固定 3 项、路径不可改」——条目数 **1~5 项**，可增删条目、可编辑 `path` 与 `order`（文案 1~12 字，图标仍只接受 `https://` 或站内相对路径）。`path` 必须是站内相对路径且**命中已注册页面白名单** `pageRegisteredRoutes`（与站内跳转链接同一份常量、抄自客户端 `pages.json`；带 query / hash 会被规范化成注册路径后落库），`path` 不得重复；`order` 可选，缺省按提交顺序补 1..N，显式值须在 1~5 且互不重复，服务端按 `order` 升序下发，客户端照数组顺序渲染。旧数据（无 `order` 的标准 3 项）继续通过校验；tabBar 仍存于草稿 / 版本快照 JSON，**不需要数据库迁移**。

**装修渠道收敛为白名单（2026-10）**：只有 `pageAllowedChannels` 里的渠道可装修，首版即 `uni_customer_home`（与 `pageChannelCustomerHome` 同一常量，不另立配置项；新增渠道须同时改常量与集合）。草稿保存、发布、回滚、发布审核、版本列表、tabBar 读写、预览码签发 / 兑换、客户端读取与工作副本读取全部先过同一判定，非白名单渠道返回参数错误并提示当前仅支持 `uni_customer_home`；`GET /admin/pages` 列表按 `ChannelIn(白名单)` 过滤，历史遗留的越权渠道文档不再回显。

**图片资源发布前必须过白名单与可访问探测（2026-10）**：`page_resource_check.go` 先读既有配置 `app.esports.page_allowed_domains`（与外部跳转链接**同一份配置、同一套匹配口径**：小写、去端口、按主机精确匹配），对组件里的图片字段**以及 `images` 轮播数组的每个元素**依次做「协议 → 网络目标 → 域名白名单 → 可访问探测」四步，**任何一步不过都不发请求**；白名单**未配置即 fail-closed**（提示先登记该配置键），因此图片域名必须与跳转域名一起登记进白名单，否则发布 / 回滚被拒。保存草稿只做静态校验（`http://`、带 `user:pass@` 凭证、非 80/443 的显式端口、`localhost` / `*.localhost` / 环回 / 私网 / 链路本地 / 未指定 / 组播 IP 字面量一律即拒），白名单与探测在发布 / 回滚期强制，便于运营「先存草稿再申请审核」。**已知取舍（2026-10-19 更新）**：白名单域名解析结果现在做二次校验——DNS 解析后逐条判 IP，任一条为环回 / 私网 / 链路本地 / 唯一本地 / 未指定 / 组播即整体拒绝（解析失败 fail-closed），真正建连用**同一份**解析结果直接连该 IP（关闭 3xx 跟随），因此 DNS rebinding 已被堵住；探测由串行改为「先收集去重、后 4 并发」，整体 15s 超时，单张仍是 5s。仍存在的取舍：单页图片多于约 12 张且 CDN 慢时可能整体超时失败；探测客户端不走出口代理（`ProxyFromEnvironment` 未启用）。

### 客服端（§8）

客服端不新建端侧逻辑，**PC 工作台 / 移动客服端（uni-app）/ 企业微信侧边栏三个入口共用同一套接口与业务规则**，全部走 `AuthAdmin`（客服即管理员账号）。快捷报单复用既有 `POST /admin/orders`，订单跟进 / 待办复用既有订单与售后接口，业绩看板复用 `GET /admin/reports/cs_performance`，代客送礼复用 `POST /admin/gift-orders`。本模块新增的是**客户档案扩展、跟进记录、话术库与工作台聚合**：

- **客户档案（§8.3）**：在既有 `EsportsCustomer` 上扩展 `phone`（手机号）、`birthday`（生日，`MM-DD`，用于生日礼遇待办 §10.4）、`consumption_preference`（消费偏好）、`tags`（客户标签，JSON 数组，最多 20 个、单个 ≤30 字，写入时去空去重）、`source`（建档来源 `manual / wecom / self / import`）、`follow_status`（跟进状态 `new / following / pending / dormant / closed`）、`last_contact_at`（最近联系时间）。汇总走 `GET /admin/customers/:id/profile`，返回客户基本信息 + 累计充值（余额充值流水本金和）+ 累计消费 / 累计退款（已付款订单实付额与已退额）+ 累计送礼（礼物单据净额）+ 当前余额（本金 / 赠送分列，由流水求和派生）+ 成长值（成长值流水求和）+ 订单数，均为 SQL 聚合。编辑走 `PUT /admin/customers/:id/profile`。
- **跟进记录（§8.3）**：只增表 `EsportsCustomerFollowUp`（`customer_id` / `admin_id` / `channel`（`pc / mobile / wecom / phone / other`）/ `summary` / `next_follow_at`）。走 `GET/POST /admin/customers/:id/follow-ups`；写记录与刷新 `last_contact_at` 在同一事务内完成。记录人（`admin_id`）不随转交改变，离职交接后接手人据此快速了解客户。
- **企微自动建档（§8.4）**：走 `POST /admin/customers/wecom-intake`（body `{corp_id, external_id, display_name, phone}`）。以「企业主体 + 外部联系人标识」组合**幂等**（`(wecom_corp_id, wecom_external_id)` 唯一索引，非企微来源两字段为空 NULL，不参与冲突），已存在直接返回既有档案（`created=false`），并发出并发建档由唯一索引兜底。只填基础字段，不覆盖客服已维护的偏好与标签。**信任边界（2026-10）**：上行 `corp_id` 与系统配置 `app.esports.wecom_corp_id` 比对——已配置且一致才自动建档；已配置但不一致返回 403「企微上行 corp_id（x）与系统配置 app.esports.wecom_corp_id 不一致，拒绝建档」且**不落任何档案**；未配置走既有降级（按手动建档处理）但返回的卡片 `message` 明确提示配置键缺失。**手机号去重（2026-10）**：组合幂等未命中时按同租户手机号查——命中 1 条且归属本人则返回既有档案（`created=false`、`matched_by=phone`、提示「已返回既有客户，未重复建档」）并补齐企微标识，使下一次走组合幂等；命中归属他人只回最小卡片（不泄露档案）；命中多条（历史脏数据）返回 409「该手机号存在 N 条客户档案，请先在客户档案中合并后再建档」，不静默挑一条。改档 `PUT /admin/customers/{id}/profile` 改手机号时同一套校验（保存自身号码不受影响、空号码不参与唯一判定）；`phone` 没有唯一索引，并发建档仍可能产生重复（残余风险，需加索引并清洗存量）。
- **话术库（§8.2）**：表 `EsportsScript`（标题 / 分类 / 内容 / 排序 / 状态），走 `GET/POST /admin/scripts`、`PUT /admin/scripts/:id` 与 `POST /admin/scripts/:id/status`；编辑走 `version` 乐观锁，版本不符返回冲突。三个入口共用。
- **工作台聚合（§8.2）**：走 `GET /admin/cs/workbench`，一次返回我的客户数、我的订单池（沿用 `loadDispatchBoard` 的按状态分桶 + 公共池脱敏摘要）、待办提醒（待付款已超时、待结单、售后待处理、跟进已到期、今日生日客户）与本人业绩（`cs_performance` 口径）。**数据隔离**：非超级管理员一律只看归属本人（归属客服 / 负责订单 / 本人处理的售后）的数据，档案与跟进访问走 `loadOwnedCustomer` 校验归属，越权返回 403。

已实现**公共池脱敏（§5.1 / §8.2）**：排单看板 `GET /admin/dispatch/board` 与客服工作台 `order_pool` 的公共池对非超管一律掩码客户身份与金额字段（`customer_id` 与各 `*_cents` 清零），保留订单号 / 商品 / 席位 / 状态 / 时间等可供认领判断的非敏感字段，并在响应新增 `masked_public_pool_ids` 列出被掩码订单 ID 供前端以「***」展示；超管仍看完整数据。工作台公共池摘要本就只展示订单号与履约状态，无需额外裁剪。

未实现的 `ponytail` 项：企微侧边栏 H5 **骨架已建**（`e-sports-uniapp/src/pages/wecom/`，按 `app.esports.wecom_sidebar_auth_mode` 取 `js_sdk` / `account` 两种模式，无宿主时降级为账号登录 + 手动识别），JS-SDK 签名接口已实现（`POST /admin/wecom/js-config`，sha1(jsapi_ticket=…&noncestr=…&timestamp=…&url=…)），但真实 corpId / agentId / 应用密钥待提供，且 `jsapi_ticket` 目前只能后台预置、`access_token → ticket` 自动拉取未接；话术「发送到会话」的发送通道能力未接入；陪玩推荐（§8.4 P2）的个性化推荐未实现（首页 `player_recommend` 组件当前取公开 `/players` 列表）。（「提成」列已随 §12.2 客服提成落地，见后一节；原待办「储值卡临近到期」随 2026-10-20 储值卡功能移除一并下线。）

### 内置角色与客服权限白名单（§2.1）

首次启动按**显式白名单**播种最小权限角色（`internal/init.go` 的 `minimalRoleSpecs`）：`cs_admin`（客服）、`finance_admin`（财务）、`examiner_admin`（考官）、`operation_editor_admin`（运营编辑）与 `cs_readonly` 等只读角色。与 `admin` 角色不同，这些角色**绝不使用「全部非敏感权限」式通配授权**——`permissionCodes` 就是全部授权来源，逐条列出 `esports:` 权限 code，名单之外的能力一律不授予；code 不存在时只记日志跳过，功能集未启用也能建角色。

`cs_admin` 的白名单已在 2026-10 补齐以下能力（此前缺 code 会让报单 / 代收款链路整段 403）：

- 菜单：报单与排单**各自独立成项**（`esports:report:menu` 与 `esports:dispatch:menu` 互不包含），另含订单、客服工作台、售后、陪玩、礼物、会员菜单。
- 目录与报价读取：`esports:skus:list`、`esports:orders:quote_preview`（报单第一步就要拉 SKU 并预览报价）。
- 代客收款：`esports:orders:pay_balance`、`esports:payments:order_start`、`esports:payments:proof_submit`、`esports:payments:proofs`——只授予「发起与提交」，**凭证核验、收款账户审核、退款审批仍归财务**。
- 档期处置：`esports:orders:appointment_dispose`（超时未确认档期的退还 / 改期）。**只读的可用动作读取是独立权限码** `esports:orders:appointment_disposal_options`（`cmfapp.ValidateApps` 要求权限码全局唯一，不能与 `appointment_dispose` 共码），本白名单**尚未授予**；同理 2026-10-19 新增的 `esports:orders:service_timing`（服务段计时读取）与 `esports:aftersales:confirm`（售后代客确认）也不在白名单内——客服要看到处置按钮 / 计时或使用代客确认，需在角色里单独勾选这三条。
- 礼物：`esports:gift:menu`、`esports:gifts:list / orders / board / intents / intent_create / send / disputes / dispute_create`（档位配置与争议裁定仍归运营 / 超管）。
- 会员：`esports:member:menu`、`esports:members:detail / levels / priority_bookings / player_availability`（调级仍归运营）。

不授予的能力（保持职责分离）：改价审批、退款审批与执行、凭证核验、结算锁定与发放、收款账户审核、页面发布审核、考核规则配置与信用复核。新增客服能力时应继续「按 code 精确追加」，不要把 `esports:*` 或前缀通配写进白名单。

### 客户风控（§13.7）

- **风险标记**：`EsportsCustomer` 新增 `risk_status`（`normal` 正常 / `watch` 疑似滥用复核 / `restricted` 限制自助下单）、`risk_reason`、`risk_marked_at`。管理端「风险客户」独立菜单（`esports:risk:menu`）列出被标记客户（`GET /admin/risk-customers`），管理员可 `POST /admin/customers/:id/risk` 标记 / 解除（标记须填原因；`normal` 会清空原因与标记时间）。
- **只标记复核、不自动处罚**：定时任务 `esports_flag_high_risk_customers` 把高频退款客户标记为 `watch`——口径为窗口内「已完成且 `refund_cents > 0`」的售后单按客户归集次数与退款总额，任一达阈值即标记并写 `customer.risk_flagged` 外盒；已是 `restricted` 的客户**不会被自动降级**，受理与申诉入口照常。阈值配置 `app.esports.risk_window_days`（默认 30）/ `app.esports.risk_refund_count`（默认 3）/ `app.esports.risk_refund_cents`（默认 50000 分）。
- **限制自助下单**：只有 `restricted` 会在自助入口拦截——新增客户自助下单 `POST /api/v1/esports/user/orders`（须已绑定登录身份、成年且未被限制；归属客服作为订单负责客服，幂等键按 `customer:<id>` 隔离），被限制时返回「限制自助下单，请联系客服或先预付」；`watch` 不拦截。
- **赔付一律走系统流程**：系统不提供私下转账入口，退款走售后（§13.3）、由陪玩承担时走追偿单（§13.4）。

未实现的 `ponytail` 项：风险判定只有退款次数与退款金额两个启发式口径（未纳入投诉率、退单率、设备或联系方式关联等维度）；「要求预付」目前只体现在拦截提示语，未接入独立预付流程。

### 储值卡功能移除（2026-10-20）

> **产品决定：储值卡（原 §4.4「储值卡与套餐」/ §11.3「储值卡账户」）整体下线。**这是产品决策、不是缺陷修复，也不存在「回归」。三仓库（`go-admin` / `go-admin-web` / `e-sports-uniapp`）的代码删除均已完成、已提交（`d99e2bf` / `7adcd51` / `fc5851a`）。`app.go`、`init.go`、`cron_seeds.go` 与两个前端仓库的 `src` 全仓 grep `stored_value|storedValue|储值卡|stored-value` 均为 0 命中（仅剩 `schema/*.go` 里「已废弃、禁止新写入」的说明与历史兼容分支）。

**移除的能力（原先都有实现，现全部不可用，接口调用会 404）**

- 开卡（客服代办 / 客户自助）、外部收款开卡与到账核验、开卡合同确认与 `open_request_key` 幂等。
- 用余额购卡的账户间划转（同事务扣本金、加卡本金并写配对流水）。
- 订单储值卡核销（管理端 `POST /admin/orders/:id/pay-by-stored-value-card` 与客户自助同名 `/user` 版本）、订单 `payment_channel=stored_value_card` / `stored_value_card_id` 的新写入，以及结单前「标记 / 关联卡 / 核销流水」一致性校验。
- 退卡全链路：试算（可退 = 未消费本金、未消费赠送作废）、申请、财务审核与打款凭证 `payout_ref`、按批次退款试算。
- 储值卡套餐（本金 + 赠送档位）与「套餐值 > 手填值 > 会员等级比例」的赠送额来源优先级。
- 储值卡一致性异常队列（列表 / 指派 / 处理）。
- 自助购卡（客户端购卡页与核验接口）、客服代客开卡入口与客服工作台「储值卡临近到期」待办。
- 储值卡本金送礼（开卡合同 `allow_gifts` 授权后可用卡内本金送礼；礼物付款来源只剩「客户余额本金」与「外部收款」）。
- 到期与过期：定时任务 `esports_expire_stored_value_cards`（卡到期作废赠送）与 `esports_expire_customer_stored_value_intents`（自助购卡支付意图过期）。
- 管理端「储值卡」菜单整页（卡列表 / 套餐 / 退款 / 支付核验 / 异常队列）、权限码 `esports:stored_value:*`、菜单码 `esports:stored_value:menu`，以及 `cs_admin` 白名单里的 `esports:stored_value:list` / `esports:stored_value:pay_order`。

**订单支付方式只剩两种**：客户余额（`customer_balance`）与外部渠道（微信 / 商户码 / 对公转账）。余额核销管理端 `POST /admin/orders/:id/pay-by-balance`、客户自助 `POST /user/orders/:id/pay-by-balance` 均在，不存在「只剩储值卡支付」的缺口。

**DROP 的表与列**

- 表（下一次迁移执行 DROP，三张）：`{prefix}esports_stored_value_cards`（卡主表）、`{prefix}esports_stored_value_packages`（套餐）、`{prefix}esports_stored_value_exceptions`（异常队列）。对应 schema `EsportsStoredValueCard` / `EsportsStoredValuePackage` / `EsportsStoredValueException` 已从 `schema/` 删除，`migrate/schema.go` 中已无这三张表。
- 列：订单 / 礼物单 / 礼物意图上的 `stored_value_card_id` / `stored_value_card_no` 已从 Ent schema 源删除，**并已重新生成生成产物**——`internal/ent/migrate/schema.go` 与 `internal/ent/runtime/runtime.go` 中均已无这些列与对应校验器（重新生成前曾因陈旧 runtime 导致进程启动 panic，若回退请重跑 `go generate ./internal/ent`，不要手改生成文件）。这些列会随迁移 `DROP COLUMN` 一并丢失；历史行上的卡 ID 在 DROP 后不可回读，如需保留请先随导出清单一起备份。

**刻意保留（不是漏删）**

| 保留项 | 理由 |
| --- | --- |
| `account_type`（钱包账户 / 钱包流水）、`payment_channel`（订单 / 礼物意图）、`payment_source`（礼物单 / 意图的 `stored_value_card_principal`）、`source_type`（售后单 / 售后规则）里的 `stored_value_card*` 枚举值 | 账本流水只增不改，历史行仍持有该值；收缩 ENUM 会让迁移失败或截断历史台账。schema 注释已标「已废弃，禁止新写入」 |
| `member_levels.prepay_bonus_bps`（及 `member_privilege` 快照里的同名键） | 只回显库中历史值，无任何消费点（见「会员等级（§10）」） |
| `wallet.go` 的 `walletStoredValueCard` / `storedValuePrepayAccount` 常量 | 历史流水与批次台账的只读识别 |
| 售后来源中文「储值卡（已停用）」 | 由 `aftersale_sla.go` 的 `afterSaleSourceTypeLabels` 统一输出，历史售后单仍可读，两端不再各维护一份映射 |
| 财务报表 `wallet.stored_value_card` 桶、账本核验跳过历史储值卡账户 | 只读聚合历史流水，避免历史差额被误判成当期异常 |
| 「退原核销款」对历史储值卡核销订单的处置 | 卡表已 DROP，卡一律视为不可用，退款改道客户余额；余额账户不可入账时明确 `execution_failed` 交人工 |

**上线前必做**

1. **先导出存量卡余额，再跑迁移**（卡内本金是真实预付资金）：`cd go-admin && go run ./cmd/esports-stored-value-export`（**只读、不写库**；`--out <路径>` 指定 CSV、`--out -` 走标准输出、`--sample` 只打前 10 条与汇总）。
2. 导出列含卡号 / 客户 / 开卡来源 / 结算渠道 / 状态 / 有效期起止 / 开卡时间 / 本金(分) / 赠送(分) / 已退(分)，以及**由 `esports_wallet_txes`（`account_type=stored_value_card`、`owner_id=卡 ID`）求和派生的台账剩余本金 / 剩余赠送**——台账才是余额真相，卡表字段只是开卡快照；台账查不到对应账户的卡该列留空并在汇总里单列计数，不静默补零。
3. **不要收缩 ENUM**：`stored_value_card` 等值必须留在枚举里（见上表），否则严格模式下 `MODIFY COLUMN` 会失败或截断历史数据。
4. 顺序建议：导出并核对总额 → 确认无新写入 → 迁移 DROP → 再发布后端 / 管理端 / 客户端产物（旧客户端调旧接口只会 404，不会再写入）。

**历史数据影响**

- 存量卡余额与流水以导出的 CSV 为准对账；系统内只保留历史钱包流水与批次台账（`account_type=stored_value_card` / `stored_value_prepay`），不再有可查询的「卡」对象。
- 历史储值卡核销订单的「退原核销款」改退客户余额，退款流水备注为「售后退款（原储值卡已下线，退回客户余额）」，本金 / 赠送按原核销构成拆分，退款总额仍与订单 `refunded_cents` 一致。
- 历史售后单 / 礼物单 / 订单 / 流水上的 `stored_value_card*` 值只作只读识别：售后来源显示「储值卡（已停用）」，财务报表该桶里仍有历史数据，但不再作为可选项或可写入值。
- 会员等级编辑表单不再提供「储值卡赠送比例」，历史值仅在只读接口回显；礼物付款来源只剩「客户余额本金 / 外部收款」两条。

**装修快捷入口的存量清理（`target=prepay_card`）**

储值卡下线后，存量装修文档（草稿与已发布版本）里可能仍留着指向它的快捷入口。处理口径：

- **写时拒绝**：保存草稿与发布前，`quick_nav` 条目的 `target` 命中已下线集合（`prepay_card` / `stored_value_card` / `stored-value`）会返回参数错误，并提示改用 `category` / `orders` / `member` / `support`（见 `page.go` 的 `pageRemovedNavTargets`、`pageNavTargetHint` 与 `validatePageEnumValue`）。
- **读时剔除**：客户端读取（`GET /page`）与预览码预览（`GET /page/preview/:code`）统一走 `normalizePageConfigForClient`——先剔除指向已下线功能的入口，再过滤过期组件；被剔空的 `quick_nav` 整块不下发。已发布版本是**不可变快照**，不回写历史数据，所以只能在读取侧兜底。
- **排查存量影响面**（只读 SQL，`{prefix}` 换成实际表前缀）：
  ```sql
  SELECT id, channel FROM {prefix}esports_page_documents WHERE draft_config LIKE '%prepay_card%';
  SELECT id, document_id, version_no FROM {prefix}esports_page_versions WHERE config LIKE '%prepay_card%';
  ```
  草稿命中：在「页面装修」里把该入口改成可用 target 后保存（保存会被强制校验）；已发布命中：**不必改历史版本**，重新发布一次即写出干净配置。

### 预付批次分摊与扣减顺序（§4.3 / §11.3）

已实现**批次级逐笔扣减（§4.3 / §11.3）**：新增只增表 `EsportsLedgerAllocation`，记录「每笔消费（`consume_source_*`）动用了哪些正向批次（`batch_source_*`）各多少金额」。充值写入 `recharge_batch` 正向批次，余额消费按批次 FIFO 分摊并落明细；`GET /admin/ledger-allocations?account_type=&account_id=` 查询分摊明细（历史储值卡账户的批次仍按 `account_type=stored_value_prepay` 只读可查，见上节）。

已实现**扣减顺序可配置（§4.5）**：系统配置 `app.esports.prepay_deduction_order` 决定本金 / 赠送的拆分口径——`bonus_first`（默认，先赠送后本金）/ `principal_first`（先本金后赠送）/ `proportional`（按本金 / 赠送剩余额比例分摊，本金向下取整、尾差归赠送）；缺失或非法值回落 `bonus_first`。余额核销与增值服务落账共用同一拆分函数，拆分之和恒等于应付额。

未实现的 `ponytail` 项：扣减顺序的「按比例」目前只作用于**本金 / 赠送拆分**，批次分摊仍按批次 FIFO、不做「按比例」分摊；充值 / 退款的财务收款回执（`EsportsPaymentReceipt`）接线仍以 `payment_ref` 与批次账本留痕为准。（原「到期作废赠送」「退卡待审」「礼物可用储值卡本金」三条已随 2026-10-20 储值卡功能移除而作废，见上节。）

### 客服提成（§12.2）

客服提成与陪玩分成**同结构、不同主体**：`EsportsCsCommissionRule`（提成规则）+ `EsportsCsCommissionEntry`（提成明细），规则同样不可原地修改，改规则＝新增递增 version 的新行、旧行转 history（§12.4）。挂在既有「结算」菜单下，不新增顶层菜单。

- **规则（§12.2）**：`GET/POST /admin/cs-commission-rules`、`POST /admin/cs-commission-rules/:id/activate`、`POST /admin/cs-commission-rules/:id/rollback`。适用范围 `global / admin`（admin 层优先于 global，同层取最高版本）；提成基数 `order_amount`（扣除结单前退款后的报单金额）/ `order_contribution`（再扣陪玩分成后的可分余额）/ `order_count`（每单 1）；计费 `percent`（万分比，可叠加阶梯，周期累计净基数分档）或 `fixed_per_order`（每单固定额）；提成周期 `daily / weekly / monthly` 决定阶梯累计窗口。单量基数 + 按比例会在建规则时直接拒绝（恒零提成）。
- **计提（§12.2）**：结单时在 `closeOrder` 同一事务内、陪玩分成之后计提；**提成只在订单完成后生成**，未开工取消单不会走到结单；**结单前已发生的退款从最终计提基数扣除**；`ResponsibleAdminID` 为空（无业绩归属客服）不计提。上限：陪玩分成 + 客服提成不得超过可分基数（含已收款加钟），超出则截断并在备注注明（§12.1）；渠道成本已实现（见下方「渠道成本与工作室剩余反算」）；加钟自身的分成另受「单笔加钟分成 ≤ 加钟本金」约束（见「加钟分成上限与营收口径」）。
- **阶梯（§12.2）**：按「提成周期累计净基数（含本单）」匹配最高档；累计口径取该客服本周期内已计提明细的基数之和（含负数冲回）。
- **退款冲回（§12.2）**：结单后部分退款按 `提成额 × 退款额 ÷ 应付额` 冲回，**全额退款把未冲回的余额一次冲至零**；新增 `kind=reversal` 负数明细，退款金额落在退款当期（§12.6）；幂等靠 `idem_key` 唯一索引兜底。
- **结算与发放（§12.2 / §12.6）**：`POST /admin/cs-settlements` 生成**独立客服提成结算单**（`EsportsSettlement.owner_type=cs`，按周期汇总待结算明细与负向冲回），**不写猫粮钱包**——客服提成发放是财务打款：`POST /admin/settlements/:id/lock` 锁定后由 `POST /admin/settlements/:id/pay` 登记发放并留外部打款凭证（`payout_ref` / `paid_by_admin_id` / `paid_at`，仅 `paid` 时非空）。
- **明细与概览（§12.7）**：`GET /admin/cs-commission-entries` 查每笔订单的提成方式与数值、基数、提成金额与状态（含渠道成本快照 `channel_cost_cents` 与工作室剩余快照 `studio_remainder_cents`）；`GET /admin/cs-commission/summary` 返回待结算净额、已结算合计与本月累计（不传 `admin_id` 看本人）。
- **渠道成本与工作室剩余反算（§12.1）**：订单新增 `channel_cost_cents`（登记接口 `PUT /admin/orders/:id/channel-cost`，非负且不超过应付，结单后不可改）；结单计提客服提成时**可分基数 = 服务应付 − 陪玩分成 − 渠道成本**，并在 service 明细快照渠道成本与**工作室剩余 = 可分基数 − 客服提成**（百分比提成的舍入尾差自然落入工作室剩余，等价尾差单列）。
- **业绩看板提成列（§8.2 / §17）**：`cs_performance` 报表新增 `commission_cents`（区间内该客服提成明细净额），客服工作台与「报表 → 客服业绩」均展示。

已实现**自动生成结算单（§12.2）**：定时任务 `esports_generate_cs_settlements` 为各客服按**上一完整周期**自动生成草稿结算单——周期取该客服生效规则的 `daily / weekly / monthly`（`csCommissionPreviousPeriodWindow` 由当前周期起点回退一个周期算出），只处理「上一周期内有待结算明细」的客服，同客服同周期已存在结算单则跳过（幂等）；**不触碰进行中的当前周期**，锁定与登记发放仍由财务操作。**草稿只把明细挂到结算单（写 `settlement_id`），明细状态仍为待结算；直到财务登记发放成功才转 `settled`**，避免草稿未锁定即永久漏发。已作废（`status=void`）的同周期结算单不算「已生成」，下一轮任务会重新出草稿；作废与重开走 `POST /admin/cs-settlements/:id/void`、`POST /admin/cs-settlements/reopen`。**争议冻结中的提成明细不进入结算单**，解冻后才可汇总。

**结算单状态与列表口径（2026-10）**：

- **状态机**：`draft（草稿）→ locked（锁定）→ paid（已登记发放）`，另有作废终态 `void`。结算单新增 `version` 乐观锁——锁定、登记发放、作废都按读取到的 `version` 做条件更新并抬版本（`settle v1 → lock v2 → pay/void v3`），并发或重复操作返回冲突；作废留痕为 `voided_by_admin_id` / `voided_at` / `void_reason`，已发放单（`paid` 或已有 `paid_at` / `payout_ref`）不能作废，须走财务冲正。
- **明细置 `settled` 的时点**：只有 `POST /admin/settlements/{id}/pay` 登记发放成功后，该单绑定的 `pending_settlement` 明细才转 `settled`，并校验「`settled` 条数 == 结算单 `entry_count`」，不一致整单回滚；**锁定不置 settled**。作废把明细回退为 `pending_settlement`、清空 `settlement_id`、追加「作废回退：原结算单 …」备注并留 `settlement.voided` 外盒；重开（`POST /admin/cs-settlements/reopen`，body `admin_id / period_start / period_end / reason`，时间为 RFC3339）按同客服同周期重新出**草稿**，仍需锁定 + 登记发放，因此「作废 + 重开」不会重复发钱。
- **列表过滤**：`GET /admin/settlements` 支持 `?status=`——不传默认只返回**未作废**单（日常发放视图）、`draft|locked|paid` 只看该状态、`void` 只看已作废单（审计与重开追溯）、`all` 全部；其它取值返回 400。客服提成单没有独立列表路由，仍在 `GET /admin/settlements` 里按 `owner_type` 混排。
- **结算单详情 `entries` 按 `owner_type` 分流（2026-10-19）**：`GET /admin/settlements/{id}` 不再只查陪玩分成明细——`owner_type=player` 仍返回 `commissionEntryView`；`owner_type=cs` 改查客服提成明细并返回 `csSettlementEntryView`（`admin_id` 替代 `player_id`，`kind` 为 `service / reversal / gift / gift_reversal`，服务 / 冲回明细回填 `order_no`、礼物明细回填礼物单号）。`entry_count` / `detail_truncated` 与陪玩侧同口径（`limit + 1` 探测截断），响应壳不变（`{settlement, entries, entry_count, detail_truncated}`，`entries` 元素形状随 `owner_type` 变化）。
- **争议冻结**：提成明细新增 `frozen_at` / `frozen_reason` / `frozen_by_admin_id`（与状态正交，仍为 `pending_settlement`），冻结要求填原因、只允许「待结算 + 未挂单 + 未冻结」的明细；冻结明细不进结算单，解冻后才可汇总，概览 `GET /admin/cs-commission/summary` 新增 `frozen_cents` 把冻结金额从待结算净额里拆出单列。**冻结 / 解冻已有 HTTP 入口（2026-10-19）**：`POST /admin/cs-commission-entries/{id}/freeze`（body `{reason}` 必填，写 `frozen_by_admin_id`，重复冻结 409）、`POST /admin/cs-commission-entries/{id}/unfreeze`（body 可空，解冻写只增外盒 `settlement.cs_entry_unfrozen`，载荷带操作人与解冻前快照，便于审计追溯），权限码分别为 `esports:settlements:cs_entry_freeze` / `esports:settlements:cs_entry_unfreeze`（内置 `finance_admin` 白名单已含）；`GET /admin/cs-commission-entries` 目前仍不支持按「冻结中」筛选，只能页面侧过滤。
- **登记发放的守卫（§12.6）**：`POST /admin/settlements/{id}/pay` 只服务 `owner_type=cs` 的外部打款登记——陪玩结算单锁定即已把金额写入猫粮钱包，登记外部发放会被拒（「该结算单的领取主体是陪玩，金额已进入猫粮账户，不能登记外部发放」）；要求状态 `locked`、`paid_at` 为空、`payout_ref` 为空，**发放人不得与登记（锁定）人相同**，且按 `source_type=settlement AND source_id=<结算单号>` 查钱包流水，已入账的一律拒绝重复登记；作废同样要求作废人 ≠ 登记人。

未实现的 `ponytail` 项：「按提成周期累计净基数分档」是**顺序相关**的——档位按计提时点的周期累计判断，不做事后重算；「单笔手动指定」优先级未实现；自动结算单生成后不自动锁定（需财务确认后锁定）。`effective_at` 调度生效已由任务 `esports_activate_scheduled_commission_rules` 落地（到点把 scheduled 规则转 active），不再属于未实现项。

已实现**退款冲回尾差单列（§12.1）**：陪玩分成退款冲回（`reverseOrderCommission`）逐条按比例截断后，**舍入尾差归最后一条冲回明细**，保证冲回合计与「总原明细金额 × 退款比例」的精确值一致；客服提成每订单只有一条 service 明细，无跨明细尾差（其舍入尾差已落入工作室剩余）。

### 基础提现（§6.3 / §12.6）

只有**猫粮（已结算收入）**可提现，客户预付款退款走退款单、不从这里出。提现单 `EsportsWithdrawal` 记录申请金额、手续费、代扣税费、实际到账、方式、收款账号、审核与打款留痕；管理端挂「结算」菜单的「提现审核」页签。

- **申请（§6.3）**：`POST /user/player/withdrawals`（陪玩端自助，须携带 8–128 位幂等键）。校验提现方式（银行卡 / 微信 / 支付宝）、收款账号、提现门槛（`app.esports.withdrawal_min_cents`）与提现频率（`app.esports.withdrawal_min_interval_hours`，与本人最近一次申请的时间间隔；`app.esports.withdrawal_daily_limit`，本人当日申请次数上限，均为 `0` 表示不限）；手续费与代扣税费按**单一基点** `app.esports.withdrawal_fee_bp` / `app.esports.withdrawal_tax_bp` 或**阶梯费率** `app.esports.withdrawal_fee_tiers` / `app.esports.withdrawal_tax_tiers` 试算——阶梯为 JSON 数组（如 `[{"min_cents":0,"bp":100},{"min_cents":50000,"bp":50}]`），取「金额 ≥ `min_cents`」的最高档，配置了合法阶梯即以阶梯为准、否则回落单一基点；**实际到账 = 申请金额 − 手续费 − 税费**且必须为正。上述各配置键缺失或非法时回落默认值（不限门槛、频率与每日次数，费率为 0，无阶梯）。人工检查：把 `app.esports.withdrawal_daily_limit` 设为 1，当日第二次申请应被拒（提示今日已达上限），次日可再次申请；把 `app.esports.withdrawal_fee_tiers` 设为 `[{"min_cents":0,"bp":100},{"min_cents":50000,"bp":50}]` 后，申请 100 元应按 1%、申请 1000 元应按 0.5% 计手续费。
- **申请即扣减猫粮**：同事务写一条 `entry_type=withdraw` 的负数猫粮流水，钱离开猫粮账户由工作室代付——这样后续申请不会再把这笔算进可提现余额，从结构上避免重复申请超额；余额不足返回中文错误。幂等靠提现单 `idem_key` 唯一索引兜底，重复提交返回既有提现单、不重复扣减。
- **审核与打款（§6.3 / §11.2）**：首版一律财务审核。`POST /admin/withdrawals/:id/review` 通过转 `approved`；驳回须填原因、转 `rejected` 并在同事务写一条 `refund_return` 把申请金额退回猫粮（唯一索引保证只退一次）。`POST /admin/withdrawals/:id/pay` 把 `approved` 转 `paid`，记录外部打款凭证（`payout_ref`）、完税凭证（`tax_ref`）、到账时间与打款人。**打款不再动钱包**——钱在申请时已离开猫粮。
- **查询**：管理端 `GET /admin/withdrawals`（按陪玩 / 状态）与 `GET /admin/withdrawals/:id`；陪玩端 `GET /user/player/withdrawals`、`GET /user/player/withdrawals/summary`（可提现余额、待审金额、累计已提现）。陪玩工作台 `GET /user/player/dashboard` 增加 `catfood_balance_cents`（已结算可提现）。

已实现**每日提现次数上限（§6.3）**：系统配置 `app.esports.withdrawal_daily_limit`（按业务时区 UTC+8 自然日，`0` = 不限），申请时统计本人当日提现申请数（含已驳回），达上限返回参数错误。

已实现**阶梯费率（§11.2）**：手续费与代扣税费均可按金额分档（`app.esports.withdrawal_fee_tiers` / `app.esports.withdrawal_tax_tiers`，JSON 数组取「金额 ≥ `min_cents`」的最高档），未配置合法阶梯时回落单一基点。

未实现的 `ponytail` 项：差异化税率（按陪玩 / 渠道区分费率）未实现；手续费与税费在申请时确定性计算，财务不能在审核时改价；提现到银行卡 / 微信 / 支付宝的**真实打款通道未接入**，凭证为人工登记；提现是否计入财务收款回执与对账报表未接线；「已提现」不单独落收入状态，由提现单与猫粮流水共同表达。

### 云账户代发（陪玩每周一键打款，2026-10）

财务按 ISO 周（周一为一周起点）把本周已审核通过的陪玩提现单一键提交云账户代发；提交前可预览逐单「可发 / 跳过原因」。管理端挂在「结算」菜单的「批量代发」页签。

- **资金口径**：提现申请时猫粮已在同事务扣减（`entry_type=withdraw`），代发只负责**出款与落账**——批次与打款执行单都不再动猫粮账户；到账只把打款单 `pay_requested → paid`、提现单 `approved → paid`，失败只把打款单置 `pay_failed`、提现单保持 `approved` 等重试或线下处理。金额一律取提现单库内净额（申请金额 − 手续费 − 税费），事务内重新查单、不信任预览结果；提交人与提现单审核人不得为同一人。
- **周口径**：提现单没有 `approved_at`，周区间以 `reviewed_at` 落在 `[周一 00:00, 下周一 00:00)` 为准（UTC）；历史 `reviewed_at` 为空的已审核单不会自动进入本周批次，需人工补录审核时间。幂等键为 `yunzhanghu:<周>`，同一周重复提交返回既有批次；重试批次键为 `yunzhanghu:<周>:retry:<原批次 ID>`，独立编号与状态。
- **配置**（系统配置，完整键带 `app.esports.` 前缀，密钥不回显）：
  - `app.esports.payout_provider`：陪玩打款渠道，默认 `manual`；只有显式配成 `yunzhanghu` 才启用云账户，缺失 / 非法一律回落人工打款。
  - `app.esports.payout_provider_yunzhanghu`：JSON，默认 `{"enabled":false,"dealer_id":"","app_key":"","base_url":"","notify_base":"","sign_base":""}`；`enabled` 总开关、`dealer_id` 商户号、`app_key` 应用标识、`base_url` 接口地址、`notify_base` 回调地址前缀、`sign_base` 签约地址前缀。
  - `app.esports.payout_provider_yunzhanghu_private_key`：商户 RSA 私钥 PEM（PKCS#1 / PKCS#8），默认空，仅用于请求签名。
  - `app.esports.payout_provider_yunzhanghu_public_key`：云账户 RSA 公钥 PEM（X.509 / PKCS#1 / 证书），默认空，用于验证响应与回调签名。
  - 两个 PEM 单独成键，是因为 2048 位密钥超出系统配置单值上限；读取、错误日志与接口响应只输出脱敏摘要，**永不回显密钥内容**。
- **接口（陪玩端 3 个 / 管理端 6 个 / 公开回调 2 个）**：
  - 陪玩端（`AuthUser`，本人陪玩身份）：`GET /user/player/payout-account` 查看签约账户（银行卡 / 支付宝各至多一条，只回脱敏后四位）；`POST /user/player/payout-account/sign-url`（`account_type=bank_card|alipay`）返回签约托管地址，本地无记录时先落 `pending`；`POST /user/player/payout-account/unbind` 解约（省略 `account_type` 解全部）。
  - 管理端（`AuthAdmin`）：`GET /admin/payout-batches/preview?week=YYYY-Www` 预览；`GET /admin/payout-batches` 列表；`POST /admin/payout-batches` 一键提交（`week` + 可选 `withdrawal_ids`）；`GET /admin/payout-batches/:id` 批次明细；`POST /admin/payout-batches/:id/sync` 手动查单；`POST /admin/payout-batches/:id/retry` 仅重试原批次 `pay_failed` 明细（新建批次保留原失败记录做审计）。对应 6 个权限码：`esports:settlements:payout_batches_preview` / `payout_batches_read` / `payout_batches_create` / `payout_batches_detail` / `payout_batches_sync` / `payout_batches_retry`。
  - 公开回调：`POST /payment-notify/yunzhanghu`（`AuthPublic`，原始 body + `X-Yzh-Sign` 头或报文 `sign` 字段验签）；验签失败不改任何业务状态，通知单号不存在也应答成功以停止重试。
  - 公开回调：`POST /payment-notify/yunzhanghu/sign`（`AuthPublic`，同一验签口径）是签约结果唯一可信入口——前端微信 / 支付宝回跳参数不可信，只认服务端回调；`status=success` 时回填 `provider_sign_no` / `account_name` / `account_no_masked` 并置 `active`（`account_no` 传完整账号也在本地按后四位脱敏后落库）。验签失败不改任何状态并应答失败；`status≠success`、非法 `account_type`、`player_id` 不存在等业务错误一律应答成功，避免服务商重试风暴；重复回调按签约号幂等。
- **状态机**：批次 `submitting`（已落库，等待服务商结果或结果未知——超时 / 网络 / 验签失败，绝不判失败）→ `submitted`（已受理，仍有明细未到终态）/ `partial`（部分成功部分失败）/ `settled`（全部到账）/ `failed`（整批拒绝或全部失败）。打款单只有 `pay_requested → paid / pay_failed` 三态；回调与查单到账时同事务把打款单置 `paid`、提现单置 `paid` 并回填到账时间；失败只记失败码与原因，提现单保持 `approved`。对应关系：`submitting` = 全部 `pay_requested`；`submitted` = 至少一条 `pay_requested`；`partial` = `paid` 与 `pay_failed` 并存；`settled` = 全部 `paid`；`failed` = 全部 `pay_failed`。批次状态由明细重算，原 `submitting` 的批次在仍有 `pay_requested` 时保持 `submitting`，交给定时任务收敛。
- **定时任务**：`esports_sync_payout_batches` 每 5 分钟一轮，选 `submitting` 超过 2 分钟、或 `submitted / partial` 仍有 `pay_requested` 明细的批次（单轮上限 50 批）逐笔查单，复用回调落账逻辑收敛终态；渠道不是 `yunzhanghu`（或适配器未就绪）时整轮跳过、不报错，单批失败只记录并继续。
- **上线顺序**：① 保持 `payout_provider=manual`，先走原有「提现审核 → 人工打款 / 打款凭证」链路验证不受影响；② 配好云账户沙箱的 `base_url` / `sign_base` / 商户号 / 密钥并打开 `enabled`，`payout_provider` 仍留 `manual`；③ 用陪玩端完成签约，拿少量提现单提交批次，核对回调与手动 / 定时查单；④ 再切 `payout_provider=yunzhanghu`，批量代发入口这时才可用。
- **已知限制**：待签串口径按云账户公开文档通用规则实现，尚未与真实商户联调，联调时只需改 `payout_yunzhanghu.go` 一处（常量与 `post` / `verifiedEnvelope`）；`wechat` 渠道不参与代发，签约与代发只支持银行卡 / 支付宝，微信提现按银行卡签约账户校验。

### 结单表单、提交规则与审批流（§3.4）

把结单时填写的内容做成「字段库 + 模板」，并按 全局 / 游戏分区 / SKU + 金额区间绑定到订单；提交规则规定结单前「陪玩个人服务段上报」需多少成员完成；审批流则把「待复核」做成可配置的串行节点。管理端挂「结单表单」独立菜单（`esports:close_form:menu`）。

- **字段库**（`EsportsCloseFormField`）：`GET/POST /admin/close-form-fields` 建档，`PUT /admin/close-form-fields/:id` 原地编辑（乐观锁 `version`），`POST /admin/close-form-fields/:id/status` 启停。字段 `key` 为小写字母开头、仅含小写字母 / 数字 / 下划线的唯一标识，创建后不可改；`field_type` 覆盖 §3.4 自定义类型（`text` / `number` / `money` 金额只读 / `select` / `multiselect` / `datetime` / `image` / `switch` / `reference`）；单选与多选必须配置选项（写入时去空去重），`text` 可设 `max_length`（`0` = 不限）。字段库**不做版本化**，停用后新模板不可再引用、已生效模板不受影响。
- **模板**（`EsportsCloseFormTemplate`）：`GET/POST /admin/close-form-templates`、`PUT .../:id`（仅草稿可改，乐观锁）、`POST .../:id/activate`（同 code 现有 active 转 history）、`POST .../:id/rollback`（以历史版本为模板生成 max version + 1 的直接 active 行，原历史行不可变，§12.4）。模板 `fields` 是字段绑定列表（`field_key` + 该模板下的必填覆盖 + 排序），引用不存在或已停用的字段会被拒。
- **解析**：`resolveCloseFormTemplate` 按 `sku` > `game` > `global` 选层，同层取**命中金额区间**（`min_cents` ≤ 订单应付，且 `max_cents` 为 0 或 应付 < `max_cents`）且 `version` 最大的一条；无命中则不校验、不留痕。
- **结单校验与留痕**：`POST /admin/orders/:id/close` 正文新增 `form_values`（key = 字段库 key）。结单事务内按生效模板逐项校验——必填项缺一不可、`select` / `multiselect` 取值须在选项内、`text` 不超 `max_length`、`datetime` 须为 RFC3339；`money` 字段由系统按订单 `payable_cents` 自动填写、不取提交值。校验通过写一条 `EsportsCloseFormSnapshot`（订单 / 模板编号与版本 / 字段值 / 提交人与备注），同一订单可有多条（复核驳回后可重新结单）。管理端 `GET /admin/orders/:id/close-form` 返回订单适用模板、字段定义与最近一次快照，供结单弹窗渲染；该视图另返回 **`evidence_attachment_ids`**（与 `GET /admin/orders/:id/close-conditions` 同名同口径：订单全部服务段 `evidence_ref` 里的图片附件 ID、升序去重，纯文本 / 历史 `oss://` 凭证不产生 ID，无图片时为空数组），前端只调 close-form 就能拿到「需逐张确认」的完整清单。

- **结单前必须逐张查看凭证图片（§3.4-03）**：`POST /admin/orders/{id}/close` 正文新增 `viewed_evidence_attachment_ids`，须覆盖 `evidence_attachment_ids` 的**全部 ID**，缺一即返回 400 并逐条列出「尚未确认查看的附件 ID」；订单没有图片凭证时不必传，直接放行；比较与顺序无关、按 ID 去重，多给或伪造的 ID 不算确认也不会写进留痕。**不传 = 全部缺失 = 拒绝**，这是责任边界，不退化成「不传就通过」。`order.closed` 事件 details 落 `viewed_evidence_attachment_ids` 与 `viewed_by_admin_id`；走审批流时申请环节的已查看集合写进 `order.close_review_requested`，复核通过按服务段重新汇总后落 `order.closed`（复核人不再单独传 ID）。

- **提交规则**（`EsportsCloseSubmitRule`）：`GET/POST /admin/close-submit-rules`、`PUT .../:id`（仅草稿）、`POST .../:id/activate`、`POST .../:id/rollback`（版本管理同结单规则）。口径：服务段 `status=finished` 且（上报服务量 > 0 或已填服务凭证）视为该成员「已提交」。`mode`：`single` 与 `multi` 现在**都要求所有实际服务成员逐人提交**（`single` 只表示单席场景，多席时与 `multi` 等价，依据 §3.4-76；此前 `single` 允许「任一人提交即算全员」已被收严）／`custom` 按 `min_submit_ratio_bp` 向上取整（`0` = 不要求）；`allow_substitute` 允许客服在结单时代未上报成员补填服务量与凭证（写回服务段并留 `order.service_submitted_by_admin`）；`on_missing`：`block` 未达标禁止结单／`remind` 放行并写 `order.close_submit_remind` 事务外盒／`auto_accept` 放行并留 `order.close_submit_auto_accepted`。结单正文可带 `substitute_reports`（`seat_index` + `reported_units` + `evidence_ref`）。解析按 `sku` > `game` > `global`，同层取 version 最大。**凭证图片张数底线对「代提交」与「视为已提交」同样生效（2026-10）**：取订单生效结单规则的 `MinEvidence`（「服务截图不少于 N 张」），`allow_substitute` 代提交时每一条 `substitute_reports.evidence_ref` 都必须是**不少于 N 张图片附件 ID** 的 JSON 数组，否则返回中文错误「结单规则要求至少 N 张服务凭证图片，第 X 个席位的代提交当前只有 M 张图片（文本凭证不能替代图片），还差 K 张」；`on_missing=auto_accept` 写留痕前校验**全单图片张数**（只算图片 ID，纯文本 / `oss://` 一律按 0 张）是否达到 N，不足则拒绝并提示还差几张——堵住「用文本把凭证张数凑够再视为已提交」。`remind`（放行催办）保持原语义、不加码。未配置生效结单规则时 `MinEvidence = 0`，此时不加图片门槛（存量只填文本凭证的订单不会被追溯卡死，由结单条件的既有口径判定）；代提交的图片 ID 暂不校验附件归属与存在性。

- **审批流**（`EsportsCloseApprovalFlow` + `EsportsCloseApproval`）：`GET/POST /admin/close-approval-flows`、`PUT .../:id`、`POST .../:id/activate`、`POST .../:id/rollback`（版本管理同结单规则）。按 全局 / 游戏分区 / SKU 绑定，并以 `min_cents` 作为触发阈值（`0` = 所有结单都需审批）。`nodes` 是串行节点：`seq` 从 1 连续递增、`role` 为 `admin`（任一管理员，不得自审）/ `super_admin`（超级管理员）、`timeout_hours`（0~720，0 = 不限）、`auto_pass_on_timeout`、`involves_funds`；**涉及资金 / 争议的节点禁止自动通过**（建流 / 改流时直接拒绝）。订单结单命中生效流即建立审批进度 `EsportsCloseApproval`（`steps` 记录每个节点的处理人 / 时间 / 意见 / 是否自动通过），**逐个节点推进、末节点通过才真正结单**；未配置审批流时回落结单规则的 `review_threshold_cents`，行为等价内置单节点「管理员复核」（`flow_id = 0`）。`GET /admin/orders/:id/close-approval` 返回当前待审批节点；`POST /admin/orders/:id/close-approve` 推进当前节点（中间节点写 `order.close_approval_node_approved` 并推进订单版本），`POST /admin/orders/:id/close-reject` 驳回并回退待结单（审批进度转 `rejected`，重新结单时重建）。任务 `esports_check_close_approval_timeout` 处理节点超时：允许自动通过的节点超时自动推进（`auto_passed = true`），其余写 `close.approval_timeout` 外盒升级（幂等键含订单 / 节点 / 阈值）。

未实现的 `ponytail` 项：拖拽画布与任意坐标、字段级联与条件显示、字段库版本历史、按订单类型（即时 / 预约）绑定模板、模板在**下单时**快照（当前在结单时解析生效模板并记版本号）、同一层多模板叠加（同层只取版本最高的一条）；提交规则未接入「陪玩端自助提交结单表单」（首版由陪玩 `service/finish` 上报服务段、客服手动结单）；审批流只支持串行节点，并行 / 条件分支、按具体角色枚举（当前仅管理员 / 超级管理员）与超时自动升级角色未实现。

### 优先派单权与独占优先通知（§5.4 / §5.5 首版）

`EsportsDispatchPriorityGrant` 是「优先派单权」名单：按**游戏分区 + 周期**授予。业务口径为**独占优先通知**——订单先只给名单内陪玩，名单内全部拒绝或通知超时才释放给所有人。

> 口径来源：功能列表 §5.4 已按业务方澄清改为**独占优先通知**（原文「排序加权、不是独占、抢单大厅不受影响」已废弃），§2.1 / §5.2 / §5.5 / §5.6 / §5.8 / §5.9 / §6.1 的相关表述同步修正。硬性条件仍与人工指派一致（资质与在线）。

- **名单（§5.4）**：`GET/POST /admin/dispatch/priority`、`POST /admin/dispatch/priority/:id/revoke`。周期支持日 / 周 / 半月 / 月（半月为每月 1–15 日与 16 日至次月 1 日）；每游戏每期名额上限取系统配置 `app.esports.dispatch_priority_quota`（缺失回落 3）；获得方式 `ranking / points / threshold / rotation / manual`；`next_period=true` 可在切换点前预生成下一期名单。重复录入同一陪玩、超出名额、非法周期/来源/排名都会被拒（判重先于判名额，报错更精确）；`game` 必须对应**启用中**的商品分类（游戏分区），填错直接返回参数错误并指出名单项（「名单项 game「xxx」找不到对应的启用游戏分区（商品分类）…」），不再静默落库后静默不开窗。移出名单须填原因，移出后名额立即释放。
- **周期重算**：`esports_rebuild_dispatch_priority` 任务把已过期的生效名单转 `expired`、把到点的待生效名单转 `active`，并为下一期预生成名单（延续本期生效名单、按排名、`source=rotation`、状态 `pending`）；下一期已存在名单时不覆盖、不重复生成。
- **开窗与通知（独占）**：订单进入待派单时统一走 `onOrderEnteredPendingDispatch`（资金核销、席位拒绝/撤回/过期回退等全部路径都已接入）。若订单 SKU → 分类能解析出游戏分区且该分区有生效名单，则开窗：订单记 `priority_notice_status=pending`、`priority_notice_until=now+app.esports.dispatch_priority_notice_minutes`（缺失回落 3 分钟）、`priority_game` 快照，并按名单逐人写 `EsportsDispatchPriorityNotice`（`notified`）与事务外盒事件 `order.priority_notice`。窗口内**只有被通知的人**在抢单大厅看得到这张单（`listGrabHall` 只对他的名单外订单做过滤）；其他人看不到、直接抢会被拒（403「该订单正在优先通知名单内陪玩，暂未对大厅开放」）。解析不到游戏分区或该分区没有名单时不开窗，订单留在待派单池由客服指派；**`required_seats > 1` 的多席订单同样不开窗**（首版抢单大厅只接受单席，开窗只会产生必失败的通知与长期「通知中」），留待客服指派或固排成队。
- **接受与闭合**：名单内陪玩走既有 `POST /user/player/orders/:id/grab` 接单——窗口内该接口额外放行「被通知的本人」，接单成功后订单先转 `pending_customer_confirm`（待客户确认）、本人通知转 `accepted`、其余待回应通知作废、订单 `priority_notice_status=accepted`；客户确认后才转 `accepted`。
- **拒绝与释放**：`GET /user/player/dispatch-notices` 看我的优先通知，`POST /user/player/dispatch-notices/:id/decline` 拒绝（只能拒自己的通知）。名单内仍有人待回应时订单留在待派单；**最后一人拒绝**时订单释放到抢单大厅（`fulfillment_status=grabbing`、`priority_notice_status=released`、原因「名单内全部拒绝」）。`esports_expire_dispatch_priority_notice` 任务把超过截止时间的窗口同样释放到大厅（原因「优先通知超时」）。释放后不再重复通知、名单不再有特权，任何人可抢。
- **客服干预**：`GET /admin/orders/:id/priority-notices` 看谁被通知/拒绝/接单；`POST /admin/orders/:id/release-priority` 由客服提前释放到大厅（§5.2「可随时人工干预」）；客服指派不受窗口限制，指派成功后窗口**立即闭合**——`pending → released`、释放原因写「已人工指派确定陪玩」并记 `priority_released_at`，待回应通知置 expired、写 `order.priority_notice_closed` 事件，且**不改履约状态**（多席订单仍停在待派单）；固排成队确定陪玩后同样闭合。
- **派单候选（§5.5 首版）**：`GET /admin/orders/:id/dispatch-candidates` 列出**当前可派单对象**——窗口内只列被通知的名单内陪玩，窗口外列全部合格在线陪玩；硬性条件与人工指派门槛一致（资质 `qualified` 且在线），已在本订单席位上的陪玩不再列出。排序为：指定陪玩（订单 `preferred_player_id` 优先，接口参数 `preferred_player_id` 可覆盖；命中者标记 `Preferred` 置顶）→ 评分高者优先（**样本不足不当作低分**）→ 近 7 天在途席位数少者优先（轮转均衡）→ 档案 ID 兜底。

已实现 §5.6 的独立发布动作「发布到抢单大厅」（`POST /admin/orders/{id}/publish-grab`）：把已付款待派单的**单陪玩**订单直接转 `grabbing`（多人订单返回参数错误并提示改用指派），窗口仍开着时等价于「提前释放」；发布前把被撤回 / 过期的席位重置为 `open`，保证抢单大厅一定有可抢席位，并留 `order.published_to_grab_hall` 事件与事务外盒。

已实现 **「客户指定陪玩的订单跳过优先通知窗口」（§5.4）**：订单新增可空字段 `preferred_player_id`，报单时可选填并校验陪玩资质（非 `qualified` 返回参数错误，§4.6）；`onOrderEnteredPendingDispatch` 对指定单**不开优先通知窗口**，订单留在待派单池由客服指派；派单候选默认回落订单上的指定陪玩并置顶标记 `Preferred`。客户端下单页可通过公开 `GET /players` 列表直接选择陪玩，选中后以 `preferred_player_id` 提交，语义与首页推荐卡带入一致。

### 抢单后客户确认 / 更换（2026-11）

抢单（大厅与优先通知窗口内接单）成功后不再直接进入 `accepted`，而是进入新履约状态 `pending_customer_confirm`（待客户确认）：

- **抢占与截止时间**：订单写入 `customer_confirm_deadline = now + app.esports.customer_grab_confirm_minutes`（系统配置 `customer_grab_confirm_minutes`，默认 5 分钟，1~1440，非法回落 5），写 `order.grabbed` 事件（details 标 `pending_customer_confirm`）与事务外盒 `order.customer_confirm_required`（客户 + 责任客服 + 该席位陪玩）。此时**不写** `ready_to_start_at`，陪玩开工接口会拒绝。
- **客户确认**：`POST /user/orders/{id}/confirm-player` → 订单转 `accepted`，写 `order.customer_confirmed_player` 事件与 `order.ready_to_start`（此后陪玩可开工），并写通知外盒。
- **客户更换**：`POST /user/orders/{id}/decline-player` → 当前陪玩席位 `accepted → revoked → open`，订单直接回 `grabbing`，写 `order.customer_declined_player` 事件（被换下的陪玩按 payload `player_id` 定向通知），并广播抢单大厅刷新；不经过待派单，也无需客服确认。
- **超时自动确认**：任务 `esports_auto_confirm_customer_grabs`（每分钟）扫描截止时间已过的订单并自动确认，保证陪玩不被长期挂起。
- **兼容口径**：客服看板新增 `pending_customer_confirm` 桶；管理端订单列表可按该状态筛选；客服可在待确认期间撤席（`revokeDispatchSeat` 放开该状态，订单回待派单后重新指派）；陪玩「我的订单」按待接单可见该单；订单冻结（存单）期间确认 / 更换返回冲突。
- **不适用**：客服指派邀请接单、客户指定陪玩单（仍由客服指派）不发生客户确认环节；多人单不进抢单大厅，也不涉及。

已实现 **§5.9 的等待时长埋点**：订单新增 `first_pooled_at` / `first_invited_at` / `first_accepted_at` / `service_started_at` / `priority_released_at`（各自只写一次、永不改写），统一在进入待派单（`onOrderEnteredPendingDispatch`）、派出邀请、有效接单、开工与优先通知释放处落点；派单效率报表（`dispatch_efficiency`）据此派生 `avg_pool_to_invite_seconds` / `avg_invite_to_accept_seconds` / `avg_accept_to_start_seconds` / `avg_pool_to_start_seconds` 四类平均等待时长（时间点不全的历史订单不计入样本）。

已实现**接单上限（§5.5 / §6.4）**：陪玩档案新增 `daily_limit`（每日可履约席位数，按接单自然日）与 `concurrent_limit`（并发可履约席位数），在**指派、接受邀请、抢单**三处接单动作原子硬校验（超限返回参数错误并说明上限，`0` = 不限）；候选池未按上限过滤，仍在接单瞬间拦截。

已实现**游戏分区权限 + 技能等级（§5.5）**：陪玩档案新增 `games`（接单游戏分区列表，空 = 不限），SKU 新增 `required_level`（所需技能等级，报单时快照到订单）；在**指派、接受邀请、抢单**三处接单动作与**派单候选池**统一硬校验「陪玩已开通该分区 + 陪玩等级不低于订单所需等级」，不满足返回 403 / 从候选池剔除。

未实现的 `ponytail` 项：排名制 / 积分制 / 达标制的**自动计算**未接入（缺少排名、积分与达标数据源），自动重算只做「上一期名单延续到下一期」，人工指定与人工增减不受影响；历史合作、客户偏好、距离或时区等加分项未实现；推送已接 `notification_push_webhook`（未配置时仍靠事务外盒事件 + 陪玩端轮询优先通知列表，见「2026-10-17 修复批次」第 3 项）；评分按每位候选单独计算，候选量级大时应改为一次聚合。

### 陪玩固排（§5.3 首版）

- **固排关系**：`EsportsTeammate` 是陪玩之间按游戏分区互加的固定队友关系（无方向，规范化存为 `player_id < teammate_player_id`，仅以 `initiator_player_id` 记录发起方）。双方确认后建立（`confirmed`），任一方可解除（`dissolved`）；邀请状态 `pending`。
- **陪玩端接口**：`POST /user/player/teammates` 发起（须指定对方陪玩档案 ID、游戏分区与 8–128 位幂等键）；`GET /user/player/teammates` 我的固排；`GET /user/player/teammate-requests` 待我确认的邀请；`POST /user/player/teammate-requests/:id/accept` / `.../decline` 确认 / 拒绝（仅被邀请方可操作）；`POST /user/player/teammates/:id/dissolve` 任一方解除；`GET /user/player/players?keyword=` 按昵称搜索可添加的陪玩（排除自己）。
- **约束**：同一对陪玩在同一游戏分区只允许一条「待确认 / 已建立」关系（重复发起 `errConflict`，幂等键重放返回既有记录）；自己加自己、被邀请方之外的操作均被拦截；解除 / 拒绝后可重新发起。
- **派单侧成队（§5.3）**：`GET /admin/orders/:id/teammate-candidates?main_player_id=` 列出主陪玩在该订单上可一起成队的合格固排；`POST /admin/orders/:id/team-dispatch`（`main_player_id` + `teammate_player_ids`）主陪玩占第一个空席位、选中固排依次占后续空席位，**同一事务一次成队**，席位 `source=fixed_team`、状态 `invited`，订单转待接单并写一条 `order.team_dispatched` 事件。硬性条件与人工指派一致（资质 + 在线 + 分区/等级 + 接单上限），且固排必须是与主陪玩在该分区已建立的关系；空席位不足 / 非固排 / 不合格均被拒。

未实现的 `ponytail` 项：陪玩端「主陪玩从固排列表主动邀请队友」已实现（`POST /user/player/orders/{id}/team-invites`，见「陪玩端主动邀请固排（2026-10）」），后台成队仍只在「席位」抽屉按席位序依次邀请；固排关系建立不校验双方资格与在线状态（仅需对方陪玩档案存在）。

### 陪玩转单与催单（§5.7 首版）

- **陪玩转单（§5.7）**：`EsportsSeatTransferRequest` 是转单申请单。陪玩端 `POST /user/player/orders/:id/transfer`（须 8–128 位幂等键）发起——仅本人在该订单上「席位为 `accepted` 且本单没有服务段」时可发起，订单须处于待接单 / 已接单；同一席位同时只允许一条待确认申请（重复提交同幂等键返回既有申请，换键则冲突）。**确认前不动席位与订单**：避免陪玩单方面退出占位。管理端 `GET /admin/seat-transfers` 查看、`POST /admin/seat-transfers/:id/decision` 确认：通过时席位回到 `open` 并清空陪玩（待补位），按剩余有效席位把订单退回 `pending_acceptance`（还有队友占位）或 `pending_dispatch`（无人占位，并调用 §5.4 的 `onOrderEnteredPendingDispatch` 重开优先通知）；驳回须填原因且保持原状。陪玩端 `POST /user/player/transfer-requests/:id/cancel` 可在确认前撤回。
- **催单时间阶梯（§5.7）**：`app.esports.remind_ladder_minutes`（逗号分隔、升序、去重，缺失或非法回落 `5,15,30`）。`esports_remind_dispatch_ladder` 任务对「待接单」订单的邀请席位，按邀请发出时间（由 `expires_at − 邀请时长` 反推）算已过分钟数，取满足的最大档位并补齐所有未发过的档位：第 1 档 `dispatch.remind_player` 提醒在线陪玩，第 2 档起 `dispatch.remind_admin` 升级提醒客服。幂等靠事务外盒 `event_key=dispatch_remind:<order>:<seat>:<level>` 唯一索引，因此重复运行只补发新档位。
- **服务超时提醒（§5.7）**：`app.esports.service_overtime_minutes`（0 表示关闭）。`esports_check_service_overtime` 任务对「服务中」订单的在服务服务段计算有效时长（`now − started_at − paused_seconds`），超过阈值即写 `dispatch.service_overtime` 事务外盒与 `order.service_overtime` 订单事件，提醒客服确认是否加钟；幂等键含阈值，调整阈值后可重新提醒。

未实现的 `ponytail` 项：**已开始服务后的转单**属售后补履约范畴（§13.6），首版直接拒绝并提示走售后（补履约任务只由售后方案「补钟 / 换人」产生，见 §13.6；转单通过后只把席位放回待补位）；**服务超时只提醒、不自动发起加钟**（加钟由客服在订单页手动发起、陪玩确认，见 §6.2 / §12.5）；**订单与 SKU 未声明每单位时长**，「服务时长超过订单约定」暂用可配置阈值近似，按承诺服务量换算时长与差异化口径未实现；催单渠道只有事务外盒（系统内消息），企微应用消息与电话 / 语音未接入；转单不校验「转出方是否仍需承担本单责任」，责任判定未实现。

## 排单与在线状态口径（2026-10）

- **在线语义（P1-01）**：落库仍是 `offline / online / protected_abnormal`，展示层新增 `presence_label`（`在线` / `连接异常（受保护）` / `已下线`），陪玩首页 `dashboard` 与排单看板 `players` 都返回该字段，看板不再把在途履约者显示成「已下线」。**`protected_abnormal` 一律不具备派单资格**：派单候选池收窄为 `presence = online`，指派、接受邀请、抢单三处一并拒绝——在途订单只保留履约与结算，客服需通过「恢复在线 / 重新心跳」把人放回候选池（与早前「有在途订单保留上线状态」的解释不同，按 BACKLOG 的连接异常暂缓新派单执行）。**人工下线不会被心跳改回在线**：`presence=offline`（陪玩自助下线与管理员改档）同时把 `auto_online` 置 false，自动上线只对「因心跳超时被动下线」的陪玩生效；想恢复自动上线需重新打开该开关。抢单只要求「非 `protected_abnormal` 且心跳未超时」，因此人工下线本身不阻止抢单（既有口径）。
- **待结单判定（P1-02）**：由「席位数」改为「所有在册席位都了结」——`orderAllSeatsServiceSettled` 逐席位判定「该席位全部服务段 `finished`」或「席位已被移除（`revoked / rejected / expired`）」，任一席位「无服务段且仍占位（`open / invited / accepted`）」即未了结。三处入口统一（陪玩 `service/finish`、客服记名停工、客服终止服务段）；未了结时把 `unsettled_seat_indexes` 写进 `order.service_finished` / `order.service_terminated` 事件，客服能定位哪个席位从未开工。`revokeDispatchSeat` 允许移除「邀请中」或「已接单但从未开工（该席位无任何服务段）」的席位（已开工必须走终止服务段，否则绕过责任认定），移除后若订单已全部了结则直接转 `pending_close` 并写 `order.pending_close_entered`。订单**无席位**时返回「未了结」，维持「历史单不自动转待结单」。
- **改派时暂停中的服务段也结算（P1-03）**：`settleSeatServiceSegment` 同时匹配 `in_service` 与 `paused`，先把在途暂停秒数并入 `paused_seconds`，再按「墙钟 − 累计暂停」写 `system_seconds`、清 `paused_at`、状态落 `finished`，逐条条件更新。服务段一旦 `finished` 即**不可再恢复**：本人 resume 与客服代恢复都要求 `status=paused` 且条件 UPDATE 带该状态。「因改派终止」与「正常结束」的区分保留在 `order.service_player_replaced` 事件的 `replaced_player_id`（未新增 `terminated` 枚举）。更正服务时长只能改已 `finished` 段的秒数，不存在「被更正复活」的路径。
- **手动催单要发外盒（P1-04）**：`POST /admin/orders/{id}/remind` 在同一事务内逐席位写 `dispatch.remind_player`（载荷含订单 / 席位 / 陪玩 / 档位 / 备注 / `source=manual`）并写一条 `dispatch.remind_admin`（收件人为责任客服，订单无负责客服时回落本次操作客服），事件键 `dispatch_remind_manual:<订单ID>:<本次催单占用的订单版本>:player:<席位>` / `:admin` 天然幂等；与阶梯催单的 `dispatch_remind:<order>:<seat>:<level>` 语义不同、互不覆盖。存单冻结仍先拦（409）。
- **优先通知窗口闭合与名单准入（P1-05）**：人工指派（含服务中换人）与固排成队确定陪玩后**立即闭合窗口**——`pending → released`、`priority_release_reason=已人工指派确定陪玩` + `priority_released_at`、待回应通知置 expired、写 `order.priority_notice_closed`，**不改履约状态**（多席订单仍停在待派单）。授名单时 `game` 做准入校验：必须存在**启用中**的 `EsportsProductCategory.game`，否则返回参数错误并指明名单项（`名单项 game「xxx」找不到对应的启用游戏分区（商品分类）…`），不再静默落库后静默不开窗。**`required_seats > 1` 的订单不开优先通知窗口**，留在待派单池由客服指派 / 固排成队（首版抢单大厅只接受单席）。窗口状态仍是 `none / pending / released / accepted`，用释放原因区分「释放到大厅」与「已指派闭合」；历史上已开窗的订单在成队 / 指派后同样闭合。
- **抢单大厅返回最小视图（P1-06）**：`GET /user/player/grab-hall` 改为专用最小视图而非完整订单——保留 `id / order_no / sku_id / sku_code / sku_name / spec_name / game（由 SKU → 分类解析，非 priority_game） / priority_game / quantity / required_seats / promised_service_units / per_seat_service_units / service_duration_minutes / required_level / appointment_status / requested_start_at / unit_price_cents / payable_cents / fulfillment_status / payment_status / priority_notice_status / priority_notice_until / created_at`；`customer_id` / `addon_cents` / `addon_units` 保留键位但恒为 0 并用 `omitempty` 从 JSON 省略；**不再返回**分成与提成（`commission*` / `cs_commission_*` / `manual_commission_*`）、`channel_cost_cents`、`discount_cents` / `manual_adjust_cents`、退款与预占（`refund_status` / `refunded_cents` / `refund_reserved_cents`）、`close_net_service_cents`、会员等级、`responsible_admin_id` / `created_by_admin_id`、内部催单备注与时间线埋点。`POST /user/player/orders/{id}/grab` 的**成功响应仍是完整订单**（是否一并收窄待产品确认）。

## 开发环境人工检查入口

启动进程前设置 `ESPORTS_EXPERIMENTAL=1` 即启用（**不再要求 `CMF_APP_ENV=development`**；`CMF_APP_ENV` 只决定运行环境与插件签名策略），并把 `app.esports.payment_deadline_minutes` 配成非零。用管理员为员工分配上述菜单与按钮权限。

- 商品：进入“商品”菜单，新增产品分类后新增商品并绑定分类；编辑商品、上下架；删除已被商品引用的分类应失败。
- 钱包：进入“钱包”菜单，为客户归属人充值（本金 + 赠送），余额应等于流水之和；重复提交同一幂等键应失败；调整不得把任一子账户调成负数。陪玩猫粮的充值 / 调整（`POST /api/v1/esports/admin/wallets/{id}/recharge|adjust?account_type=player_catfood`）**仅超级管理员可操作**：非超管应返回 403「陪玩猫粮的充值 / 调整仅限超级管理员操作」且零流水落账，客户余额口径仍按归属客服校验。
- 订单：在“订单”菜单对未支付订单改价（份数变化应同步占用名额、旧报价版本作废）、改期、取消（名额释放）、余额核销（余额足够时转入待派单，余额不足应被拦截）。存单：仅**已付款**订单可点「存单」把订单冻结存起来，点「恢复」清掉标记；存单只是订单上的一个冻结标记，**不改变履约状态、不释放名额占用**，未付款单、已结束 / 已取消单、重复存单均被拒绝。加钟：对**服务中**订单点“加钟”填追加金额 / 追加服务量与备注（金额与服务量至少一项为正）后生成“待陪玩确认”的加钟单；非服务中订单、待结单订单发起与同一订单重复发起均应被拒。用陪玩身份调 `POST /api/v1/esports/user/player/addons/{id}/confirm` 确认：订单 `addon_cents` / `addon_units` 累加且 `payable_cents` 不变，按确认时点生效的分成规则生成 `kind=addon` 的分成明细（如全局 50% 规则 + 加钟 40 元 → 2000 分，基数记 4000）；非本单陪玩确认返回 403，重复确认失败。拒绝须填原因且订单不累加（写 `order.addon_rejected`）。订单存在“待陪玩确认”加钟时结单被拒，客服“撤回”该加钟后可正常结单。加钟收款：确认后对该加钟单点“余额收款”，应收追加金额应从客户余额扣减（含批次分摊）、加钟单转“已收”、留 `order.addon_paid` 事件；客户余额不足、加钟未确认或重复收款均应失败。加钟退款分摊：对有加钟分成明细的订单走一遍售后部分退款，到“结算 → 分成明细”应同时看到 `service` 与 `addon` 两条同比例负数冲回。取消政策（§3.3）：把 `app.esports.cancel_free_hours` 设为 2、`app.esports.cancel_fee_bp` 设为 5000，对一张已付款待派单订单调 `GET /api/v1/esports/admin/orders/{id}/cancel-preview`：付款后 2 小时内应显示免费取消、应退 = 应付；把订单 `paid_at` 回拨 3 小时后再查询应显示手续费 = 应付的一半、应退减半。此后点“取消”应转“已取消”、SKU 已售名额退回（占用记录转 `returned`）、该单未终结席位作废，并在“售后”菜单看到一条 `退款申请` 售后单（来源为该订单、渠道为客服），退款按该单审批与执行；对已有服务段的订单点“取消”应被拒并提示走售后。
- 报单：为两个不同员工分配 `esports:age_checks:create`、`esports:age_checks:list`、`esports:age_checks:review` 及对应按钮权限，提交人与复核人必须不同。进入“报单 → 客户”，创建客户并上传证据至 `private/esports/age-checks`，提交年龄核验；另一人到“年龄资格复核”查看证据、批准或驳回。批准后客户 `age_status` 及 `version` 更新，拒绝不改变资格；重复提交、本人自审、旧版本审批和删除引用中的附件均应失败。报单时可选填“指定陪玩”（陪玩档案 ID）：填 `qualified` 陪玩可正常下单，该订单进入待派单后**不开优先通知窗口**（派单候选中本人置顶）；填未通过考核或已停用的陪玩应返回参数错误（§4.6）。
- 排单：进入“排单”菜单查看看板（在线陪玩数、待派单池/公共池/抢单中/待接单/已接单/服务中/待结单/异常分桶）。对已付款待派单订单“指派”陪玩（陪玩须 `qualification=qualified` 且 `presence=online`），席位转 `invited` 且订单转待接单；从“席位”抽屉撤回邀请后订单退回待派单；公共池订单可“认领”，重复认领应失败；转单改变负责客服；手动催单应在同一事务内写 `dispatch.remind_player`（逐席位）与 `dispatch.remind_admin`（责任客服，无负责客服回落操作人）两条外盒，重复催单按键幂等（自动阶梯催单见下条）。邀请有效期取系统配置 `app.esports.dispatch_invite_ttl_minutes`（缺失按 10 分钟），超时由 `esports_expire_dispatch_invites` 任务回收。对没有优先名单（或窗口已闭合）的已付款待派单单陪玩订单可点“发布抢单”：订单转“抢单中”，此后任意合格陪玩在抢单大厅可见并可抢；重复发布、未付款单与多人订单均应失败（多人订单提示改用指派）；若该单此前有被撤回 / 过期的席位，发布后席位应重置为“待接单”的前置状态 `open`。会员优先：把某会员等级的 `priority_dispatch` 设为开启并把某客户挂到该等级后，其待派单订单应置顶于看板“待派单池”（公共池同理）并在 `member_priority_order_ids` 中返回；把该等级关闭 `priority_dispatch` 后应不再置顶。接单上限：在“陪玩”编辑里设「并发接单上限=1」保存后，该陪玩已有 1 个 `accepted` 席位时，再对其指派、接受邀请或抢单均应返回参数错误（提示并发上限）；设「每日接单上限=1」后，当日接满 1 单再接单同样被拒；把上限改回 0（不限）后恢复正常。游戏分区 + 技能等级：在“商品”编辑里把某 SKU 的「所需技能等级」设为“资深”，在“陪玩”编辑里给陪玩设「接单游戏分区」为不含该订单所属分区、或等级低于“资深”，再对其指派该 SKU 订单应返回 403（提示未开通分区 / 等级不足）；把陪玩分区与等级设到满足条件后应可正常指派，且“派单候选”中不再出现不满足条件的陪玩。
- 陪玩：进入“陪玩”菜单可搜索陪玩档案、新建（`user_id` + 昵称）与编辑（昵称 / 资格 / 在线状态）。陪玩端自助接口面向 uni-app 客户端（当前无独立前端页面），可用登录用户身份调用：`dashboard` 查看在线状态与待接邀请/进行中订单/当日猫粮收入；`presence` 上下线；`invites` 列表并 `accept`/`reject`（`:id` 为席位 ID，接受在唯一席位接满时订单转 `accepted`，拒绝在无剩余席位时订单退回 `pending_dispatch`）；`grab-hall` 列出 `grabbing` 单席位订单并 `grab`（合格陪玩，人工下线仍可抢单；响应为最小视图，不含 `customer_id`、分成 / 提成、渠道成本、退款与调整金额、报单人等字段）；`orders` 按 `bucket` 查看我的订单；`service/start` 开始服务（订单 `accepted`→`in_service`），`service/finish` 结束服务（记上报服务量与系统秒数，**所有在册席位服务段均终态且无未开工席位**才转 `pending_close`，否则事件里带 `unsettled_seat_indexes`）。心跳（§6.4）：用陪玩身份调 `POST /api/v1/esports/user/player/heartbeat` 应只刷新 `last_heartbeat_at`、不改变 `presence`；把 `app.esports.player_heartbeat_timeout_minutes` 设为 10 并把该陪玩的 `last_heartbeat_at` 回拨 30 分钟后跑 `esports_auto_offline_players`：无在途订单应转 `offline`，若其在待接单 / 已接单 / 服务中订单上持有已接单席位则应转 `protected_abnormal` 并写一条 `player.heartbeat_timeout` 外盒；重复跑不再处理。服务售后申诉（§13.2）：用本单服务成员身份调 `POST /api/v1/esports/user/player/order-after-sales`（填 `order_id`、`kind`、`demand`）应建立待受理售后单；非本单成员返回 403，`kind` 填 `refund_request` 返回参数错误。
- 结单：进入“订单”菜单，对 `pending_close` 订单点“结单”填写备注。未上传服务凭证或服务时长为零时结单失败并提示具体原因，订单保持待结单；成功则订单转 `completed`、写入 `closed_at` 与 `order.closed` 事件，重复结单失败。若该单的陪玩上传了图片凭证，结单弹窗须先逐张「查看」并把确认过的 ID 作为 `viewed_evidence_attachment_ids` 回传（清单取 `GET /api/v1/esports/admin/orders/{id}/close-form` 的 `evidence_attachment_ids` 或 `.../close-conditions` 的同名字段），漏传或漏确认会被 400 拦下并列出还差的附件 ID；只有文本凭证的订单不必传。结单规则：进入「结算 → 结单规则」新建规则（如全局、凭证 2 张、复核阈值 10 元）并“生效”：凭证 1 张的订单结单应失败（提示凭证不足）；应付达 10 元且由负责客服本人结单应失败，换管理员结单应成功；“回滚”历史版本应生成更高版本并直接生效。结单复核：金额达复核阈值结单时订单转“待复核”（`pending_close_review`）并留 `order.close_review_requested`，**不**直接完成；负责客服本人复核应返回 403（不得自审），换管理员“复核通过”后订单 `completed` 并计提分成 / 提成 / 成长值，或“复核驳回”（须填原因）后回退 `pending_close`、留 `order.close_review_rejected`。结单表单：进入「结单表单」菜单先在“字段库”新建字段（如类型选“单选”、选项填“满意 / 不满意”），再到“表单模板”新建模板（如全局、绑定该字段并勾选必填）并“生效”；此后对 `pending_close` 订单点“结单”应弹出该表单项，不填必填项时前端拦截、直接调接口也返回中文错误（提示缺哪一项）；填全后结单成功，调 `GET /api/v1/esports/admin/orders/{id}/close-form` 的 `latest_snapshot` 应能看到提交值与模板版本；把模板“回滚”应生成更高版本号的新记录。提交规则：到「结单表单 → 提交规则」新建规则（模式选“多人提交”、未达标选“禁止结单”）并“生效”：一张有 2 名服务成员、仅 1 人上报（服务量或凭证）的待结单订单点“结单”应被拒（提示需 2 / 共 2，当前 1）；把该规则的「允许客服代提交」打开后重启新版本并生效，结单弹窗应出现“代提交未上报陪玩”区块，填第 2 席的服务量**并按结单规则要求的图片张数填图片附件 ID**（结单规则要求凭证 N 张时，代提交只填文本凭证会被拒并提示「还差 K 张」，文本不能替代图片）后应结单成功；改选“放行并催办提醒”后同样订单应结单成功且事务外盒多一条 `order.close_submit_remind`；改选“视为已提交”后应结单成功并留 `order.close_submit_auto_accepted`（若规则要求 N 张图片，全单图片张数不足会被拒）。审批流：到「结单表单 → 审批流」新建流程（触发金额 10 元、节点 1「主管复核」角色任一管理员、节点 2「管理员终审」角色超级管理员）并“生效”：对一张应付 20 元的待结单订单点“结单”应进入待复核；订单详情「结单审批」应显示「待主管复核（第 1 / 2 节点）」；用普通管理员“复核通过”后订单**仍在待复核**、详情显示「待管理员终审（第 2 / 2 节点）」；用普通管理员再通过应返回 403（该节点须超级管理员），用超级管理员通过后订单 `completed` 并计提；把节点 2 的「资金 / 争议节点」勾上再点「超时自动通过」应被拒（提示资金节点不能自动通过）。驳回：任一节点“复核驳回”后订单回待结单、审批进度转“已驳回”，再次“结单”应重建进度回到第 1 节点。超时：把节点超时设为 1 小时且勾选“超时自动通过”，把该订单的审批进度创建时间回拨 2 小时后跑 `esports_check_close_approval_timeout`，应自动通过该节点并推进；资金节点超时则只写 `close.approval_timeout` 外盒、不自动通过。
- 售后：进入“售后”菜单新建售后单（来源订单 ID + 类型 + 来源 + 诉求），同来源同问题已有进行中售后时创建失败。受理后提交方案（责任方 / 方案 / 赔付金额 / 处理说明），方案先进入**待客户确认**（§13.3）：此时直接审批应被拒（提示状态冲突）；客户自助调 `POST /api/v1/esports/user/after-sales/{id}/confirm` 后转待审批，调 `.../decline`（须填原因）则退回处理中、客服可改方案重新提交（再次进入待客户确认）；客户线下已同意时也可由客服点「代客户确认」（`POST /api/v1/esports/admin/after-sales/{id}/confirm`，权限码 `esports:aftersales:confirm`）：该入口只把单据从「待客户确认」推到「待审批」，**不写审批人、不预占退款额度、不产生资金动作**，代确认人也不会因此获得审批资格，审批仍须另一名员工走 `.../approve`。随后由**另一名员工**审批：`退原核销款` 仅支持原订单以客户余额核销的退款，审批通过即按 `refunded_cents + refund_reserved_cents + 本次 <= payable_cents + 加钟已收款`（§12.5-02 可退上界，加钟已收由只增流水求和派生）预占额度；执行后客户钱包新增 `refund_return`（退还本金 / 赠送，服务部分与原核销账户原路退回、加钟部分单独按 `source_type=after_sale_addon` 退回加钟收款账户）或 `adjust`（补偿权益）流水、订单 `refunded_cents` 累加且 `refund_status` 转 `partial`/`full`，重复执行失败。无余额核销流水或“补履约”方案执行时返回未接入提示（补履约见下条）。驳回 / 撤回进入终态。补履约：提交方案时选「补履约（补钟 / 换人）」，填承接陪玩 ID（须 `qualified`）、席位序号（须在 1..订单席位数内）、补足服务量与公司承担报酬；**带赔付金额、缺类型 / 缺陪玩 / 补钟量为零、陪玩未合格均应被拒**。由**另一名员工**审批通过后执行：应生成一条补履约任务，且订单 `refunded_cents` 与 `refund_reserved_cents` 均不变、无退款流水，订单事件为 `order.makeup_created`；同一售后单重复执行应失败。到页面下方「补履约任务」卡片：任务为“待服务” → 点“开始服务”转“服务中” → 点“验收”填说明转“已验收”（记录系统服务秒数与验收人）；重复开始、重复验收均应失败。限时与超时升级（§13.5）：系统配置 `app.esports.aftersale_accept_hours` / `app.esports.aftersale_plan_hours` / `app.esports.aftersale_customer_confirm_hours` / `app.esports.aftersale_execute_hours`（单位小时，`0` = 该阶段不限，缺失或非法按 2 / 24 / 48 / 24 回落）分别约束「待受理 / 处理中 / 待客户确认 / 待审批与待执行」；各阶段锚点为创建时间 / 受理时间 / 方案提交时间 / 审批时间（待审批时审批时间缺失则回落到方案提交时间）。把 `app.esports.aftersale_accept_hours` 设为 1、造一张 3 小时前创建的待受理单，跑 `esports_check_aftersale_sla` 应写一条 `aftersale.sla_breach` 事务外盒（含阶段、阈值与截止时间），**重复跑不重复升级**；把阈值放宽到 6 小时后再跑不再升级；售后列表「阶段时限」列应显示剩余分钟、超时标红（详情与列表均返回 `sla_limit_hours` / `sla_deadline` / `sla_overdue`）。陪玩追偿（§13.4）：提交方案时责任方选“陪玩”会多出「向陪玩追偿（元）」与「追偿陪玩 ID」，追偿金额超过赔付金额、责任方非陪玩或未填陪玩 ID 均应被拒；审批执行后到「结算 → 陪玩追偿」应看到一条 `待审批` 追偿单（金额 / 原因 / 来源售后）；点“审批”由**非申请人**通过后转“待扣回”，点“执行扣回”从该陪玩猫粮扣回同等金额（猫粮不足时返回中文错误、可待结算后再执行），重复执行失败；“驳回”须填原因且不得自审；陪玩端 `GET /api/v1/esports/user/player/recoveries` 可查看本人的追偿明细。
- 结算：进入“结算”菜单。先在“分成规则”新建草稿（全局 / 按游戏 / 按陪玩，按比例 / 每单固定 / 每单位固定），点“生效”切换为 `active`、原生效版本转 `history`；“回滚”以当前生效版本为模板生成更高版本并直接生效。结单后到“分成明细”按陪玩 / 订单 / 状态查询：应看到 `service` 正数明细（无命中规则或无服务成员时不生成）；对已结单订单发起并执行售后退款后，应出现同比例 `reversal` 负数明细。到“结算单”按陪玩 ID 生成结算单（填写周期开始 / 结束），应结金额为周期内待结算明细之和；点“锁定”后结算单转 `locked`、对应明细转 `settled` 并写入陪玩猫粮流水（金额为 0 时不写流水），重复锁定失败。
- 客服提成：进入“结算”菜单 → “客服提成规则”，新建草稿（适用范围选“全局默认”或“指定客服”；基数选报单金额 / 订单贡献 / 单量；计费选按比例或每单固定额；周期选日 / 周 / 月结）。**单量基数配按比例应被前端拦截、直接调接口也应返回参数无效**；阶梯阈值不严格递增、admin 范围不给 key 同样被拒。点“生效”后，用负责客服结单一张订单：到“客服提成明细”应看到 `订单提成` 明细，金额 = 基数 × 比例，且**不超过「报单金额 − 陪玩分成」**（超出时备注注明已截断）；同一周期内第二单的阶梯档位应按累计净基数上移。对该订单执行售后部分退款后应出现同比例负数 `退款冲回` 明细，全额退款后净额应为 0。到“结算单”点“生成客服提成结算单”（填客服 ID 与周期）：应结金额为周期内待结算提成之和、明细**只挂到结算单（状态仍为待结算）**，**且不产生猫粮钱包流水**；点“锁定”后出现“登记发放”，填外部打款凭证号后结算单转 `paid` 并记录打款人与凭证；未锁定就发放、或重复发放均应失败。到“报表 → 客服业绩”应看到该客服的“提成”列；客服工作台“工作台”页的业绩行也应含提成。自动生成：造两条待结算提成明细（一条落在上一周期、一条落在当前周期），跑 `esports_generate_cs_settlements` 后应只生成上一周期的**草稿**结算单，且该周期明细**仍为待结算（只挂了 `settlement_id`）**、当前周期明细保持待结算；重复跑不再生成；对该草稿「锁定」后明细仍待结算，只有「登记发放」成功后明细才转 `settled`；作废该单后同周期任务下一轮会重新出草稿。风控：进入「风险客户」菜单点“标记 / 修改”给某客户标记为“限制自助下单”并填原因，列表应显示 `限制自助下单` 与原因；该客户自助调 `POST /api/v1/esports/user/orders` 应返回「限制自助下单，请联系客服或先预付」且不产生订单；改为“疑似滥用复核”后自助下单不再被该标记拦截；点“解除”后风险名单应不再包含该客户。自动标记：给同一客户造 3 张「已完成且退款金额 > 0」的售后单，跑 `esports_flag_high_risk_customers` 应把其标记为“疑似滥用复核”并写 `customer.risk_flagged` 外盒，重复跑不重复标记、也不会把已“限制自助下单”的客户降级。
- 提现：先给陪玩结算一笔（见“结算”），使其猫粮余额非零。用该陪玩的登录用户身份调用陪玩端自助接口：`GET /api/v1/esports/user/player/dashboard` 应看到 `catfood_balance_cents` 等于可提现余额；`POST /api/v1/esports/user/player/withdrawals` 申请提现（金额、方式 `bank_card / wechat / alipay`、收款账号、8 位以上幂等键）应成功，**同一幂等键重复提交返回同一张提现单且猫粮只扣一次**；金额超过余额、金额低于 `app.esports.withdrawal_min_cents`、距上次申请不足 `app.esports.withdrawal_min_interval_hours`、收款账号为空、幂等键过短均应返回中文错误。申请后 `GET /api/v1/esports/user/player/withdrawals/summary` 的可提现余额应已扣减、待审金额增加。到管理端“结算 → 提现审核”：点“通过”后提现单转“已通过待打款”（点“登记打款”必须填外部打款凭证号，成功后转“已打款”并记录完税凭证、到账时间与打款人）；对另一张单点“驳回”必须填原因，成功后**申请金额退回陪玩猫粮且不重复退回**；重复审核、重复打款、未通过审核就打款、空打款凭证均应失败。打款前后猫粮余额不变（钱在申请时已离开猫粮）。
- 礼物：进入“礼物”菜单先在“礼物目录”新建礼物（填编号 / 名称 / 价格 / 分成方式）并点“上架”。到“送礼单据”点“代客送礼”，选上架礼物 + 收礼陪玩 ID：客户须已绑定登录身份、不得给本人送礼、须为成年、礼物须上架，高额档或超过阈值的大额礼物必须打开“大额二次确认”，付款来源仅为客户余额本金（余额不足提示中文错误）。送礼成功后客户余额减少、陪玩生成 `gift` 分成明细（并入“结算”一起结算）、客户成长值增加。同一「客户 → 陪玩」24 小时内第 6 笔会转“待复核”且暂不生成分成，复核“通过”后补建分成；对已付款单据“退款”会回退本金、冲回分成与成长值。到“礼物榜单”按日 / 周 / 月切换陪玩收礼榜与客户贡献榜，已退款不计入。
- 考核：进入“考核”菜单，先在“考核权重配置”选考核类型、填写六个维度权重（合计须 10000）并保存。到“考核任务”新建入职考核（陪玩 ID + 类型，目标等级默认新手），“分配考官”指定管理员 ID 与约定时间；用分配到的考官“打分”，逐维度填 0~100 分与评语（合计达标则任务转“通过”、陪玩被定级并开通接单，未达标转“不通过”且不改权限）；对 `spot_check` 抽查全打低分应触发降一档并暂停接单。陪玩端可对自己的已出结论考核发起申诉，管理员“复核申诉”选“申诉成立”应改判结论并重放定级。到“客户评价”用已完成订单的服务成员身份（客户自助接口）评价，同一订单同陪玩再次提交应只更新已有评价并追加修订历史；“客服代录”评价不计入评分；对评价“标记无效”后到“陪玩综合评分”查询应看到该评价被剔除、样本不足时显示提示。
- 报表：进入“报表”菜单，在“老板经营总览 / 客服个人业绩 / 派单效率 / 售后质量 / 财务资金与结算”五类 tab 间切换，用日 / 周 / 月与筛选维度（负责客服、游戏、即时/预约、单人/多人等）查询，核对订单量 / 营收 / 退款 / 净额与时间轴分段并下钻明细。用客服（非超级管理员）账号查询订单类报表应只看到本人负责 / 协作的订单，以及「负责客服为空但由本人报单」的兜底订单，售后质量只看本人处理的售后（响应 `data_scope=owner`）；`财务资金与结算` tab 仅超级管理员或命中 `app.esports.finance_report_role_names` 的账号可用，其余不显示该 tab 且直接调用 `finance` 返回 403，可用账号拿到的 `data_scope` 为 `full`。财务页签按 `account_type` / `entry_type` / `settlement_status` 筛选应真正生效（汇总与下钻明细同口径），传非法枚举值应 400 并点名「账户类型 / 流水类型 / 结算状态」；把另一类报表（如经营总览）传 `settlement_status` 应 400。派单效率传 `created_by_admin_id` 应 400（该维度已按需求删除），售后质量传 `responsible_admin_id` 应真正按处理客服收窄且**导出明细与页面条数一致**。
- 页面装修：进入“页面装修”菜单，在“装修”tab 从左侧组件库添加组件（上移 / 下移 / 显隐 / 复制 / 删除），选中组件在右侧属性面板编辑属性 JSON 并“应用属性”。“保存草稿”只影响后台预览，**不影响客户端**；删掉“客服与订单入口”组件或组件数与类型非法时保存 / 发布应失败。点“发布”生成不可变快照，到“发布记录”应看到该版本转 `已发布`、旧生效版本转 `历史版本`；再次发布后版本号递增。选一个历史版本“回滚”应生成**更高版本号**的新记录并记录回滚来源，旧记录不被改写。选未来时间“定时发布”后文档转 `待发布`，到点由 `esports_publish_scheduled_pages` 任务激活。客户端读取走公开 `GET /api/v1/esports/page?channel=uni_customer_home`：未装修时返回系统默认模板（版本号 0），发布后返回已发布配置与版本号，且响应不含任何个人 / 资金数据。素材库新增仅接受 `https://` 或站内 `/` 路径，非法协议被拒，且 **`mime` 与 `size_bytes` 必填**（体积 > 10 MiB 或非图片 `mime` 均被拒）。渠道白名单：把 `channel` 换成 `uni_player_home` 等非白名单渠道后，保存草稿 / 发布 / tabBar 读写 / 预览码 / 客户端读取都应返回参数错误并提示当前仅支持 `uni_customer_home`。图片白名单：把某组件的图片地址（或 `images` 数组里的一项）改成未登记进 `app.esports.page_allowed_domains` 的域名后，发布应被拒且**不发起任何网络请求**；登记进白名单后才能发布并只探测一次。
- 客服工作台：进入“客服工作台”菜单，“工作台”tab 应看到我的客户数、本人业绩（报单量 / 成交额 / 退款 / 净额，口径同 `cs_performance`）、按状态分桶的我的订单池与公共池摘要，以及待办提醒（待付款已超时、待结单、售后待处理、跟进已到期、今日生日客户）。到“我的客户”tab 点客户“档案”打开抽屉：核对汇总（累计充值 / 消费 / 退款 / 送礼 / 余额本金与赠送分列 / 成长值 / 订单数）、编辑手机号 / 生日（`MM-DD`，非法格式被拒）/ 消费偏好 / 标签（写入时应去空去重，超 20 个或单个超 30 字被拒）/ 跟进状态；把某客户生日改为今天（UTC+8）后工作台待办「今日生日客户」应 +1；记录一条跟进后 `最近联系` 应刷新，记录人固定为当前员工。到“企微自动建档”填企业主体 ID + 外部联系人标识：首次自动建档，**同一组合再次提交只返回既有档案**（提示已返回既有档案）且不重复建客户、不覆盖已维护的偏好与标签。到“话术库”tab 新建话术并编辑（版本不符返回冲突）、启停话术。用非超级管理员账号访问他人客户档案或跟进应返回 403。
- 储值卡：该菜单与开卡 / 核销 / 退卡 / 套餐 / 到期任务已于 2026-10-20 整体移除，**无人工检查步骤**；历史数据的可读性口径与存量导出见「储值卡功能移除（2026-10-20）」。扣减顺序：把系统配置 `app.esports.prepay_deduction_order` 设为 `principal_first` 后，用余额核销订单应**先扣本金**（本金不足才扣赠送）；缺省或设回 `bonus_first` 则先扣赠送；设为 `proportional` 则按本金 / 赠送剩余额比例分摊（本金向下取整、尾差归赠送）。批次分摊：先给客户余额充值，再报单并用余额核销；到“钱包”菜单对客户余额点“流水”，抽屉下方「批次分摊明细」应列出该笔消费（`order / 单号`）动用了哪个充值批次（`wallet_tx / 流水号`）及分摊金额。
- 优先派单权（独占优先通知）：先把系统配置 `app.esports.dispatch_priority_quota` 设为 2、`app.esports.dispatch_priority_notice_minutes` 设为 3。进入“排单”菜单下方的“优先派单权”卡片，授予某陪玩某游戏分区的优先派单权（周期选周结、获得方式选人工指定）：当期应直接“生效中”；**同一游戏当期再录入第三名应提示名额已满**，**同一陪玩同一期重复录入应提示已在本期名单中**；勾选“预生成下一期名单”再录入应得到“待生效”条目。点“移出名单”必须填原因，移出后状态转“已移出”且名额立即释放。跑 `esports_rebuild_dispatch_priority` 任务后：上一期名单应转“已过期”、到点的“待生效”应转“生效中”，并为下一期预生成“待生效”名单；重复跑不应重复生成。准备一张属于该游戏分区 SKU 的订单并核销到待派单：看板“优先通知”列应显示“通知中 · 至 <截止时间>”；点“通知明细”应只看到名单内若干陪玩、状态“待回应”。此时用**名单外**陪玩登录调 `GET /api/v1/esports/user/player/grab-hall` **不应看到该单**，直接 `POST .../orders/{id}/grab` 应返回 403 与「该订单正在优先通知名单内陪玩，暂未对大厅开放」；用**名单内**陪玩调 `GET /api/v1/esports/user/player/dispatch-notices` 应看到该单，`POST .../dispatch-notices/{id}/decline` 拒绝后订单仍在待派单（名单内还有人待回应），把名单内最后一人也拒绝后订单应转“抢单中”、看板“优先通知”列显示“已释放到大厅”、释放原因为「名单内全部拒绝」，此后**任意陪玩**在抢单大厅都能看到并可抢。另一单走超时路径：不做回应，跑 `esports_expire_dispatch_priority_notice` 任务后同样应释放到大厅（原因为「优先通知超时」）。名单内陪玩在窗口内直接 `grab` 接单应成功，订单转“已接单”、其通知转“已接单”、其余待回应通知作废。对窗口内的订单点“释放到大厅”可由客服提前放开。到“排单”看板对订单点“指派”后点“按规则排序”：窗口内候选应**只包含被通知的名单内陪玩**，窗口结束后候选为全部资质合格且在线陪玩（已占席位的除外）。
- 陪玩转单与催单：先把系统配置 `app.esports.dispatch_invite_ttl_minutes` 设为 60、`app.esports.remind_ladder_minutes` 设为 `5,15,30`、`app.esports.service_overtime_minutes` 设为 30。用陪玩登录对一张已接单（未开始服务）的订单调 `POST /api/v1/esports/user/player/orders/{id}/transfer`（填原因与 8 位以上幂等键）：应返回“待确认”；**重复同一幂等键应返回同一条申请**，换幂等键应返回「该席位已有待确认的转单申请」；**确认前**到“排单 → 席位”看该席位仍为 `accepted`、订单仍为已接单。用本人调 `POST .../transfer-requests/{id}/cancel` 可撤回；撤回后再确认应冲突。重新发起后用管理员到“排单 → 陪玩转单申请”点“通过”：席位应回到“待补位”（陪玩清空）、订单退回待接单；单席位订单应退回待派单，且若该游戏分区有优先名单会重新开优先通知窗口。点“驳回”必须填原因，驳回后席位与订单都不变；重复确认应失败。对已有服务段的订单发起转单应返回「已开始服务后不可转单，请走售后补履约」。催单阶梯：把邀请席位的到期时间回拨到「已过 20 分钟」（有效期按 60 分钟反推发出时间），跑 `esports_remind_dispatch_ladder` 任务应补发第 1、2 档提醒（`dispatch.remind_player` 与 `dispatch.remind_admin` 两条事务外盒 + 两条 `order.reminded` 事件）；**重复跑不应重复发**，时间推进到第 3 档后再跑只补发 1 条。服务超时：把某订单置为 `in_service` 并造一条 40 分钟前开始的服务段，跑 `esports_check_service_overtime` 应写一条 `dispatch.service_overtime` 与 `order.service_overtime`；把阈值改为 120 后重复跑不应再提醒。

对应接口为 `GET/POST /api/v1/esports/admin/skus`、`PUT /api/v1/esports/admin/skus/{id}`、`GET/POST /api/v1/esports/admin/categories`、`PUT/DELETE /api/v1/esports/admin/categories/{id}`、`PUT /api/v1/esports/admin/orders/{id}/quote`、`PUT /api/v1/esports/admin/orders/{id}/appointment`、`POST /api/v1/esports/admin/orders/{id}/cancel`、`GET /api/v1/esports/admin/orders/{id}/cancel-preview`、`POST /api/v1/esports/admin/orders/{id}/pay-by-balance`、`POST /api/v1/esports/admin/orders/{id}/deposit`、`POST /api/v1/esports/admin/orders/{id}/restore`、`GET /api/v1/esports/admin/wallets`、`GET /api/v1/esports/admin/wallets/{id}/transactions`、`POST /api/v1/esports/admin/wallets/{id}/recharge`、`POST /api/v1/esports/admin/wallets/{id}/adjust`、`POST /api/v1/esports/admin/customers/{id}/age-checks`、`GET /api/v1/esports/admin/age-checks`、`POST /api/v1/esports/admin/age-checks/{id}/review`、`GET /api/v1/esports/admin/dispatch/board`、`GET /api/v1/esports/admin/orders/{id}/seats`、`POST /api/v1/esports/admin/orders/{id}/seats/{seat_index}/assign`、`POST /api/v1/esports/admin/orders/{id}/seats/{seat_index}/revoke`、`POST /api/v1/esports/admin/orders/{id}/claim`、`POST /api/v1/esports/admin/orders/{id}/transfer`、`POST /api/v1/esports/admin/orders/{id}/remind`、`GET/POST /api/v1/esports/admin/orders/{id}/addons`、`POST /api/v1/esports/admin/order-addons/{id}/cancel`、`POST /api/v1/esports/admin/order-addons/{id}/pay-by-balance`。结单与售后接口为 `POST /api/v1/esports/admin/orders/{id}/close`、`POST /api/v1/esports/admin/orders/{id}/close-approve`、`POST /api/v1/esports/admin/orders/{id}/close-reject`、`GET/POST /api/v1/esports/admin/after-sales`、`GET /api/v1/esports/admin/after-sales/{id}`、`POST /api/v1/esports/admin/after-sales/{id}/accept`、`POST /api/v1/esports/admin/after-sales/{id}/plan`、`POST /api/v1/esports/admin/after-sales/{id}/approve`、`POST /api/v1/esports/admin/after-sales/{id}/reject`、`POST /api/v1/esports/admin/after-sales/{id}/execute`、`POST /api/v1/esports/admin/after-sales/{id}/withdraw`。待客户确认（§13.3）接口管理端为 `POST /api/v1/esports/admin/after-sales/{id}/confirm`（客服代确认）；客户自助为 `POST /api/v1/esports/user/after-sales/{id}/confirm`、`POST /api/v1/esports/user/after-sales/{id}/decline`。陪玩追偿（§13.4）接口管理端为 `GET /api/v1/esports/admin/player-recoveries`、`POST /api/v1/esports/admin/player-recoveries/{id}/approve`、`POST /api/v1/esports/admin/player-recoveries/{id}/reject`、`POST /api/v1/esports/admin/player-recoveries/{id}/execute`；陪玩端为 `GET /api/v1/esports/user/player/recoveries`。客户风控（§13.7）接口管理端为 `GET /api/v1/esports/admin/risk-customers`、`POST /api/v1/esports/admin/customers/{id}/risk`。客户自助端（§13.2 / §10，供 uni-app 客户端调用，均为 `AuthUser`）接口为 `POST /user/orders`（自助下单）、`GET /user/orders`（我的订单，`bucket` 分组）、`GET /user/orders/{id}`（我的订单详情）、`GET /user/aftersales`（我的售后）、`POST /user/aftersales`（自助发起售后）、`GET /user/wallets`（余额本金 / 赠送）、`GET /user/profile`（会员等级 / 成长值 / 风险只读）。结单表单设计器（§3.4）接口为 `GET/POST /api/v1/esports/admin/close-form-fields`、`PUT /api/v1/esports/admin/close-form-fields/{id}`、`POST /api/v1/esports/admin/close-form-fields/{id}/status`、`GET/POST /api/v1/esports/admin/close-form-templates`、`PUT /api/v1/esports/admin/close-form-templates/{id}`、`POST /api/v1/esports/admin/close-form-templates/{id}/activate`、`POST /api/v1/esports/admin/close-form-templates/{id}/rollback`、`GET /api/v1/esports/admin/orders/{id}/close-form`；结单提交规则（§3.4）接口为 `GET/POST /api/v1/esports/admin/close-submit-rules`、`PUT /api/v1/esports/admin/close-submit-rules/{id}`、`POST /api/v1/esports/admin/close-submit-rules/{id}/activate`、`POST /api/v1/esports/admin/close-submit-rules/{id}/rollback`；结单审批流（§3.4）接口为 `GET/POST /api/v1/esports/admin/close-approval-flows`、`PUT /api/v1/esports/admin/close-approval-flows/{id}`、`POST /api/v1/esports/admin/close-approval-flows/{id}/activate`、`POST /api/v1/esports/admin/close-approval-flows/{id}/rollback`、`GET /api/v1/esports/admin/orders/{id}/close-approval`。补履约任务（§13.6）接口为 `GET /api/v1/esports/admin/fulfillment-makeups`、`POST /api/v1/esports/admin/fulfillment-makeups/{id}/start`、`POST /api/v1/esports/admin/fulfillment-makeups/{id}/complete`。结单规则（§3.4）接口为 `GET/POST /api/v1/esports/admin/close-rules`、`PUT /api/v1/esports/admin/close-rules/{id}`、`POST /api/v1/esports/admin/close-rules/{id}/activate`、`POST /api/v1/esports/admin/close-rules/{id}/rollback`。结算接口为 `GET/POST /api/v1/esports/admin/commission-rules`、`POST /api/v1/esports/admin/commission-rules/{id}/activate`、`POST /api/v1/esports/admin/commission-rules/{id}/rollback`、`GET /api/v1/esports/admin/commission-entries`、`GET/POST /api/v1/esports/admin/settlements`、`POST /api/v1/esports/admin/settlements/{id}/lock`、`GET /api/v1/esports/admin/settlements/{id}`；陪玩端为 `GET /api/v1/esports/user/player/commission`、`GET /api/v1/esports/user/player/commission/summary`。陪玩模块管理端接口为 `GET/POST /api/v1/esports/admin/players`、`PUT /api/v1/esports/admin/players/{id}`；陪玩端自助接口为 `GET /api/v1/esports/user/player/dashboard`、`POST /api/v1/esports/user/player/presence`、`GET /api/v1/esports/user/player/invites`、`POST /api/v1/esports/user/player/invites/{id}/accept`、`POST /api/v1/esports/user/player/invites/{id}/reject`、`GET /api/v1/esports/user/player/grab-hall`、`POST /api/v1/esports/user/player/orders/{id}/grab`、`GET /api/v1/esports/user/player/orders`、`POST /api/v1/esports/user/player/orders/{id}/service/start`、`POST /api/v1/esports/user/player/orders/{id}/service/finish`（供 uni-app 客户端使用）。陪玩端保活（§6.4）为 `POST /api/v1/esports/user/player/heartbeat`。陪玩端服务售后申诉（§13.2）为 `POST /api/v1/esports/user/player/order-after-sales`、`GET /api/v1/esports/user/player/order-after-sales`。陪玩端加钟（§6.2）接口为 `GET /api/v1/esports/user/player/addons`、`POST /api/v1/esports/user/player/addons/{id}/confirm`、`POST /api/v1/esports/user/player/addons/{id}/reject`。。充值活动接口为 `GET/POST /api/v1/esports/admin/campaigns`、`PUT /api/v1/esports/admin/campaigns/{id}`、`POST /api/v1/esports/admin/campaigns/{id}/status`、`POST /api/v1/esports/admin/wallets/{id}/recharge-campaigns`。会员等级接口为 `GET/POST /api/v1/esports/admin/member-levels`、`PUT /api/v1/esports/admin/member-levels/{id}`、`POST /api/v1/esports/admin/member-levels/{id}/status`、`GET /api/v1/esports/admin/customers/{id}/member`、`POST /api/v1/esports/admin/customers/{id}/member/level`。礼物接口为 `GET/POST /api/v1/esports/admin/gifts`、`PUT /api/v1/esports/admin/gifts/{id}`、`POST /api/v1/esports/admin/gifts/{id}/status`、`GET/POST /api/v1/esports/admin/gift-orders`、`POST /api/v1/esports/admin/gift-orders/{id}/refund`、`POST /api/v1/esports/admin/gift-orders/{id}/review`、`GET /api/v1/esports/admin/gift-leaderboard`。考官端接口为 `GET/POST /api/v1/esports/admin/assessment-rules`、`GET/POST /api/v1/esports/admin/assessments`、`GET /api/v1/esports/admin/assessments/{id}`、`POST /api/v1/esports/admin/assessments/{id}/assign`、`POST /api/v1/esports/admin/assessments/{id}/score`、`POST /api/v1/esports/admin/assessments/{id}/appeal/review`、`GET/POST /api/v1/esports/admin/player-reviews`、`POST /api/v1/esports/admin/player-reviews/{id}/status`、`GET /api/v1/esports/admin/players/{id}/rating`；陪玩端为 `GET /api/v1/esports/user/player/assessments`、`POST /api/v1/esports/user/player/assessments/{id}/appeal`；客户自助为 `POST /api/v1/esports/user/orders/{id}/reviews`。报表接口为 `GET /api/v1/esports/admin/reports/{type}`（`:type` 为 `owner_overview` / `cs_performance` / `dispatch_efficiency` / `aftersale_quality` / `finance`）。页面装修接口为 `GET /api/v1/esports/admin/pages`、`GET /api/v1/esports/admin/pages/{channel}`、`PUT /api/v1/esports/admin/pages/{channel}/draft`、`POST /api/v1/esports/admin/pages/{channel}/publish`、`POST /api/v1/esports/admin/pages/{channel}/rollback`、`GET /api/v1/esports/admin/pages/{channel}/versions`、`GET/POST /api/v1/esports/admin/page-assets`、`DELETE /api/v1/esports/admin/page-assets/{id}`，客户端读取为公开 `GET /api/v1/esports/page`（仅返回已发布配置，无个人数据）。客服端（§8）接口为 `GET /api/v1/esports/admin/cs/workbench`、`GET/PUT /api/v1/esports/admin/customers/{id}/profile`、`GET/POST /api/v1/esports/admin/customers/{id}/follow-ups`、`POST /api/v1/esports/admin/customers/wecom-intake`、`GET/POST /api/v1/esports/admin/scripts`、`PUT /api/v1/esports/admin/scripts/{id}`、`POST /api/v1/esports/admin/scripts/{id}/status`，PC 工作台 / 移动客服端 / 企微侧边栏三个入口共用这一套接口。**储值卡（§4.4）相关接口已随 2026-10-20 功能移除全部删除**（`/admin/stored-value-cards*`、`/admin/stored-value-packages*`、`/admin/orders/{id}/pay-by-stored-value-card`、`/user/stored-value-cards*`）；订单支付方式只剩两种——客户余额核销 `POST /api/v1/esports/admin/orders/{id}/pay-by-balance` 与 `POST /api/v1/esports/user/orders/{id}/pay-by-balance`，外部渠道走到账凭证核验与在线收银台。客服提成（§12.2）接口为 `GET/POST /api/v1/esports/admin/cs-commission-rules`、`POST /api/v1/esports/admin/cs-commission-rules/{id}/activate`、`POST /api/v1/esports/admin/cs-commission-rules/{id}/rollback`、`GET /api/v1/esports/admin/cs-commission-entries`、`GET /api/v1/esports/admin/cs-commission/summary`、`POST /api/v1/esports/admin/cs-settlements`、`POST /api/v1/esports/admin/settlements/{id}/pay`。提现（§6.3 / §12.6）接口管理端为 `GET /api/v1/esports/admin/withdrawals`、`GET /api/v1/esports/admin/withdrawals/{id}`、`POST /api/v1/esports/admin/withdrawals/{id}/review`、`POST /api/v1/esports/admin/withdrawals/{id}/pay`；陪玩端为 `POST /api/v1/esports/user/player/withdrawals`、`GET /api/v1/esports/user/player/withdrawals`、`GET /api/v1/esports/user/player/withdrawals/summary`。优先派单权与优先通知（§5.4 / §5.5）接口管理端为 `GET/POST /api/v1/esports/admin/dispatch/priority`、`POST /api/v1/esports/admin/dispatch/priority/{id}/revoke`、`GET /api/v1/esports/admin/orders/{id}/dispatch-candidates`、`GET /api/v1/esports/admin/orders/{id}/priority-notices`、`POST /api/v1/esports/admin/orders/{id}/release-priority`、`POST /api/v1/esports/admin/orders/{id}/publish-grab`；陪玩端为 `GET /api/v1/esports/user/player/dispatch-notices`、`POST /api/v1/esports/user/player/dispatch-notices/{id}/decline`（接受复用既有 `POST /api/v1/esports/user/player/orders/{id}/grab`）。陪玩转单（§5.7）接口管理端为 `GET /api/v1/esports/admin/seat-transfers`、`POST /api/v1/esports/admin/seat-transfers/{id}/decision`；陪玩端为 `POST /api/v1/esports/user/player/orders/{id}/transfer`、`GET /api/v1/esports/user/player/transfer-requests`、`POST /api/v1/esports/user/player/transfer-requests/{id}/cancel`。这组开发入口不提供真实支付能力。

本轮（2026-10）新增或变更的接口路径（均相对 `/api/v1/esports`）：结算单作废 / 重开 `POST /admin/cs-settlements/{id}/void`、`POST /admin/cs-settlements/reopen`；结算单列表状态过滤 `GET /admin/settlements?status=draft|locked|paid|void|all`；会员优先预约处理 `POST /admin/priority-bookings/{id}/process`；结单表单读取 `GET /admin/orders/{id}/close-form`（含 `evidence_attachment_ids`）、结单条件 `GET /admin/orders/{id}/close-conditions`；商品与规格组 `GET/POST /admin/products`、`GET/PUT /admin/products/{id}`、`GET/POST /admin/spec-groups`、`PUT /admin/spec-groups/{id}`、`GET/POST /admin/skus`、`PUT /admin/skus/{id}`；收款账户与收款方式 `GET/POST /admin/payment-accounts`、`POST /admin/payment-accounts/{id}/review`、`POST /admin/payment-accounts/{id}/disable`、`GET /admin/payment-channels`、`POST /admin/payment-channels/{channel}/status`；历史订单补录 `POST /admin/orders/backfill`（须带 `Idempotency-Key`）；外部退款待执行单 `GET/POST /admin/external-refunds`、`POST /admin/external-refunds/{id}/execute`。

2026-10-19 后端缺口补齐批新增的接口路径（均相对 `/api/v1/esports`）：`POST /admin/after-sales/{id}/confirm`（代客确认，`esports:aftersales:confirm`）、`GET /admin/orders/{id}/appointment/disposal-options`（`esports:orders:appointment_disposal_options`）、`GET /admin/orders/{id}/players/{playerId}/service/timing`（`esports:orders:service_timing`）、`GET /admin/recharge-refund-executions?status=&limit=`（`esports:settlements:recharge_refund_executions`）；用户侧 `GET /user/reviews`、`GET /user/reschedule-proposals`、`GET /user/player/reschedule-proposals`（均为 `AuthUser`，不声明 PermAPI）。口径见「售后代客确认与三条只读接口」一节。

2026-10-19 清零轮新增的接口路径（均相对 `/api/v1/esports`）：`POST /admin/cs-commission-entries/{id}/freeze`、`POST /admin/cs-commission-entries/{id}/unfreeze`（`esports:settlements:cs_entry_freeze` / `cs_entry_unfreeze`）、`DELETE /admin/member-levels/{id}`（`esports:members:level_delete`）。配套的定时任务与动作项新状态见「售后执行与退原核销款口径」一节。

**存量数据修复命令（2026-10-19 新增 `cmd/esports-data-repair`）**：在 `go-admin` 目录执行 `go run ./cmd/esports-data-repair --dry-run`（默认就是 dry-run，只统计并打印样例、不写库）；确认样例与补齐值后再用 `go run ./cmd/esports-data-repair --apply --per-seat-units N`（`--per-seat-units` 必须显式传入正整数，不传则拒绝执行；只写 `--dry-run=false` 也会被拒，避免误以为已修复）。三类历史数据里**只有「多人 SKU 缺每席承诺服务量」会自动补齐**（按 `id + 原值` 条件更新并抬版本，可重复执行）；`required_level` 全 novice 与「名单 game 与分类不一致」只输出统计与样例、不自动改（真实等级与哪一侧写错都无法从历史数据推断）。

## 生成与验证

从 `go-admin` 目录依次执行：

```text
go generate ./internal/apps
go generate ./internal/ent
go test ./internal/apps/esports ./internal/apps
go test ./...
```

完整 `go test ./...` 需要先构建管理端并将实际产物放入 `go-admin/web/dist`，以满足主程序的 `embed` 声明。生产迁移须先备份、在隔离库演练并审查生成的 DDL；启动建表已限制为非破坏性增量操作。

## 代客送礼确认流程（2026-10）

客服先通过 `POST /admin/gift-intents` 创建 30 分钟有效的明细快照；客户通过 `GET /user/gift-intents` 查看礼物、收礼人、数量、单价、总价与付款来源，并以已验证账号调用 `POST /user/gift-intents/{id}/confirm` 明确确认。客服随后只用 `intent_id` 调用 `POST /admin/gift-orders`。服务端在扣余额本金的同一事务中校验并消费确认单，礼物版本、价格或收礼人变化会拒绝执行；原有 `confirmed` 布尔值不再被管理接口接受。管理端列表与退款按客户当前归属隔离，疑似礼物复核不得由业绩归属客服自审。已实现部分退款与独立争议：客户或客服可发起礼物争议单（`POST /user/gift-disputes` / `POST /admin/gift-disputes`），经审核（申请人不得自审）后由异人执行退款，支持部分退款，见 `gift_dispute.go`（§9.5）。

## 关联资源权限与售后客户确认（2026-10）

客服访问礼物、售后、客户余额钱包与加钟独立入口时按当前归属客户或订单过滤。客服建售后单会自动绑定来源订单客户；把售后单从「待客户确认」推进到「待审批」有两条合法路径——客户在自助端以已验证账号确认（`POST /user/after-sales/{id}/confirm`），或客户线下已同意时由客服代确认（`POST /admin/after-sales/{id}/confirm`，2026-10-19 起以独立路由提供，只做状态推进、不等于审批，见下文「售后代客确认与三条只读接口」）；代确认与审批都只是流程节点，审批仍须另一名员工执行。跨客服审批和资金操作仍需按角色权限配置，现阶段不可视为完整生产权限验收。

## 人工调价底价与超限审批（2026-10）

商品 SKU 增加每份最低议价及人工调整免审幅度（基点），报单时快照到订单。改价预览与执行均使用订单价格和客户当前会员权益重新计算报价；人工议价后的标价小计不得低于订单快照底价 × 份数，会员折扣独立计算。超出免审幅度时，负责客服提交包含订单版本、份数、调整金额、应付和原因的审批单，由非申请人的超级管理员复核；审批通过后申请人才能按完全相同的报价应用，应用与审批单消费同事务。改价作废旧支付意图并抬升报价版本，客户须重新确认。已下架 SKU 不再生成未付款新报价。接口为 `POST /admin/orders/{id}/quote-preview`、`POST /admin/orders/{id}/price-adjustments`、`GET /admin/price-adjustments`、`POST /admin/price-adjustments/{id}/decision`、`PUT /admin/orders/{id}/quote`。尚需人工端到端验收和权限角色配置。

## 储值卡本金送礼（2026-10，已随储值卡功能移除）

**该能力已于 2026-10-20 随储值卡功能整体下线**：开卡合同的 `allow_gifts`、礼物确认单的「已授权储值卡本金」付款来源、卡内本金扣减与储值卡预付款批次分摊均已删除。礼物付款来源现在只有两条——「客户余额本金」（即时扣减）与「外部收款」（`external_*`，走收款核销），赠送权益仍不能买礼物，退款仍按原付款来源返还本金。历史储值卡支付的礼物单以数据库中的 `stored_value_card_id` / `payment_source` 只读留存（这两列是否随迁移 DROP 见上节「DROP 的表与列」）。

## 分成与提成规则锁定（2026-10）

陪玩接受邀请或抢单时，逐成员锁定当时适用的分成规则；无规则也锁定空值，后续新规则不追溯旧单。开工时把锁定结果写入本人服务段，改派也不会覆盖旧成员约定。结单时把扣除结单前退款的服务应付按成员有效服务秒数分配，再分别应用其规则，分成合计不超过净服务应付减已知渠道成本。加钟确认也按确认人本人规则计算。客服提成在余额或已验签外部付款核销时锁定业绩客服与规则，结单时使用该快照。历史订单若没有锁定字段，则仍按结单时规则兼容计算，正式启用前需处理历史数据。单笔手动指定、陪玩等级优先级及按具体争议成员冲回尚未完成。

## 加钟分成上限与营收口径（2026-10）

加钟分成原先只受「结单侧三项合计 ≤ 可分基数」约束，固定额 / 每单位固定额规则可以算出**超过本次加钟本金**的分成，结单侧把服务分成压到 0 也补不回来（工作室透支）。现在加两条口径：

- **单笔加钟分成 ≤ 本次加钟本金**：`addonCommissionCents` 在原值上夹到 `[0, added_cents]`（`percent` 天然不超本金，只有两种固定额模式会命中）；超限**截断而非拒绝**，否则固定额规则下陪玩将完全无法确认加钟。截断事实写进 `order.addon_confirmed` 事件（`commission_capped=true` + `commission_uncapped_cents=<原值>`），加钟行快照 `commission_cents` 即截断后的值。
- **计提二次收紧**：`accrueAddonCommissionOnPayment` 在写入 `kind=addon` 明细时再按本金截断一次（兜住历史超额快照），被截断时明细备注追加「已按加钟本金上限截断」，`order.addon_paid` 事件的 `commission_cents` 记**实际计提额**。本金为 0 的加钟（只追加工时、不追加金额）不产生加钟分成。
- **加钟收入并入营收口径**：加钟收入继续并入订单可分基数（§12.1 的上限因此才成立），报表与结单快照一律按**已收款**计（`EsportsOrderAddon.payment_status=paid` 的 `paid_cents`）——未确认 / 未收款 / 已拒绝 / 已取消的加钟既不计收入也不产生分成，收入与成本时点对称。`owner_overview` 与 `cs_performance` 的 `revenue_cents` / `net_cents` 已含加钟并单列 `addon_revenue_cents`；结单快照（`order.closed` 事件 details）新增 `addon_revenue_cents`（加钟实收）、`addon_commission_cents`（对应加钟分成）与 `close_net_total_cents`（本体净额 + 加钟实收，与报表 `net_cents` 同口径、不扣分成），`close_net_service_cents` 的语义仍是「订单本体净服务额」，未被改写。
- 限制：渠道成本是订单级字段，不逐笔分摊到加钟，因此「三项合计 ≤ 加钟本金」由「订单可分基数 + 单笔加钟分成 ≤ 本金」两条联合保证；结单快照是**结单当时**的时点值，结单后的加钟退款与售后退款不回写快照，跨期净额仍以报表动态汇总为准。

## 结单模板与退款冲回快照（2026-10）

新订单在下单时锁定适用的结单表单模板，包含当时无模板的情况；模板生效时固定字段类型、选项、必填设置和版本，后续发布模板或编辑字段库不会改变旧单的结单要求。结单时另保存未退款的净服务应付额；结单后服务分成和客服提成按该额冲回，退款执行会读取事务内更新后的累计退款额。历史无模板/字段/净服务额快照的订单仍使用兼容逻辑，正式启用前需迁移或审计。

客户自助发起已完成或已取消服务订单的售后时，申请窗口读取 `app.esports.customer_aftersale_window_days`，缺省 30 天，配置 0 为不限时；在途订单仍可即时申诉。该窗口不限制客服代客户建单。礼物及充值来源接入统一售后后仍需补同类校验（储值卡来源已随 2026-10-20 功能移除，历史单据只读）。

## 邀请容量占位（2026-10）

指派与固排成队时，待确认且未过期的邀请临时占用陪玩并发及当日接单容量；接受邀请时排除本席位并再次校验。拒绝、撤回、过期、服务完成或订单取消后不再占并发容量，历史已接单席位仍计入接单当日上限。写入前对陪玩版本做条件更新，避免两个订单同时基于旧容量通过。按预约服务时段的重叠判定与服务开始自然日口径仍待补齐。

## 已付款取消政策与退款闭环（2026-10）

取消政策是工作室公示的两项配置：`app.esports.cancel_free_hours`（付款后免费取消窗口，`0` 表示不提供免费窗口）与 `app.esports.cancel_fee_bp`（超出窗口后按应付金额收取的手续费基点，`0～10000`）。两项缺失时回落「无免费窗口且不收费」，即未取得业务签收前不擅自扣客户款；配置存在但非法（小时超出 0～720、基点超出 10000）时试算直接报参数错误，不用错误费率算钱。

`GET /admin/orders/{id}/cancel-preview` 给出完整试算：计时锚点（优先 `paid_at`，历史缺付款时间回落创建时间）、免费截止时间、是否免费、应付 / 已退 / 已预占、手续费与应退金额，以及不可取消原因。取消计入时点只在**付款后**：`POST /admin/orders/{id}/cancel` 对已付款订单在同一事务内按政策算退款、把履约状态置 `cancelled`、名额占用记录由 `sold` 转 `returned` 并同步扣减 SKU `quota_sold`、作废该单未终结的派单席位与待支付意图，最后生成一张 `kind=refund_request` 的售后单——退款从此走既有的售后审批与执行流程，不再停留在只改状态。政策为「全额收取手续费」（应退为 0）时不建售后单、也不置退款状态。

已开工订单（已存在服务段）不允许直接取消，返回「订单已开始服务，请通过售后流程处理退款」；待付款未付款的取消口径不变（直接释放名额与支付意图）。退款金额只按订单 `payable_cents` 与已退 / 已预占额度计算，加钟金额不在取消政策范围内。

## 服务时长严重差异与陪玩售后申诉（2026-10）

服务结束时除原有「差异写事件 + 客服待办」外，差异超过 `app.esports.service_duration_severe_minutes`（默认 30 分钟，缺失或非法回落默认值）时，系统在同一事务内自动建立一张 `channel=system`、`kind=duration_short` 的售后单，并把来源陪玩（`player_id`）与该成员的服务段序号写进单据，让客服在统一售后状态机内核实与处置；同订单同问题已有进行中售后时跳过，服务结束不会因为重复建单失败。

陪玩端新增自助申诉：`POST /user/player/order-after-sales`（须为本单服务成员，即有派单席位或服务段），落 `channel=player` 与来源陪玩，只接受服务履约类问题（`service_mismatch / duration_short / no_show / wrong_item / overcharge / player_appeal / other`），退款金额由客服在售后方案中确定，陪玩不能直接指定；`GET /user/player/order-after-sales` 查本人相关售后（含系统按本人服务段自动建的单）。陪玩端页面入口与推送渠道仍未接入。

## 心跳超时自动离线与在途订单保护（2026-10）

陪玩档案新增 `last_heartbeat_at`。陪玩端 `POST /user/player/heartbeat` 只刷新心跳时戳、**不改变在线状态**，客户端不会因为一次保活被误置上线；陪玩自助上线与管理端改档为在线时都会写入心跳基线，超时任务才有判断失联的依据。

`esports_auto_offline_players` 任务按 `app.esports.player_heartbeat_timeout_minutes`（默认 10 分钟，缺失或非法回落默认值）处理：只挑「在线且已有心跳基线」的陪玩，无心跳记录的存量档案不动（避免升级后一次性把在线陪玩全部离线）。若该陪玩在待接单 / 已接单 / 服务中订单上持有已接单席位，说明存在在途履约，此时**不置离线**而是转 `protected_abnormal` 并写 `player.heartbeat_timeout` 外盒提醒客服核实——`protected_abnormal` 一律**不具备派单资格**（不进派单候选池、不可被指派、不可接受邀请或抢单），在途订单只保留履约与结算，客服需通过「恢复在线 / 重新心跳」把人放回候选池；其余置 `offline`。外盒幂等键含心跳时戳，同一次失联只提醒一次，重新上线后再次失联才会再提醒。

## 终态售后新证据复核（2026-10）

已驳回 / 已完成的售后单不再只能另建一张不同问题单：`POST /admin/after-sales/{id}/reviews` 提交复核申请（`reason` 必填、`evidence_ref` 存新证据引用），`GET /admin/after-sales/{id}/reviews` 查看复核历史，`POST /admin/after-sale-reviews/{id}/decision` 由异人审查（`decision` 取 `uphold` 维持原结论或 `reopen` 发回重新处理），`POST /admin/after-sale-reviews/{id}/withdraw` 由申请人撤回待审查申请。状态机为 `pending → upheld | reopened | withdrawn`。

约束：只有终态售后可申请，同一售后单同一时刻只允许一张进行中复核（撤回后可再申请）；审查人不得是申请人，也不得是原审批人——发回重处理会推翻原审批结论，由原审批人自己审查等于自审。`reopen` 会把售后单由终态置回 `processing`、清空结案时间、重新冻结该订单待结算分成，之后可重新提交方案、重走客户确认与审批；若该订单的分成已结算或已纳入结算单，改判无法把退款冲回已结算分成，此时直接拒绝并提示转人工财务处理（复核申请与售后单状态均保持不变）。全流程写 `order.after_sale_review_requested` / `order.after_sale_review_decided` 订单事件与 `aftersale.review_requested` 外盒。

人工检查：把一张售后单驳回到 `rejected`，用处理客服提交复核申请，再用同一账号审查应返回 403；换第三个管理员选 `reopen` 后，该售后单回到「处理中」且可重新提交方案；对分成已结算的订单重复上述「发回重处理」应被拒并提示已结算。

## 售后执行与退原核销款口径（2026-10）

- **执行失败 / 撤回必须释放预占额度**：单动作售后落 `execution_failed`（无法再执行）时在**同一事务内**释放它仍占用的 `refund_reserved_cents`；多动作售后失败时保留预占（人工可续跑），`POST /admin/after-sales/{id}/withdraw` 只释放「未成功的退款动作项」的占用。释放额按 `afterSaleHeldRefundReserveCents`（单动作为方案退款额、多动作为未成功动作项之和）与当前预占取小，用条件表达式收口，**幂等且永不释放成负数**；`refunded_cents` 全程不变。兼容存量已落 `execution_failed` 的历史单。
- **历史原核销账户已下线时的改道（2026-10-20 后）**：原核销流水若来自已下线的储值卡账户（`account_type=stored_value_card`），卡表已 DROP、卡一律视为不可用，退款一律改写客户余额的 `refund_return`（备注「售后退款（原储值卡已下线，退回客户余额）」，本金 / 赠送按原核销构成拆分，退款总额与订单 `refunded_cents` 一致）；客户余额账户不可入账时返回 `execution_failed` 并写中文原因，交人工处置（未新增 `manual_required` 枚举）。
- **外部渠道「退原核销款」改为登记待执行外部退款单**：微信 / 商户码 / 对公转账且订单**没有**余额核销流水（含历史储值卡核销流水）时，售后执行只生成一张 `registered` 的 `EsportsExternalRefund`（复用 `externalRefundNo()`、关联 `aftersale_id`、幂等键 `transaction_no = AS-EXT-<售后单号>`，多动作方案为 `AS-EXT-<售后单号>:a<序号>`），写 `order.refund_external_registered` 事件，并把「已生成外部渠道退款待执行：退款单 ER…（渠道 …），须由财务核验后执行」写进方案 `resolution`；**不写钱包、不累加 `refunded_cents`**，预占额度保留到财务执行外部退款那一刻才扣预占、累加已退并同事务冲回分成 / 客服提成 / 成长值。同一动作项重试复用原单，同键已是 `executed` 则返回冲突（不得重复放款）；自动登记的 `registered_by_admin_id` 是售后执行人，因此财务执行外部退款必须换人。
- **动作项新增 `awaiting_execution`（2026-10-19，`after_sale_action.status` 的第 6 个状态）**：多动作方案的退款动作项走外部渠道时落 `awaiting_execution`（登记待执行），**不再用 `succeeded` + 摘要伪装成已执行**；此时钱未出账，所以售后单停在 `pending_execution`（既不判 `execution_failed`、也不直接结案），预占 `refund_reserved_cents` 继续保留；财务执行该外部退款成功后，动作项 `awaiting_execution → succeeded` 并与售后结案同事务提交（失败整体回滚，不会出现「钱已出账、动作项还挂待执行」）。批量执行会跳过待执行项（不重复登记、不当失败），单序号人工重试对 `awaiting_execution` 返回 409。进度聚合把 `pending / executing / awaiting_execution` 统一计入「待执行」、中文标签为「待执行（外部退款）」——**待执行不算失败**。单动作售后（没有动作项）仍是「登记即结案」，两者口径差异已在注释中标明。
- **待执行外部退款超期督办（2026-10-19 新增任务）**：定时任务 `esports_expire_pending_external_refunds`（cron `0 15 * * * *`，每小时 15 分）扫描 `status=registered` 且登记时间早于 `now - 24h` 的退款单（阈值是代码内常量 `externalRefundOverdueHours = 24`，不新增系统配置项；单轮上限 200 条），写一条 `external_refund.overdue` 事务外盒督办（`event_key` 含退款单号 + 小时窗口，小时级幂等），投递给财务角色成员、无财务成员时回落启用中的超管，企微通道 `wecom_app`。**只督办不改单**：不自动执行、不自动取消，退款单状态 / 金额 / 执行人与订单额度一概不动。注意 `GET /admin/external-refunds` 的响应里**还没有** `overdue` 标记（判定函数 `externalRefundOverdue` 目前只有任务在用），列表页要展示需由 handler 侧补 1 行。
- **`payment_status=exception` 的订单禁止退款审批与执行**：仅当本次「退原核销款」会占用原单退款额度（`afterSaleRefundReserveCents > 0`）时，审批、执行两个入口与外部退款执行侧都校验订单支付状态，命中即 409 中文「订单支付状态异常（渠道撤销 / 拒付），禁止执行退款，请先人工处置支付异常」；补偿 / 补履约不涉及原支付款回退，不受此限。`unpaid` 未纳入拦截，保持既有「未付款单也能走售后方案」的口径。
- 遗留：外部退款待执行期间预占不释放（钱未出账不得释放），这是刻意口径；超期提醒已由任务 `esports_expire_pending_external_refunds` 承担，「已超期」字段尚未进列表响应（见上条）。

## 售后代客确认与三条只读接口（2026-10-19 后端缺口补齐）

本节对应审核报告 §3.1 / §3.3 / §4.5 / §5.5 点出的「后端缺读接口」缺口：四条管理端路由 + 三条用户侧读路由均为**新增路由与新增权限声明**，未改 schema、未改既有状态机。

- **售后代客确认（≠ 审批）**：`POST /admin/after-sales/{id}/confirm`，权限码 `esports:aftersales:confirm`，路由由 `ownedAfterSaleHandler` 保护（负责客服 / 超管，越权 403）。语义严格限定为 `pending_customer_confirm → pending_approval`：写 `customer_confirm_channel=admin_proxy`、`customer_confirmed_by_admin_id`、`customer_confirmed_at`，并清除上一次客户拒绝原因；**不写** `approver_admin_id` / `approved_at`、**不预占** `refund_reserved_cents`、不产生任何资金动作。非「待客户确认」状态（已进入待审批 / 待执行 / 终态）一律 409，重复调用同样 409，因此它**不能**替代 `.../approve`；「审批人不得为该单处理人 `handler_admin_id`」的职责分离不受影响，代确认人也不会因代确认取得审批资格。**注意**：确认路径（客户自助与代客确认共用 `confirmAfterSalePlanIn`）**没有**存单冻结守卫——受理与提交方案有 `ensureAfterSaleSourceOrderNotFrozen`，确认只按状态条件更新，因此已冻结订单仍可能被推进到待审批（后续审批 / 执行仍会拦冻结）；`adminConfirmAfterSaleHandler` 的注释把「冻结订单」也写成 409，与实现不一致，以本条为准。
- **档期超时处置的可用动作（只读）**：`GET /admin/orders/{id}/appointment/disposal-options`，权限码 `esports:orders:appointment_disposal_options`，由 `readableOrderHandler` 保护（负责客服 / 协作客服 / 超管只读可见）。返回 `{allowed_actions, current_action, expire_at}`：`current_action` 取配置 `app.esports.appointment_expire_action` 的生效值（`refund` / `reschedule` / `both`，未配置或非法回落 `both`），`allowed_actions` 是与之对应的可执行集合；`expire_at` 是**档期确认截止时间**（缺失回落 `appointment_suspended_at`，两者都缺失返回空串，不伪造时间）。判定与 `disposeExpiredAppointment` 的允许校验共用同一份 `appointmentExpireAction`，不会出现「前端给了按钮、后端拒绝」；接口不写配置、不写事件、不改订单（订单版本与档期状态前后不变），也不回显其它配置或密钥类内容。
- **服务段计时读取（只读）**：`GET /admin/orders/{id}/players/{playerId}/service/timing`，权限码 `esports:orders:service_timing`，同样走 `readableOrderHandler`。返回 `{order_id, player_id, seat_index, status, effective_seconds, wall_clock_seconds, upper_bound_seconds, paused_seconds}`：墙钟跨度与 `upper_bound_seconds` 由**与时长更正校验同一个** `serviceTimingCorrectionBound` 计算，因此接口给的上限恒等于更正时可接受的边界；`effective_seconds` 对已结束服务段取落库 `system_seconds`（时长更正就是该字段的唯一写入口，避免读回值把上一次更正覆盖掉），进行中的服务段用实时口径（每次读取都在变，前端提交前宜重新取一次或以上界兜底）。陪玩在本单没有服务段返回 403（与代暂停 / 更正的「非本单成员」同口径）。更正本身仍单独持有 `esports:orders:service_timing_correction`，写入口仍由 `ownedOrderHandler` 收窄到负责客服 / 超管——**只读接口的可见范围比写入口宽**（协作客服能看见按钮与计时，点了会被 403，前端应按权限位隐藏按钮）。
- **充值退款执行批次列表（只读）**：`GET /admin/recharge-refund-executions?status=&limit=`，权限码 `esports:settlements:recharge_refund_executions`（与既有的 `.../{id}/execute` POST 是两条独立声明）。`status` 可空（不过滤）或取 `pending` / `executed`，其它值 400 并提示「批次状态只能是 pending 或 executed」；`limit` 缺省 100、夹在 1–200；默认按 ID 倒序（最新批次在前），并批量补齐关联售后单号（一次 `IN` 查询，不逐行查库）。
- **用户侧评价列表**：`GET /user/reviews`（`AuthUser`，可选 `?limit=`（默认 100 / 上限 200）与 `?order_id=`）。身份由 `customerForVerifiedUser` 解析，**不接受客户端传 `customer_id`**；`?order_id=` 非本人订单返回 403（不泄露订单是否存在）。字段 `review_id / order_id / order_no / player_id / rating / content / verified / status（normal / disputed / invalid / appeal_pending）/ appeal_status（none / pending / approved / rejected）/ appeal_id / can_appeal / created_at`，按 ID 倒序。`appeal_status` 由该评价的申诉流水推导，评价处于 `appeal_pending` 而流水缺失（历史脏数据）时保守返回 `pending`；`can_appeal` 与 `createReviewAppeal` 的拦截条件完全一致（终局 `invalid` / 申诉中 / 已有申诉 / 超过 `app.esports.review_appeal_window_days` 时限）。用户端自助路由按仓库惯例**不声明 PermAPI**（仅管理端路由强制）。
- **用户侧改期提案列表**：`GET /user/reschedule-proposals`（客户，可选 `?order_id=`，`?limit=` 默认 50 / 上限 100）与 `GET /user/player/reschedule-proposals`（陪玩，仅 `?limit=`，按本人持 `accepted` / `invited` 席位的订单收窄，**不接受 `?order_id=`**）。字段在前文「改期提案」之外另有 `my_decision` / `my_seat_index` 与 `progress`（客户 + 每名需确认成员的 required / confirmed / rejected 计数、`pending_confirmers` 逐人清单 `{type, id, seat_index}`、`complete`，成员集合按 accepted + invited 席位）。**待确认（`pending`）提案排在历史之前**，其后按提案 ID 倒序——确认时限只有小时级，被历史记录挤出列表就等于客户端看不见。范围推导上限：客户按最近 200 单、陪玩按最近 200 个席位。
- **权限旁注**：这四条管理端权限只是**声明**，需 `go generate ./internal/apps` 生成后再由 `Provision` 播种，并**在角色里单独授权**——`cs_admin` 白名单未包含 `appointment_disposal_options` / `service_timing` / `aftersales:confirm`（见「内置角色与客服权限白名单」）；未授权前非超管调用会被 `PermissionMiddleware` 拒绝。

人工检查：造一张「待客户确认」售后单，用负责客服调代客确认接口应转「待审批」，且 `approver_admin_id` 为空、订单 `refund_reserved_cents` 不变；再次调用、或对「待审批」单调用应 409；随后用**另一名**员工审批应成功，用该单处理人审批仍 403。对已付款待派单订单调 `.../appointment/disposal-options` 应返回与 `app.esports.appointment_expire_action` 一致的 `allowed_actions` 且订单版本不变。对服务中订单的某陪玩调 `.../service/timing`，用返回的 `upper_bound_seconds` 做时长更正应成功、+1 秒应被拒；换成非本单陪玩应 403。造待打款与已打款批次各一条，`?status=pending` 只应返回前者，`?status=x` 应 400。用客户身份 `GET /user/reviews` 应看到本人评价与 `can_appeal`，用他人订单 `?order_id=` 应 403。

## 陪玩责任信用联动（2026-10）

责任方判定为陪玩的售后执行完成时，同一事务内自动写一条 `EsportsPlayerCreditEvent` 只增记载（`kind=fault_admitted`，取追偿金额与来源陪玩，同一售后单幂等），并写 `player.credit_recorded` 外盒。**自动记载不改接单资格**，避免自动处罚误伤；资格变更只由复核动作显式触发。

复核入口 `POST /admin/player-credit-events/{id}/review`：`action=penalty` 确认责任并把该陪玩 `qualification` 置为 `suspended`（暂停接单，必须填复核说明），`action=clear` 判定不构成责任只清记录、不动资格。恢复接单资格走既有「陪玩」档案编辑入口，复核动作不自动恢复。`GET /admin/player-credit-events`（可按 `player_id`、`status` 过滤，状态枚举受控）与陪玩端 `GET /user/player/credit-events`（只看本人）用于查询追溯。

人工检查：让一张售后单的责任方为陪玩并带追偿金额，执行完成后到「陪玩」相关查询应看到一条「已记载」信用事件且该陪玩资格仍为合格；对其选 `penalty` 并填说明后，该陪玩资格应变为暂停接单；换一条记载选 `clear` 应只把事件置为已消除、资格不受影响。

## 陪玩端主动邀请固排（2026-10）

`GET /user/player/orders/{id}/teammate-candidates` 列出本人可邀请的固排候选，`POST /user/player/orders/{id}/team-invites`（body `teammate_player_ids`，1–9 个）由主陪玩就本人已接单的订单邀请固排占剩余空席位。硬性口径与后台「一次成队」一致：本人须持该单 `accepted` 席位、固排关系已在该订单分区建立、固排 `qualification=qualified` 且 `presence=online`、分区与等级达标、容量可用、空席位充足；任一不满足即整体失败回滚。

与后台成队的差别：主陪玩席位已 `accepted`，本接口只新增固排邀请、不重复占用主陪玩容量；陪玩发起的邀请不写 `invited_by_admin_id`，席位来源记 `fixed_team`，操作人由 `order.team_invited_by_player` 订单事件承载（`actor=player`）。邀请仍受同一邀请有效期约束，超时由既有回收任务处理。

人工检查：用主陪玩身份对本人已接单的多人订单调候选接口应只返回合格在线固排；对非本单订单应返回 403；邀请非固排、离线固排或列表为空应返回参数错误；正常邀请后剩余席位应转 `invited`、来源为固排、无后台操作人，订单转待接单并留 `order.team_invited_by_player` 事件。

## 钱包展示口径配置（2026-10）

`app.esports.wallet_display`（JSON）可配置每类钱包账户的展示名称、图标、说明文案、计费单位名与固定换算倍率：

```json
{"customer_balance":{"display_name":"账户余额","icon":"wallet","description":"可用于支付订单","unit_name":"元","unit_cents":100},
 "player_catfood":{"display_name":"猫粮","icon":"cat","description":"已结算收入","unit_name":"猫粮","unit_cents":100}}
```

钱包账户视图新增 `display` 字段：除了名称 / 图标 / 说明 / 单位名外，还按 `unit_cents`（每单位对应多少分，即固定倍率）给出两位小数的 `display_amount`，换算用整数运算（`分 / unit_cents`，余数按百分位向下取整），不引入浮点误差。配置缺失、JSON 非法、单字段缺失或 `unit_cents ≤ 0` 均按**字段级**回落内置默认（余额 / 猫粮，100 分每单位），配置写坏不会改坏金额展示；列表接口一次性读取配置后逐行套用，避免逐行查库。

人工检查：默认状态下钱包接口的前端展示口径应为「余额 / 猫粮」且 2050 分显示 `20.50`；把 `unit_cents` 改为 50 后同一余额应显示 `41.00`；把配置值写成非法 JSON 后应回落默认而不是报错或显示异常金额。客户端（客户钱包、陪玩钱包、陪玩首页）已改为优先读 `display`，并用 `GET /user/wallets/display` 兜底；管理端页面仍用固定文案渲染。

## 历史订单补录与业绩隔离（2026-10）

`POST /admin/orders/backfill` 补录漏报的历史订单，须带 `Idempotency-Key`（8–128 字符，幂等键与请求体哈希按常规报单同口径，重复提交返回同一张单、同键不同体判冲突）。入参：`customer_id`、`sku_id`（可选，省略或 0 表示无 SKU 的历史补录）、`quantity`、`unit_price_cents`（有 SKU 时留 0 回落 SKU 规格价，无 SKU 时必填且 >0）、`settled_at`（RFC3339 实际成交时间，必填且不得晚于当前、不得早于 5 年前）、`fulfillment_status`（`completed` 默认 / `cancelled`）、`payment_status`（默认 `paid`，历史补录单必须已收款）、`reason`（4–255 字，必填）、`count_cs_commission`（是否计入客服提成，默认 `false`）。返回体除订单外还给 `counted_cs_commission` 结论。

隔离规则（与常规报单的关键差别）：入口固定 `entry_point=admin_backfill`（调用方不能指定）；只允许 `completed` / `cancelled` 两个终态，**不占 SKU 名额、不建支付意图与支付回执、不建派单席位**，避免历史单消耗当期容量；订单 `created_at` 与 `paid_at` 写实际成交时间，报表按业务发生期归集；`completed` 单同时写 `closed_at` 与 `close_net_service_cents` 快照，保证后续退款冲回口径正确。有 SKU 时沿用 SKU 快照价格 / 服务量；无 SKU 时不绑定 SKU（`sku_id=0`），用 `unit_price_cents` 兜底定价、服务量按数量 1:1、席位默认 1。**陪玩分成与成长值默认正常计提**（与普通订单一致）；**客服提成默认不计入**，写一条 `order.cs_commission_excluded` 外盒留痕，只有显式 `count_cs_commission=true` 才在同一事务内按结单口径计提。补录必须记录补录原因、补录人与补录时刻，写 `order.backfilled` 订单事件与外盒；补录原因必填（4–255 字）；`order.backfilled` 事件与外盒额外留痕操作人、客户、客户归属客服、`cross_owner` 与 `is_super_admin`。**超管可为任意客户补录，非超管仍限本人负责的客户**；补录单的 `responsible_admin_id` 取客户归属客服（客户无归属时才回落操作人），避免超管代补录把业绩与客服提成算到自己头上。

结单路径也加了同一守卫（`orderCountsCsCommission`）：即使有人把补录单推进到结单流程，补录单仍默认不计提客服提成，只写客服提成排除外盒，不会把历史流水算进当期业绩。

人工检查：用客服身份对本人客户补录一张 30 天前的已完成单，应看到 `entry_point=admin_backfill`、`created_at` 为 30 天前、`closed_at` 已写入、SKU 的已售与预占名额均未变化、`order.cs_commission_excluded` 外盒存在、成长值流水已计提且客服提成明细为空；同一 `Idempotency-Key` 重复提交返回同一张单，改数量后同键提交应报冲突；把 `count_cs_commission` 置 `true` 再补录不应出现排除外盒且按规则产出客服提成明细；省略 `sku_id` 并给 `unit_price_cents` 可补录一张 `sku_id=0` 的历史单。管理端补录表单已接入，字段名为 `count_cs_commission`（旧名 `count_commission` 已废弃且不再生效），收款状态固定为已收款、不再提供「未收款」选项。超管 / 归属边界：用超管账号补录**他人客户**应成功，且落库的 `responsible_admin_id` 是客户归属客服、`order.backfilled` 留痕 `cross_owner=true`；换非超管补录他人客户应 403「无权访问该客户或订单」且不落单、不写幂等记录；补录原因为空 / 过短（< 4 字）/ 超过 255 字均应 400 并提示「补录原因必填，长度须为 4–255 个字符」。

## 收款账户与主体白名单（§4.3，fail-closed）

收款账户走 `GET/POST /admin/payment-accounts`、`POST /admin/payment-accounts/{id}/review`（批准 / 驳回，须填意见）与 `POST /admin/payment-accounts/{id}/disable`；收款方式启停走 `GET /admin/payment-channels`、`POST /admin/payment-channels/{channel}/status`。提交一律进 `pending_review`，**不允许直接启用**；审核人不得是提交人（自审 403），驳回必须填结论。

**收款主体白名单改为 fail-closed（2026-10）**：主体名白名单的配置键是 `app.esports.payment_subject_whitelist`（逗号分隔的工作室主体名，去空白、去空项）。**未配置（缺行 / 留空 / 全空白）时提交与启用收款账户一律被拒**，不再是「留空 = 不限制」——否则任何主体名都能提交、再经财务审核被启用为收款账户，等于该控制默认关闭。中文提示为：

- 未配置：「未配置收款主体白名单（`app.esports.payment_subject_whitelist`），请先在系统配置中登记工作室主体后再提交或启用收款账户」；
- 不在名单内：「收款主体「<主体名>」不在工作室主体白名单内」（主体名去空白后须与名单项完全匹配）。

**提交与批准两处都校验**：`createPaymentAccount` 提交时校验一次；`reviewPaymentAccount` 在 `approve=true` 时按**当前**白名单再校验一次（提交后白名单被改小或清空的一律拦下，存量待审核账户在批准时同样受这条约束），驳回路径不需要该校验。升级注意：先登记白名单再放开收款账户的提交与启用，否则提交与启用全被拒；白名单未登记完成前不要放开审批。

## 已核销收款的渠道撤销 / 拒付（2026-10）

核销后的钱可能被渠道或客户事后撤回（商户撤销、客户拒付），此前系统只记录「到账」、核销后没有任何独立风险事件，账面上订单仍是已收款。现在：`POST /admin/payment-reversals` 由财务登记撤销 / 拒付（`receipt_id` 指向一笔已核销支付回执，`kind` 取 `revoked` 渠道撤销 / `chargeback` 客户拒付，`amount_cents` 不得超过该回执实际到账额，`reason` 4–255 字，`evidence_ref` 存渠道通知或银行回单引用），`GET /admin/payment-reversals` 按订单与状态查询，`POST /admin/payment-reversals/{id}/decision` 处置。

一笔回执只允许登记一次（`receipt_id` 唯一索引兜底）。**登记只记账与告警，不改订单资金状态**——订单怎么处置由人工在 decision 里明确，避免系统替人做资金判断。登记写 `order.payment_reversal_registered` 订单事件与 `payment.reversal_registered` 外盒。

处置动作（`pending → handled`，`ignore` 记为 `rejected`），处置人不得是登记人：`mark_exception` 把订单 `paid/partial` 改判为 `exception`（资金异常转人工，必须填处置说明）；`suspend_customer` 在其上再把客户置 `restricted` 并写风控原因（阻断客户自助下单，必须同时填处置说明与风控原因）；`ignore` 认定为误报，只留结论不动订单。处置写 `order.payment_reversal_handled` 订单事件。

人工检查：走完「提交到账凭证 → 财务核销」拿到回执后，用财务身份登记一笔超额撤销应报参数错误，正常登记后订单 `payment_status` 仍为 `paid` 且出现 `payment.reversal_registered` 外盒；重复登记同一回执应报冲突；换另一个管理员选 `suspend_customer` 并填风控原因后，订单应转 `exception`、客户 `risk_status` 变 `restricted`，客户自助下单被拒；用 `ignore` 处置另一笔时应只留结论、订单与客户都不变。真实渠道回调自动登记仍需商户密钥与回调地址，当前为财务人工登记闭环。

## 2026-10-17 修复批次：资金口径、通知投递、在线收款与客户端补齐

本章节记录本轮 9 项修复的最终口径，并附第 10 项「README 早期未实现清单对齐」（纯文档，不改代码）。**前文凡与本批次冲突的表述（如「外部渠道退款未实现」「真实推送通道未接入」「管理端补录表单尚未接入」「在线支付验签回调尚未开通」）均以本章节为准。**

### 1. 外部退款：纳入预占、禁止自执行、同步冲回（P0）

`executeExternalRefund` 补齐三条规则：

- **不得自执行**：`registered_by_admin_id == 执行人` 直接 403（与售后「不得自审」同口径），避免同一人既登记凭证又核验放款。
- **额度分两种**：关联售后单（`aftersale_id` 非空）时，金额已在售后审批阶段预占进 `refund_reserved_cents`，执行时要求 `refund_reserved_cents >= amount`，扣减预占并累加 `refunded_cents`，不再重复占用额度；未关联售后单时按 `refunded_cents + refund_reserved_cents + amount <= payable_cents + 加钟已收款`（§12.5-02 可退上界）校验，只累加已退额，**不得侵占已被售后占用的额度**。两种情况都用条件表达式防并发，累计已退额不会超过可退上界（服务应付 + 加钟已收款，加钟已收由只增流水求和派生）。
- **同事务冲回三件套**：退款落账后在同事务内冲回陪玩分成（`reverseOrderCommissionFor`）、客服提成（`reverseOrderCsCommissionFor`）与客户成长值（`reverseOrderGrowthFor`）。三者由 `refundReversalContext` 统一承载退款金额、幂等键与争议陪玩，售后链路与外部退款链路共用同一套冲回算法，只是幂等键不同（售后用 `AfterSaleNo`，外部退款用 `RefundNo`）。成长值流水沿用 `source_type=after_sale` + `source_id=退款单号`，语义为「退款冲回」；未改 schema。

登记阶段同步加基本校验：未关联售后单时按「已退 + 已预占 + 该订单已登记未执行的外部退款 + 本次 ≤ 服务应付 + 加钟已收款」拦截，避免多笔待执行登记合计透支订单额度；关联售后单的登记金额已含在 `refund_reserved_cents` 中，不重复计入。

人工检查：财务 A 登记一笔外部退款后自己点执行应返回 403；换财务 B 执行应成功，订单 `refunded_cents` 增加、`refund_status` 变 `partial`，且分成 / 客服提成 / 成长值各出现一条负数冲回明细；重复执行同一退款返回 409 且不再新增冲回明细；已退 300 分后再登记 800 分（应付 1000）应在登记阶段即返回 409。

### 2. 部分退款：按累计已退计算剩余可返本金（P0）

- **充值批次退款**：执行明细按 `(account_type, account_id, batch_source_type, batch_source_id)` 聚合已生成金额，逐批可退额取 `min(试算剩余, 批次原始本金 − 既有执行明细合计)`。即使前一笔执行明细仍为 `pending`（真实打款未回填、钱包余额尚未扣减），后一张售后单也无法就同一批次重复退款，**所有售后单的执行明细合计不超过批次原始本金**。
- **充值售后部分退款**：`executeRechargeAfterSale` 由「必须等于全部可退本金」放宽为 `<= 剩余可返本金`，本金按实际退款额冲减，赠送按「退款本金 / 可退本金」同比例作废（全额退款即 100% 作废，与既有口径一致）。
- **渠道撤销累计**：`registerPaymentReversal` 增加「同一回执已登记且未 rejected 的金额合计 + 本次 ≤ 回执实际到账额」校验。schema 仍保留 `receipt_id` 唯一索引（一笔回执一次登记），累计校验作为防御层存在。

并发与多次部分退款是本批次的测试重点：`TestExternalRefundConcurrentExecutionNeverOverRefunds`（两个 goroutine 同时执行、累计不超应付）、`TestExternalRefundRepeatedPartialRefunds`（连续三笔部分退款）、`TestRechargeRefundExecCumulativeBatchCap`（同批次多笔执行的合计上限）、`TestRechargeAfterSalePartialRefund`、`TestPaymentReversalPartialRegistrationCumulativeLimit`。

### 3. 通知：按订单与角色定收件人，接真实推送（P0）

- **不再全员广播**：`ent.Message.user_uid` 为空即全员广播（读取侧按 `user_uid = 当前 uid OR user_uid = ''` 命中）。原实现写空 `user_uid` 把业务通知与整段事件载荷投给所有人，现改为逐收件人投递：管理员落真实 `Admin.UID`，客户 / 陪玩落 `customer:<id>` / `player:<id>` 稳定地址（客户端消息中心未建，实时触达由推送网关承担）。
- **收件人映射**：`order.*` → 订单客户 + 责任客服 + 已接单席位陪玩；`aftersale.*` → 售后客户 + 责任客服（订单来源优先 `responsible_admin_id`，否则回落 `handler_admin_id`）；`payment.*` → 责任客服（退款 / 拒付 / 驳回类同时通知客户）；`dispatch.remind_admin` → 责任客服；`customer.*` → 该客户 + 归属客服；`player.*` → 该陪玩。同一人多角色合并为一条消息。
- **解析不到收件人即失败**：返回 `errNotificationRecipientMissing`，事件按外盒退避重试直至 dead 并计入客服失败待办，**绝不降级为广播**。
- **正文只放业务摘要**：标题 + 摘要（如「订单已结单，订单号 NO-xxx」），完整 payload 只进 `extra`，不再进入消息正文。
- **真实推送通道**：配置 `app.esports.notification_push_webhook`（`{"url","secret","timeout_seconds"}`）后按收件人 POST，头 `X-Esports-Signature: sha256=<hex HMAC-SHA256(secret, body)>`，非 2xx / 超时交由外盒退避重试；未配置视为该通道未启用，站内信仍投递（站内信不受 `notification_channels` 开关控制，开关只决定是否调用 webhook）。

人工检查：结一张有多席位陪玩的订单后，消息中心应只出现客户 / 责任客服 / 相关陪玩三类收件人，**不存在 `user_uid` 为空的全员消息**，正文不含 `payload=` JSON；把某订单责任客服清空后再触发订单事件，应只投客户与陪玩且不报错；配置 webhook 后抓包应看到签名头与收件人角色数组。

### 4. 在线收款：发起、验签回调与核销（P1）

- **发起收款**：`POST /user/orders/{id}/payment/start`（`AuthUser`），请求体 `{"channel":"wechat_work"}`，返回 `{intent_no, channel, pay_url, amount_cents, expires_at}`。渠道必须已开通（`payment_channels`）且存在审核通过的收款账户，否则 503；复用 `ensureOrderPaymentIntent` 保证支付意图与报价确认书 / 金额 / 报价版本一致。pay_url 不本地持久化，渠道侧可按 `intent_no` 重取。
- **验签回调**：`POST /payment-notify`（兼容 `X-Pay-Channel` 头）与 `POST /payment-notify/:channel`（`AuthPublic`）。**必须使用原始 body 验签**，签名为 `hex(HMAC-SHA256(app_secret, canonical))`，canonical 为 `app_id / body_sha256 / timestamp` 按键字典序拼接，`timestamp` 5 分钟内有效，常量时间比较。验签通过后走唯一核销边界 `applyVerifiedPayment`（订单 `paid` → `pending_dispatch`、名额 hold `reserved` → `sold`、写回执 / 账本 / 外盒）。
- **状态码**：200 成功 / 400 载荷或单据无效 / 403 验签失败 / 409 幂等冲突 / 503 渠道未开通。**不存在假成功网关**：未配置或未开通的渠道一律 503。
- **配置**：`app.esports.payment_gateways`，按渠道编码索引，`kind=http_hmac` 目前是唯一实现：

```json
{"wechat_work":{"kind":"http_hmac","base_url":"https://pay.example.com","app_id":"studio-001",
 "app_secret":"***","notify_url":"https://studio.example.com/api/v1/esports/payment-notify/wechat_work","enabled":true}}
```

端到端验收由 `TestOrderPaymentGatewayEndToEnd`（httptest 假渠道跑通「发起 → 回调 → 订单已支付 → 名额已售」）覆盖，另有签名错误、重放幂等、未配置 503、金额不足仍记异常四条用例。真实商户密钥与真实回调地址仍需业务方提供后才能对外开通。

### 5. 补录订单：字段与表单口径统一（P1）

- 前后端统一为 `count_cs_commission`。旧名 `count_commission` **一旦出现即返回 400 并提示刷新页面**，不再静默按 `false` 处理——部署顺序错位会立即暴露为明确报错，而不是「开关点了没用」的隐性财务错误。新旧字段同时出现同样拒绝。
- 开关文案改为「计入客服提成」，说明为「仅影响客服提成；陪玩分成与成长值始终按补录单计提」——原「关闭则不计提分成 / 提成 / 成长值」与后端实际口径不符。
- 「未收款」选项已移除：历史补录单一律按已收款登记，未收款的历史单应补录为「已取消」。`cancelled` 单传 `count_cs_commission=true` 返回参数错误，不静默忽略。

### 6. 陪玩端心跳与售后申诉（P1）

- **心跳常驻前台**：`startPlayerHeartbeat` 原先无任何调用点，心跳实际从未发送。现由 `App.vue` 的 `onShow` 启动、`onHide` 停止；陪玩页只登记/释放页级持有者，**页面间切换（订单、抢单大厅、钱包等）不会再中断定时器**，真正停表只由应用退后台、登出或 token 失效触发。身份判断用 `esports_player_mode` 本地标记并绑定当前 token，换账号自动失效。
- **售后申诉页**：新增 `pages/player/appeals`（列表 + 处理进度 + 发起申诉），走既有 `GET/POST /user/player/order-after-sales`；发起入口在陪玩「我的」与「我的订单」。进度按「提交 → 受理 → 出方案 → 审批 → 终态」展示，并显示客服结论、退款 / 赔付、向本人追偿与客户拒绝原因。

### 7. 钱包展示：两端读服务端配置（P1）

客户钱包、陪玩钱包、陪玩首页改为优先读响应内嵌 `display`，缺失时读 `GET /user/wallets/display`（`AuthUser`），再缺失才回落到旧的固定文案。展示名称 / 说明 / 单位 / 金额全部来自 `app.esports.wallet_display`：`display_amount` 是最终值直接渲染，只有它缺失时才按 `unit_cents` 换算一次，不存在二次换算。充值 / 购卡页保持法币口径（元），不套用钱包单位。

### 8. 存单冻结规则（确认，P2）

保持既有口径，本批次未改动：存单只是订单上的冻结标记，**不改变履约状态、不释放名额占用**（不动 `quota_reserved`、不碰 `EsportsQuotaHold`），仅禁止后续写操作；恢复后回到原状态。未付款单、已结束 / 已取消单、重复存单均被拒绝。

### 9. 当前生产启用条件（P2）

`ESPORTS_EXPERIMENTAL=1` 即启用（不再限定 `CMF_APP_ENV=development`），但上线前必须满足：

1. `app.esports.payment_deadline_minutes` 配为非零（否则待付款订单永久不可支付）；
2. 真实收款需配置 `payment_gateways` 与商户密钥，否则发起收款返回 503（人工核验链路仍可用）；
3. 真实推送需配置 `notification_push_webhook`，否则只有站内信；
4. 企微侧边栏、真实打款 / 退款通道仍缺宿主与商户密钥，按人工核验闭环运行。

**上线自检**：`GET /admin/readiness`（管理端「收款管理」页顶部）逐项回报功能开关、支付时限、各收款渠道的通道参数与可用账户、站内/推送通知、企微自建应用的配置状态，并区分「阻断上线」与「仅降级」；只回显是否配置，绝不回显密钥、商户号或回调地址。功能开关与支付时限是唯二阻断项，其余缺失只让对应能力降级（例如在线收款不可用但人工核验仍可跑）。

本轮未新增数据库迁移；外部退款冲回、通知收件人、在线收款、钱包展示均复用既有表结构与索引。

### 10. README 早期未实现清单对齐（文档）

「当前状态」是逐阶段追加的开发记录，早期段落里的「未实现 / 未接入」在多轮迭代后已大量过期。本次按代码逐条复核并改写，**只改文档，不含代码变更**：

| 早期表述 | 当前事实 | 依据 |
| --- | --- | --- |
| 结单表单设计器 / 审批流节点编排未实现 | 字段库 + 模板 + 提交规则 + 串行审批流已实现，缺的是拖拽设计器与并行分支 | 「结单表单、提交规则与审批流（§3.4）」 |
| 外部渠道退款未实现 | 已实现，且纳入预占、禁止自执行、同步冲回三件套 | 本批次第 1 项 |
| 售后「待客户确认」与客户自助确认入口未实现 | 已实现（`POST /user/after-sales/{id}/confirm` / `decline`） | 「关联资源权限与售后客户确认」 |
| 事务外盒仅逻辑、未接投递器 | 已接站内信 + `notification_push_webhook`，缺人工重发入口 | 本批次第 3 项 |
| 定时自动结算 / 提现未实现 | 陪玩分成与客服提成均有自动结算任务，基础提现含审核与打款登记 | `esports_generate_player_settlements` / `esports_generate_cs_settlements`、`### 基础提现（§6.3 / §12.6）` |
| 礼物储值卡支付未实现 | 该能力已于 2026-10-20 随储值卡功能整体移除（不再属于「已实现」也不属于「未实现」）；礼物付款来源只剩客户余额本金与外部收款 | 「储值卡功能移除（2026-10-20）」 |
| 礼物独立争议 / 部分退款未实现 | 已实现（`pending_review → approved → completed`，另有 `rejected` / `withdrawn`，审核人不得自审） | `gift_dispute.go`（§9.5） |
| 提现次数限制 / 阶梯费率未实现 | 已实现（`withdrawal_daily_limit`、`withdrawal_fee_tiers` / `withdrawal_tax_tiers`） | 「基础提现」已实现段 |
| 游戏分区权限 / 技能等级 / 接单上限未实现 | 已在指派、接受邀请、抢单三处硬校验 | 「接单上限」「游戏分区权限 + 技能等级」 |
| 真实推送通道未接入 | 已接 webhook 推送（含 HMAC 签名与退避重试） | 本批次第 3 项 |
| 客服提成 `effective_at` 调度生效未做 | 已由定时任务到点激活 | `esports_activate_scheduled_commission_rules` |
| 企微侧边栏 H5 未接入 | 骨架页已建，签名接口已实现，缺真实凭据与「发送到会话」 | `e-sports-uniapp/src/pages/wecom/index.vue`、`POST /admin/wecom/js-config` |
| 页面外部域名白名单 / 发布前有效性校验未实现 | 均已实现（`page_allowed_domains`、`page_resource_check.go`） | 「页面装修（§16）」及其 ponytail 段 |
| 首页发布直接生效 | 已改为「提交 → 异人审核」（`publish_review` 状态 + approve / reject） | `page.go` `publishPage` |
| uni-app 只有示例页、无客户端会话 | 已具备 customer / player / cs 三身份会话与订单、钱包、礼物、售后页面 | `e-sports-uniapp/src/pages/` |
| 没有真实收款通道接口 | 已有 `payment-notify` 验签回调与 `payment_gateways`，仍缺商户密钥 | 本批次第 4 项 |
| 排名 / 积分 / 达标制自动计算未实现 | 已实现：下一期名单按本期获得方式自动重算（ranking 按综合评分降序、points 按历史完成单数降序、threshold 按「评分 ≥ `rating_min` 且完成单数 ≥ `completed_min`」双阈值），且**先按游戏分区收窄候选池再取名次** | `dispatch_ranking.go` `autoComputeDispatchPriority`、`esports_rebuild_dispatch_priority` |
| 礼物外部直付未实现 | 已实现：余额与卡本金都不足时对礼物确认单「发起收款」，在线渠道须网关可用且有启用中的收款账户，人工核销与渠道回调共用同一落库边界；**抽奖类礼物仍缺** | `gift_payment.go`、`gift.go` `giftPaymentSourceExternal` |
| 页面素材字节上传 / 裁剪未实现 | 已实现：装修页 canvas 裁剪后按附件上传（目录 `esports-page`），再把地址登记进素材库；**拖拽画布 / 任意坐标仍缺** | `go-admin-web` `components/page-designer/ImageCropper.vue`、`views/pages/index.vue` `uploadImageBlob`；`page.go` `savePageAsset` |
| 提现差异化税率未实现 | 按**金额**的阶梯税费已实现（`app.esports.withdrawal_tax_tiers`，取「金额 ≥ `min_cents`」的最高档）；**按陪玩 / 渠道区分的差异化税率仍缺** | `withdrawal.go` `taxBpFor` / `parseWithdrawalTiers` |

**仍未实现（保留，不要误读为已完成）**：以下保留项按代码复核后的口径写清「缺的是什么」；已完成项已移入上表。

- **自动结单 / 客户确认结单**：没有任何定时任务执行自动结单（`esports_scan_close_deadline` / `scanCloseDeadlineOverdue` 只把超时单写进客服与管理员待办），客户自助端也没有「确认结单」入口；结单仍只能由员工调 `POST /admin/orders/{id}/close`。
- **拖拽式表单设计器**：字段库 + 模板（全局 / 游戏分区 / SKU / 订单类型 + 金额区间）+ 提交规则 + 版本化已有，缺拖拽画布、任意坐标、字段级联与条件显示（`close_form.go` 顶部 ponytail 注明）。
- **并行 / 条件分支审批**：审批流只支持**串行**节点（`nodes.seq` 从 1 连续递增、逐节点推进，末节点通过才结单），缺并行会签与按条件跳转的分支节点。
- **规则灰度**：分成 / 提成 / 结单 / 结单表单规则只有 draft → active → history（另含 `scheduled` 到点生效）的版本管理，缺按比例或人群分批放量的灰度开关。
- **陪玩分成阶梯**：陪玩分成规则只有单一 `percent` / `fixed_per_order` / `fixed_per_unit` 口径（`ruleInput` 无阶梯字段）；阶梯目前只在客服提成上实现（`schema.CsCommissionTier`、`matchCsCommissionTier`）。
- **团队分成**：多席订单按**各成员自己的规则**与有效服务秒数分别计提（`splitPool` 分摊基数），缺团队级分成池与团队内部分摊比例配置。
- **外盒死信的人工重发入口**：`EsportsOutbox` 达上限置 `dead` 后只计入客服待办计数（`notification_failed`），缺查询死信明细与人工重投的管理端接口。
- **加钟的在线收款意图**：加钟只有客服「余额收款」（`POST /admin/order-addons/{id}/pay-by-balance`），缺在线收款意图一条路径（原「储值卡本金核销」随 2026-10-20 功能移除，不再列为缺口）。
- **抽奖类礼物**：礼物只有固定价与价格档位（`EsportsGiftTier`），无抽奖玩法、无开奖记录。
- **页面拖拽画布**：装修仍是纵向模块列表拖拽排序 + 右侧属性面板，缺自由画布与任意坐标（素材字节上传 / 裁剪已实现，见上表）。
- **企微应用消息与电话 / 语音渠道**：`app.esports.notification_channels` 已可填 `wecom_app` / `voice` / `push`，但三类通道名都发往同一个 `app.esports.notification_push_webhook` 网关，缺企微应用消息 API 与语音外呼网关的真实实现（未配置网关时只降级并记 `degraded_channels`）。
- **企微「发送到会话」与 `jsapi_ticket` 自动拉取**：侧边栏只能「复制话术」自行粘贴，缺发送到会话的通道能力；签名票据仍需手工预置 `app.esports.wecom_jsapi_ticket`，`access_token → jsapi_ticket` 自动拉取未接（`wecom_sidebar.go` 注释已标注接入点）。
- **个性化陪玩推荐**：`player_recommend` 走公开 `/players` 的筛选口径（游戏 / 等级 / 在线 / 可接单），不按客户偏好、历史合作或画像取数。
- **差异化提现税率**：按陪玩 / 渠道区分税率未实现（按金额阶梯已实现，见上表）。
- **真实商户收款 / 打款 / 退款通道与企微应用凭据**：`payment_gateways` 仍只有 `kind=http_hmac` 一种适配器；提现打款（`withdrawal_payout.go`）与外部退款（`external_refund.go`）仍是人工登记外部流水号；企微 `corpId` / `agentId` / 应用密钥待业务方提供。

### 11. 本轮流程缺口修复（订单收款 / 结单 / 优先派单 / 客户首页）

本轮把「收款 → 结单 → 派单 → 客户首页」闭环上的一批缺口补齐，**无数据库迁移、无新增环境变量**；除客户端一条外，每条都带同目录回归测试，测试名可直接在 `internal/apps/esports` 下检索。

| 问题 | 现在的行为 | 接口或文件 | 回归测试 |
| --- | --- | --- | --- |
| 外部到账凭证按「核验时刻」算到账时间，客户在支付时限内已付款、只是截图不清被驳回补交后会被判超时 | 核销的到账时间改按**凭证提交时间**（`proof.CreatedAt`）判定；超时任务 `expireOrder` 发现该订单仍有 `pending_verification` 凭证时不再取消订单（交还财务裁决，被驳回后才由下一轮超时扫描取消） | `offline_payment.go` `verifyOfflinePaymentProof`（`ConfirmedAt: item.CreatedAt`）、`service.go` `expireOrder`（前置查待核验凭证） | `timeout_pending_proof_test.go` `TestVerifyOfflinePaymentProofAcceptsProofSubmittedBeforeDeadline`、`TestExpireOrderSkipsOrderWithPendingVerificationProof`、`TestExpireOrderCancelsOrderAfterProofRejected` |
| 被驳回的凭证只能换流水号重提，撞 `(channel, transaction_no)` 唯一键后财务无法把「同一笔转账」对上 | 允许**同一流水号原地补交**：按流水号找回原凭证行重置为待核验（刷新意图 / 账户 / 金额 / 截图、清空核验结论），凭证单号、创建时间与提交人保持不变，唯一键仍保证同一笔流水不会被核销两次 | `customer_payment.go` `submitCustomerOfflinePaymentProof`（复用分支）、`resetRejectedPaymentProof` | `customer_payment_resubmit_test.go` `TestCustomerPaymentProofResubmitAfterRejection`、`TestCustomerPaymentProofResubmitVerifiedConflicts`、`TestCustomerPaymentProofDuplicateResubmitConflicts` |
| 结单条件 `evidence_count` 按服务段条数计（一段多图只算 1），且自定义条件树会**替换**默认底线，配一棵宽松条件树即可绕过「有凭证 / 服务量不为零」 | `evidence_count` 改按**图片附件张数**计（同段多图算多张、跨段重复附件 ID 只算 1 张、历史文本引用按 1 张兜底）；自定义条件树与默认底线树 **AND 合并**，只能加严、不能删掉默认底线 | `close_condition.go` `closeConditionEvidenceCount`、`mergeCloseConditionTrees`、`checkCloseConditions` | `close_condition_fix_test.go` `TestCloseConditionEvidenceCountByImageRefs`、`TestCustomConditionTreeCannotDropDefaultEvidenceFloor` |
| 优先派单下一期名单在全量合格陪玩里取名次，其它分区（甚至无权接该分区）的陪玩占满名额，通知阶段按分区过滤后该分区可能一个有效陪玩都不剩 | 生成阶段**先按游戏分区收窄候选池，再在分区内取名次**；生成阶段不按服务时段过滤（可接单性仍在通知 / 派单阶段判定），`len(名单) ≤ 名额` 恒成立 | `dispatch_ranking.go` `autoComputeDispatchPriority`、`dispatch_priority.go`（重算任务按 `grant.game` 逐分区调用）、`app.go`（`esports_rebuild_dispatch_priority` 注册） | `dispatch_priority_game_scope_test.go` `TestDispatchPriorityNextPeriodScopedByGame`、`TestDispatchPriorityNextPeriodIgnoresServiceSlotAtGeneration`、`TestDispatchPriorityNextPeriodTruncatesWithinGame` |
| 客户自助端只有「发起收款 + 提交凭证」，余额核销要走客服人工；站内渠道停用也没有拦截 | 客户自助端补齐**余额核销**：`POST /user/orders/{id}/pay-by-balance`（在线收银台 `POST /user/orders/{id}/payment/start` 此前已有）；两者复用管理端同一核销边界（金额、订单状态、支付时限、配额、账本），并按**站内渠道启停**明确拒绝。客户自助路由走 `AuthUser`、按绑定客户做数据隔离，不声明 `PermAPI`（与既有 `/user/...` 一致）。原同批补齐的「储值卡核销」已于 2026-10-20 随功能移除 | `customer_self_payment.go`、`app.go`（客户自助路由）、`wallet.go` `payOrderWithBalanceAsCustomer` | `customer_self_payment_test.go` `TestCustomerSelfBalancePayment`、`TestAdminBalancePaymentKeepsOperator` |
| 陪玩推荐公开接口只给在线状态，客户端只能猜「能不能约」，下单后才发现不可接单 | 公开陪玩接口返回真实 `bookable`（在线连接健康 ∩ 服务时段 ∩ 固排时段 ∩ 并发 / 当日容量），`player_recommend` 也可用 `bookable=true` 继续收窄 | `player.go` `publicPlayerView.Bookable`、`page_component_data.go` `listPublicPlayersByProps` | `page_player_bookable_test.go` `TestPlayerBookableFlagsConnectionServiceSlotAndFixedTeamSlot`、`TestPublicPlayersBookableFlagInDefaultJSON`、`TestPlayerBookableFalseWhenDailyCapacityFull` |
| 存单冻结的订单仍可催单，而催单会写 `remind_priority_level` / `remind_note` / `reminded_at` 并抬升版本，等于冻结期改单 | 催单纳入冻结守卫，返回 `errOrderFrozen`（HTTP 409），冻结期间不改字段、不升版本、不写 `order.reminded`；恢复存单后催单恢复可用 | `dispatch.go` `remindOrder`（调 `ensureOrderNotFrozen`） | `dispatch_remind_freeze_test.go` `TestRemindOrderRejectedWhenDepositFrozen` |
| 收款主体白名单为空时被当作「不限制」，任意主体名都能经财务审核变成可收款账户 | 白名单**未配置即 fail-closed**：提交与启用收款账户一律拒绝并提示先登记 `app.esports.payment_subject_whitelist`；批准（review）时再按**当前**白名单复核，提交后白名单被改小 / 清空的一律拦下 | `payment_account.go` `ensurePaymentSubjectAllowed`、`createPaymentAccount`、`reviewPaymentAccount`、`app.go`（配置项 Remark） | `payment_account_whitelist_test.go` `TestPaymentSubjectWhitelistFailsClosed` |
| 客户端仍按旧口径实现：报价确认前就展示收款账户、驳回后不能重提、没有余额 / 在线收银台入口、首页不吃组件 props、推荐卡片用 `online` 判可约 | 客户端（`e-sports-uniapp`，**不在本仓库**）已同步：报价确认前不展示收款账户、驳回后允许重提、接入余额核销 / 在线收银台、首页按组件 props 取数、推荐卡片按 `bookable` 展示并用「客户指定陪玩」下单（原储值卡入口已于 2026-10-20 随功能移除） | `e-sports-uniapp`（本仓库不含源码，此处只记口径） | 无（客户端自测，本仓库不覆盖） |

**升级注意（合并前后的口径变化）**：

1. **到账时间改口径**：外部到账核销改按凭证**提交时间**判定到账，且订单存在 `pending_verification` 凭证时超时任务不再取消订单。上线后会出现「已过支付时限但仍待财务核验」的订单，这是有意为之——不要手工取消，先让财务核销或驳回。
2. **白名单由「空 = 不限制」改为「空 = 拒绝启用」**：升级前必须先在系统配置登记 `app.esports.payment_subject_whitelist`（逗号分隔工作室主体名），否则新**提交**与**启用**收款账户全部被拒；**存量处于待审核的收款账户在批准时会被这条规则拦下**，须先配好白名单再审批。
3. **结单条件合并**：默认底线（凭证 / 非零服务量 / 无未处理加钟与售后）现在恒定生效，历史上为「放宽」而配的宽松条件树会突然结单失败；`evidence_count` 也改为按图片张数计，存量单的凭证张数可能变多（同段多图）或按 1 张兜底（历史文本引用）。
4. **优先派单名单生成口径**：下一期名单改为「先按游戏分区收窄再取名次」，各分区名单成员可能与之前明显不同，且不再按服务时段过滤（白天档陪玩也会入名单，接单性在通知 / 派单阶段判定）。升级后第一次重算前后建议各留一份名单做对比，再对外解释差异。

四类改动都是**行为收紧**，不需要数据库迁移；除收款主体白名单是新增的**前置配置**外，其余都不需要改配置。白名单未登记完成前不要放开收款账户的提交与启用。

维护约定：新增能力时同步改写本节「仍未实现」列表，不要再把已完成项留在里面。

## 统一实名认证（微信 / 支付宝 / 第三方 / 人工，2026-10）

客户与陪玩共用一张实名档案（`esports_real_name_verifications`）与一张只增的核验流水表（`esports_real_name_attempts`）：同一主体只有一条档案，重复提交在待审 / 已通过时返回 409，驳回或失效后可重新提交。旧表 `esports_player_identities` **保留只读**、不再写入，初始化时把历史 `verified` 记录一次性迁入新表（幂等，跳过已存在档案）；旧陪玩实名接口（`GET/POST /user/player/identity`、`GET/POST /admin/players/:id/identity`）保留为**兼容别名**，内部转发统一实现并投影成旧字段，权限码不变。

**渠道**：`manual` 人工审核始终可用；`third_party` 需运营在 `app.esports.realname_providers` 配置完整才出现在用户端可选列表；微信 / 支付宝目前是**骨架**（`ready()` 恒 false），未接入前不注册、不出现。渠道异常（超时、非 2xx、坏 JSON、缺跳转地址）一律返回 503「实名核验服务暂不可用」，绝不静默通过。

**接口**：

| 角色 | 接口 | 说明 |
| --- | --- | --- |
| 客户 | `GET/POST /user/realname` | 主体由登录态决定；GET 返回状态 + 可用渠道，POST 提交 |
| 陪玩 | `GET/POST /user/player/realname` | 同上，主体为当前陪玩档案 |
| 公开 | `POST /realname/callback/:channel` | 渠道异步回调：先验签再落定，重复回调幂等 |
| 管理端 | `GET /admin/realnames`、`GET /admin/realnames/:id` | 列表（按主体类型 / 状态 / 渠道筛选）与详情（含最近 20 条流水） |
| 管理端 | `POST /admin/realnames/:id/review` | 人工审核，驳回须填原因 |
| 管理端 | `POST /admin/realnames/submit` | 代提交，`subject_type` / `subject_id` 放在请求体，渠道缺省人工审核 |

**状态机**：`pending → verified / rejected / expired`。非人工渠道的待审记录超过 `app.esports.realname_pending_timeout_minutes`（默认 30）由 `esports_expire_realname_pending` 任务置 `expired` 并把流水置 `abandoned`；人工审核不超时。每日提交次数受 `app.esports.realname_daily_attempt_limit`（默认 5，0 = 不限）限制，按 UTC+8 自然日统计流水条数。

**拦截**：`realname_required_order` / `realname_required_recharge` / `realname_required_withdrawal` 三个开关默认关闭（与改动前行为一致）。开启后未实名主体在确认报价 / 创建充值意图 / 申请提现时被拦截，响应为 `403` + `{code: 403, message, error_code: "realname_required", scope: "order|recharge|withdrawal"}`（保留 `code=HTTP 状态码`，`error_code` 为追加口径），客户端据 `scope` 引导到对应主体的实名页。

**回调验签**：请求头 `X-App-Id` / `X-Timestamp`（Unix 秒，±5 分钟）/ `X-Nonce` / `X-Signature`，签名 = `hex(HMAC-SHA256(app_secret, app_id + "\n" + timestamp + "\n" + nonce + "\n" + 原始 body))`，比较用恒定位数。签名失败 403、时间戳过期 400、渠道未配置 503。`app_secret` 不在接口与日志中出现。

**敏感信息**：证件号只存脱敏值（保留前 6 后 4），原始号码不落库；列表 / 详情出参只给脱敏值，并有测试断言出参不含原号。

