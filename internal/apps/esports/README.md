# 陪玩报单源码功能集

本功能集通过 `app.go` 注册到 `/api/v1/esports`；管理端页面位于 `go-admin-web/src/apps/esports`。Ent Schema 只放在本目录的 `schema/`，由 `go generate ./internal/apps` 汇入主 Schema，再由 `go generate ./internal/ent` 生成客户端。不要直接修改生成文件。

## 当前状态

本节保留逐阶段开发记录，部分早期“未实现”描述已被后文和当前代码覆盖。P0 缺口与修复进度请以 `go-admin/docs/esports-p0-remediation.md` 的逐项核对为准；是否可上线仍须按实际通道、端到端流程和权限验收。

功能集默认关闭：模型可迁移，但不播种权限、不挂载路由，也不注册超时任务。**当前生产启用条件**为环境变量 `ESPORTS_EXPERIMENTAL=1`（已不再要求 `CMF_APP_ENV=development`），且必须先把 `app.esports.payment_deadline_minutes` 配成非零值，否则待付款订单无法支付。当前代码仅提供首版交易底座的一部分：商品模块（产品分类维护、固定价 SKU 的创建/编辑/上下架）、客服名下客户、即时待付款报单、按份数原子占用名额、幂等创建、超时释放、客户本人确认报价，以及供**已验签通道适配器**调用的到账裁决。订单操作补齐改价/改份数（先作废旧支付意图并抬升报价版本）、改期、取消（待付款释放名额，已付款转退款申请）与余额核销（转待派单）。新增**加钟（§3.3 / §6.2 / §12.5 首版）**：客服对**服务中**订单发起加钟（追加金额与工时，至少一项为正，同一订单只允许一条待确认），陪玩确认后按**确认时点锁定的分成规则**生成 `kind=addon` 的加钟分成明细，并把追加金额与服务量累加到订单（`addon_cents` / `addon_units`，**不改写原报价快照 `payable_cents`**）；存在待确认加钟时禁止结单。**加钟收款（§3.3 首版）**：确认后的追加金额由客服在订单页按**客户余额**收单（`POST /admin/order-addons/:id/pay-by-balance`，扣减顺序同订单核销、含批次逐笔分摊），加钟单记录已收金额 / 收款人 / 时间，并落 `order.addon_paid` 事件；**加钟退款分摊**已接入——售后退款按比例同时冲回 `kind=service` 与 `kind=addon` 两类分成明细（`加钟退款分摊` 备注）。加钟的支付意图 / 真实通道与储值卡核销未接入。钱包模块提供客户余额与陪玩猫粮两类账户，余额不落库、一律由只增流水求和派生，本金与赠送分列；管理员可充值、调整并查看流水。新增年龄资格核验：客服提交本人上传的私有附件、另一名员工独立复核、批准后原子更新客户资格；被核验引用的附件不可通过普通删除接口移除。客户确认要求已验证的 User 绑定和成年资格，保存完整报价版本；未确认、停售、迟到及多付的真实到账不直接进入可派单金额，异常部分记入暂收来源流水。新增**排单模块**（管理端/客服工作台）：订单按 `required_seats` 生成逐条席位，支持指派（校验陪玩资格与在线状态）、撤回、公共池认领、转单、催单与排单看板，邀请到期由系统任务回收并回退订单。并落 §5.9 时间线埋点（首次入池 / 首次派出 / 首次接单 / 开工各记一次、永不改写）。账本与幂等记录由 Ent Hook 禁止更新或删除。新增**陪玩模块**：管理端维护陪玩档案（`user_id`、昵称、资格 `pending/qualified/suspended`、在线状态 `offline/online/protected_abnormal`）；陪玩端自助接口（供 uni-app 客户端）覆盖上下线、我的邀请与接单/拒单、抢单大厅与抢单、我的订单、服务开始/结束（写入服务段，记录陪玩上报服务量与系统点击间隔秒数两个来源）。事务外盒已有条件认领、租约、退避重试和死信逻辑，仍需接入实际消息投递器及人工处置入口。新增**结单（§3.4 首版）**：客服手动结单，事务内校验“服务凭证不少于 1 张、服务时长不为零”后把 `pending_close` 订单条件更新为 `completed` 并留 `closed_at` 与 `order.closed` 事件；新增**结单规则（§3.4 条件自定义）**：可配置「凭证不少于 N 张、服务时长不为零、无未处理加钟 / 售后」与金额复核阈值，按 全局 / 游戏分区 / SKU 绑定、版本化管理（草稿 → 生效 / 回滚，改规则生成新版本）；金额达阈值时订单进入 `pending_close_review` **待复核**，由非负责客服的管理员复核通过（`close-approve` → `completed` 并计提）或驳回（`close-reject` → 回退待结单，不得自审）；无规则回落内置默认（凭证 ≥1、时长 >0、无未处理加钟 / 售后）。结单表单设计器、审批流节点编排、自动结单 / 客户确认结单未实现。新增**售后与退款（§13 首版核心切片）**：售后单覆盖受理 → 处理方案 → 审批 → 执行状态机，审批与执行分人、不得自审；赔付支持“退原核销款”（仅限原订单以客户余额核销的退款，按“先本金后赠送”还原本金 / 赠送构成）与“发放补偿权益”两种，钱包流水与订单退款额度在同一事务内落账；退款额度以订单 `refunded_cents + refund_reserved_cents <= payable_cents` 的条件表达式预占与扣减，退款流水以 `(source_type, source_id, entry_type)` 唯一索引保证幂等。补履约（补钟 / 换人）已实现（见下「售后补履约任务」）。新增**售后补履约任务（§13.6 首版）**：方案动作项新增「补钟 / 换人」参数（承接陪玩、席位、补足服务量、公司承担报酬），提交方案时校验承接陪玩资质与席位范围；审批通过后由执行动作在同一事务内创建补履约任务，**不退款、不冲回分成与成长值、也不改原订单实收与成交额**；任务由客服「开始服务 → 验收」两态流转（`pending → servicing → completed`），一张售后单只创建一个任务（`after_sale_id` 唯一索引兜底）。外部渠道退款、“待客户确认”节点与客户自助端确认入口未实现。新增**分成与结算（§12 首版核心切片）**：分成规则按 `player > game > global` 优先级解析（同层取最高版本，无命中则不分成），结单时在同事务内为订单服务成员生成分成明细、售后退款在执行时按比例冲回负数明细，均以唯一 `idem_key` 保证幂等；结算单按周期聚合待结算明细，锁定后把应结金额写入陪玩猫粮账户流水，明细批量转为已结算。规则灰度 / 定时自动结算 / 提现（打款）、阶梯与团队分成未实现。新增**礼物（§9 首版核心切片）**：礼物目录（价格档位、礼物自带分成与订单分成独立）、代客送礼（校验身份绑定 / 本人送礼 / 未成年 / 付款来源 / 大额二次确认，事务内原子扣减客户余额本金、生成陪玩礼物收入并计入客户成长值），礼物收入并入既有分成总账一起结算（`kind=gift` 明细与订单 `service` 明细区分），含防刷榜标记与人工复核、退款冲回分成与成长值、陪玩收礼 / 客户贡献日周月榜；外部直付通道、储值卡支付、抽奖类礼物未实现。新增**考官端（§7 首版核心切片）**：考核类型（入职 / 升级 / 抽查复核 / 复考）与六维度权重可配置（基点合计须 10000，未配置时回落等权）；考核任务状态机 `pending → scheduled → passed/failed`，分配考官后逐维度打分（评语与依据必填），按权重加权总分与全局通过线（系统配置 `app.esports.assessment_pass_score`，默认 60）判定；入职 / 升级 / 复考通过即按目标等级定级并开通接单权限（`qualification=qualified`），抽查不通过降一档并暂停接单（`qualification=suspended`）；陪玩对已出结论的考核可发起申诉（每任务一次），管理员复核成立时改判结论并重放定级动作；客户评价按「订单 + 客户 + 陪玩」唯一保留一份有效评价，修改递增 `version` 并追加只增修订历史，客服代录标记未验证不计入综合评分，综合评分仅纳入已验证且状态正常的评价并含样本数与规则版本（样本不足显示提示），异常刷评可复核为 `disputed/invalid` 剔除。新增**页面装修（§16 首版核心切片）**：运营在后台以结构化组件（纵向模块编排）配置 uni-app 客户自助端首页草稿，支持组件增删 / 排序 / 显隐 / 复制与属性 JSON 编辑；保存草稿走 `draft_version` 乐观锁，发布生成不可变快照并把旧生效版本转历史（一次性版本切换），支持定时发布与「以历史快照重新发布」式回滚，发布前校验组件类型 / 数量上限 / 必需入口（客服与订单入口），客户端走公开接口读已发布配置并在未装修时回落系统默认模板；配套素材库登记受控资源地址。拖拽画布、素材上传裁剪、域名白名单强校验、客户端能力兼容快照未实现。新增**客服端（§8 首版核心切片）**：PC 工作台 / 移动客服端 / 企微侧边栏三个入口共用同一套接口与业务规则（快捷报单复用报单、订单跟进与待办复用订单与售后、业绩看板复用 `cs_performance`、代客送礼复用礼物），本模块新增客户档案扩展（手机号 / 消费偏好 / 客户标签 / 建档来源 / 跟进状态 / 最近联系）与档案汇总（累计充值 / 消费 / 退款 / 送礼、余额本金与赠送分列、成长值、订单数）、只增的客户跟进记录、企微「企业主体 + 外部联系人标识」幂等自动建档、话术库（乐观锁编辑与启停）以及工作台聚合（我的客户数 + 按状态分桶的订单池与公共池摘要 + 待办提醒 + 本人业绩），非超级管理员一律只看归属本人数据。储值卡到期提醒、公共池客户 / 金额掩码、客服提成、企微侧边栏 H5 与「发送到会话」宿主能力、陪玩推荐未实现。新增**储值卡（§4.4 / §11.3 首版核心切片）**：每张储值卡是独立预付账户，登记合同确认、有效期、适用范围与开卡本金 / 赠送权益，卡内余额一律由只增流水求和派生、本金与赠送分列，预付款批次另记账本供来源追溯；支持外部收款开卡（留支付凭证）与**用余额购卡（账户间划转，同事务扣本金、加卡本金并产生配对流水）**，报单时用储值卡足额核销并把付款方式与关联卡写进订单独立字段，结单前校验「标记 / 关联卡 / 核销流水」一致；售后退款按原账户**原路退回**（储值卡订单退回原卡并冲回批次），退卡按「可退 = 未消费本金、赠送作废」试算并由财务审核、不得自审；到期任务只把过期卡标记为已过期，不擅自没收未消费本金。储值卡套餐折扣、扣减顺序可配置、批次级逐笔扣减、礼物可用合格预付本金纳入储值卡本金均未实现。新增**客服提成（§12.2 首版核心切片）**：与陪玩分成同结构不同主体，支持全局 / 指定客服的提成规则（报单金额 / 订单贡献 / 单量的基数 × 按比例含阶梯 / 每单固定额），结单同事务计提、**结单前退款从基数扣除、结单后部分退款按比例冲回、全额退款冲至零**，并受「陪玩分成 + 客服提成 ≤ 可分基数」上限约束；提成按规则周期生成**独立结算单且不写猫粮钱包**，锁定后由财务打款并留外部凭证；`cs_performance` 报表与客服工作台展示提成列。渠道成本与工作室剩余反算、尾差单列、提成结算单自动生成未实现。新增**基础提现（§6.3 / §12.6 首版核心切片）**：只有猫粮（已结算收入）可提现，陪玩自助申请时同事务扣减猫粮（`entry_type=withdraw`）避免重复申请超额，支持提现门槛、提现频率、手续费与代扣税费的配置化试算并给出申请金额 / 手续费 / 税费 / 实际到账四个口径，银行卡 / 微信 / 支付宝三种方式；首版一律由财务审核，通过后登记外部打款凭证与完税凭证、到账时间与打款人，驳回则原额退回猫粮；陪玩工作台补可提现余额。提现次数限制（仅支持时间间隔）、阶梯费率与差异化税率、打款对接真实通道未实现。新增**优先派单权（§5.4 / §5.5 首版，业务口径为独占优先通知）**：按游戏分区 + 周期（日 / 周 / 半月 / 月）授予名单，每游戏每期名额可配置，支持人工指定与排名 / 积分 / 达标 / 轮换五种获得方式、切换点前预生成下一期名单、中途人工增减，周期任务自动过期与转生效；订单进入待派单时若该游戏分区有生效名单，则**只通知名单内陪玩**（其他人在抢单大厅看不到该单），名单内**全部拒绝**或**超过配置的通知时长**后订单转 `grabbing` **释放到大厅**给所有人先到先得，客服亦可提前释放；派单候选按窗口可见性过滤（窗口内只列名单内、窗口外列全部合格在线）后再按评分与在途席位均衡排序。备注：此处的「独占」口径已按业务方明确要求实现，并**已回改功能列表文档**——§2.1 / §5.2 / §5.4 / §5.5 / §5.6 / §5.8 / §5.9 / §6.1 均已同步为「独占优先通知」，原文「排序加权、非独占、抢单大厅不受影响」已废弃。同时补齐 §5.6 的独立发布动作：新增**发布到抢单大厅**，把没有优先名单（或窗口已闭合）的已付款待派单**单陪玩**订单直接转 `grabbing`，发布前把被撤回 / 过期的席位重置为 `open`。并支持**客户指定陪玩**（§5.4）：订单 `preferred_player_id` 有值时直接走指派流程、**不进优先通知窗口**，报单时校验陪玩资质 `qualified`。排名 / 积分 / 达标制的自动计算、游戏分区权限与技能等级门槛、接单上限、历史合作与客户偏好等加分项未实现；真实推送通道未接入。新增**陪玩转单与催单（§5.7 首版核心切片）**：陪玩接单后未开始服务时可发起转单申请（带幂等键、同一席位只允许一条待确认申请），**客服确认前席位与订单状态都不变**，确认通过才把席位释放为待补位并按剩余席位把订单退回待接单或待派单（退回待派单时复用 §5.4 的开窗 / 释放逻辑，不会绕开优先通知）；驳回须填原因且保持原状，发起方可在确认前撤回。催单时间阶梯可配置（`app.esports.remind_ladder_minutes`，默认 5/15/30 分钟），`esports_remind_dispatch_ladder` 任务对超时未接单的邀请按档位逐级提醒——第 1 档提醒在线陪玩、第 2 档起升级提醒客服；服务超时提醒由 `app.esports.service_overtime_minutes` 配置阈值，`esports_check_service_overtime` 任务在服务段有效时长超阈值时提醒客服确认是否加钟；两者的提醒都写事务外盒与订单事件，幂等靠事务外盒 `event_key` 唯一索引，任务可安全重复运行。已开始服务的转单（属售后补履约）、企微应用消息与电话/语音渠道、加钟确认流程未实现。没有真实收款通道接口对外开放。

启用前仍需完成计划中的身份绑定与真实登录、支付意图及服务商适配、预约与派单、服务、售后与退款、对账报表、四类移动角色和企微侧边栏，并以功能规划 A01–A125 全量验收。年龄证据使用现有附件系统，生产前还须复核附件列表及访问地址权限对私有核验文件的隔离。现有 uni-app 项目只有示例页，尚无客户端会话能力。未取得业务签收的支付时限等参数默认 `0`，阻断可支付订单。不得仅将 `featureEnabled` 改为 `true` 作为上线方式。

## 管理端菜单

一个模块一个菜单，均为顶层菜单（共 16 个）：**商品**（`/esports/products`，产品分类与商品/规格维护）、**订单**（`/esports/orders`，订单列表、详情与改价/改期/取消/余额核销/结单）、**报单**（`/esports/reports`，客服报单、客户、年龄资格复核）、**钱包**（`/esports/wallets`，客户余额与陪玩猫粮账户、充值/调整/流水）、**排单**（`/esports/dispatch`，排单看板、席位指派/撤回、公共池认领、转单、催单、优先派单权名单与独占优先通知、陪玩转单确认）、**陪玩**（`/esports/players`，陪玩档案创建与编辑/上下线）、**售后**（`/esports/aftersales`，售后单受理/方案/审批/执行/退款）、**结算**（`/esports/settlements`，分成规则/分成明细/结算单/客服提成规则/客服提成明细与发放/提现审核）、**充值活动**（`/esports/campaigns`，充值活动配置与状态管理，第 9 个顶层菜单，SortOrder 48）、**会员等级**（`/esports/members`，会员等级配置与客户会员查询/手动调级，第 10 个顶层菜单，SortOrder 49）、**礼物**（`/esports/gifts`，礼物目录、代客送礼、单据复核/退款、收礼/贡献榜单，第 11 个顶层菜单，SortOrder 50）、**考核**（`/esports/assessments`，考核权重配置、考核任务打分定级、申诉复核、客户评价与综合评分，第 12 个顶层菜单，SortOrder 51）、**报表**（`/esports/stats`，五类经营统计报表入口，第 13 个顶层菜单，SortOrder 52）、**页面装修**（`/esports/pages`，uni-app 客户自助端首页装修、发布版本与素材库，第 14 个顶层菜单，SortOrder 53）、**客服工作台**（`/esports/cs`，客服端客户档案与跟进、企微自动建档、话术库、订单池与待办聚合，第 15 个顶层菜单，SortOrder 54）、**储值卡**（`/esports/stored-value`，预付账户开卡、卡余额与流水、退卡试算与财务审核，第 16 个顶层菜单，SortOrder 55）。菜单权限码分别为 `esports:product:menu`、`esports:order:menu`、`esports:report:menu`、`esports:wallet:menu`、`esports:dispatch:menu`、`esports:player:menu`、`esports:aftersale:menu`、`esports:settlement:menu`、`esports:campaign:menu`、`esports:member:menu`、`esports:gift:menu`、`esports:assessment:menu`、`esports:stats:menu`、`esports:cs:menu`、`esports:stored_value:menu`，子页面按钮沿用 `esports:skus:*`、`esports:categories:*`、`esports:orders:*`、`esports:customers:*`、`esports:age_checks:*`、`esports:wallets:*`、`esports:dispatch:*`、`esports:players:*`、`esports:aftersales:*`、`esports:settlements:*`、`esports:campaigns:*`、`esports:members:*`、`esports:gifts:*`、`esports:assessments:*`、`esports:stats:*`、`esports:scripts:*`、`esports:stored_value:*`。陪玩端自助接口属独立客户端（uni-app），不挂管理端菜单，其按钮权限统一归到 `esports:players:manage`。

### 充值活动与赠送（§4.5）

充值活动通过 `GET/POST /admin/campaigns`、`PUT /admin/campaigns/:id` 与 `POST /admin/campaigns/:id/status` 维护，落库字段包括：活动类型 `activity_type`（`full_gift` 满赠 / `ladder` 阶梯 / `limited` 限时）、阶梯档位 `tiers`（元素 `{threshold_cents, bonus_cents}`，按 `threshold_cents` **严格递增**，门槛与赠送额均须为正）、叠加规则 `stack_rule`（`stack` 可叠加 / `exclusive` 互斥）、适用客户 `audience`（`all` 全部 / `new_customer` 新客 / `specified` 指定，指定时 `audience_customer_ids` 不得为空）、活动期 `starts_at ~ ends_at`（须早于结束）、赠送有效期 `bonus_valid_days`。其中 `bonus_valid_days = 0` 表示**赠送永久有效**，永不被到期作废。状态流转为 `draft ↔ active ↔ paused`，任意非 `ended` 都可转 `ended`，`ended` 不可回退；`code` 创建后不可改，编辑走 `version` 乐观锁，`code` 重复或版本不一致返回冲突。新建活动状态固定 `draft`。

按活动充值走 `POST /admin/wallets/:id/recharge-campaigns?account_type=customer_balance`，**仅客户余额账户**（陪玩猫粮属结算发放，返回参数错误）。后端按实充金额 `principal_cents` 自动匹配当前生效中活动：阶梯取**最高**命中档，命中多个活动时，只要其中有 `exclusive` 活动就只取赠送额最大的一个互斥活动，否则把各命中活动赠送额求和。赠送写入的是**一条**带活动快照的充值流水（本金 / 赠送分列），快照 `rule_snapshot` 含命中活动列表 `campaigns`（`id/code/name/activity_type/stack_rule/version/matched_threshold_cents/bonus_cents/audience/bonus_valid_days`）、赠送合计 `total_bonus_cents` 与赠送到期 `bonus_expires_at`。

赠送到期作废由系统任务处理，按**只增不改**写入作废流水（不改写原充值流水）；只要命中活动中含 1 个 `bonus_valid_days = 0` 的永久赠送，整笔赠送即视为永久、到期任务不处理。重复幂等键返回冲突。

### 会员等级（§10）

等级配置通过 `GET/POST /admin/member-levels`、`PUT /admin/member-levels/:id` 与 `POST /admin/member-levels/:id/status` 维护，按成长值门槛 `growth_threshold` 分层（单位与订单金额一致，即分；`10000` = 累计消费满 100 元）；折扣以基点 `discount_bps` 表示（`10000` = 原价、`9500` = 9.5 折），另有会员订单优先 `priority_dispatch`、储值卡赠送比例 `prepay_bonus_bps`（基点）、专属客服 `dedicated_cs`（开关）与优先预约 `priority_booking`（开关）。`code` 创建后不可改，编辑走 `version` 乐观锁，`code` 重复或版本不一致返回冲突；状态为 `enabled / disabled`。

客户会员详情走 `GET /admin/customers/:id/member`，返回当前等级、折扣、累计成长值 `growth_total`、待降级等级 `pending_level_code`（空串表示无）与降级到期 `downgrade_after`，以及成长值流水 `ledger`（`source_type` 为 `order` 结单计提 / `after_sale` 售后退款冲回 / `manual` 手动 / `gift` 礼物打赏）与手动调级覆盖记录 `overrides`。成长值来源为**结单计提**（按订单实付额）、**礼物打赏**（按礼物金额）与**售后退款 / 礼物退款冲回**；升级即时生效，降级走可配置保护期（系统配置键 `app.esports.member_downgrade_guard_days`，默认 30 天），到期由定时任务 `esports_apply_member_downgrade` 执行降级。报单优惠顺序为「规格价 → 会员折扣 → 人工调整」。手动调级走 `POST /admin/customers/:id/member/level`（body `{level_code, reason}`），仅留覆盖记录、不改成长值流水。

已实现**排单池会员订单置顶**（§10.4）：`GET /admin/dispatch/board` 的 `pending_dispatch` 与 `public_pool` 会把「当前等级开启 `priority_dispatch`」客户的订单稳定置顶（等级须 `enabled`），并在响应新增字段 `member_priority_order_ids` 中给出这些订单 ID（两池按订单去重）供前端标记；只改展示顺序，**不绕过资质、档期与接单上限**等硬性条件。客户未定级或其等级关闭 `priority_dispatch` 时不置顶、不标记。

已实现**储值卡赠送比例（§10.4）**：会员等级的 `prepay_bonus_bps` 已接入开卡流程——未选套餐且未手填赠送额时，按「本金 × `bps` / 10000」自动计算赠送（整数向下取整，等级越高赠送越多）；选套餐时以套餐值为准，余额购卡不赠送。

已实现**生日礼遇（§10.4）**：客户档案新增 `birthday`（月-日，如 `03-15`，空表示未填），客服工作台待办新增「今日生日客户」——按业务时区（UTC+8）匹配今日生日的归属客户，提醒客服跟进；档案编辑校验 `MM-DD` 格式（月 01-12、日 01-31），非法格式被拒。

已实现**专属客服 / 专属通道（§10.4）**：规则为「会员等级开启 `dedicated_cs` 权益时，客户绑定固定负责客服、不参与轮转」。系统当前已用 `owner_admin_id` 承载归属客服，且轮转尚未实现，因此这条天然满足；`dedicated_cs` 仅作权益开关与展示标识。客户档案 / 工作台 / 企微侧边栏客户卡片（`GET /admin/customers/:id/profile`）新增 `member_level_name`、`dedicated_cs`（是否开启专属客服）与 `owner_admin_name`（负责客服姓名，昵称优先回落用户名），身份标识同时返回 `member_level_code` / `member_level_name` 供前端渲染等级徽标。

已实现**专属陪玩预约 / 优先预约（§10.4）**：会员等级新增 `priority_booking` 权益开关；启用该等级的客户在自助端（`POST /user/priority-bookings`）可对「指定陪玩」提交预约意向（陪玩 + 期望时段 + 备注），自助资料 `GET /user/profile` 返回 `priority_booking_enabled` 供前端显隐预约入口。意向落独立轻量实体 `EsportsPriorityBookingIntent`（不含 SKU 与金额，与订单 / 排单 / 报表解耦，客服确认后再走既有报单链路）；客服工作台待办新增「会员优先预约」（按归属客服隔离统计），待办列表 `GET /admin/priority-bookings` 按会员等级置顶（门槛越高越靠前，未定级 / 停用等级排最后）。无权益等级或未定级客户提交返回 403，期望时段须为未来时间。

调出 P0 → P2（首版不实现，明确不落字段）：生日自动发券、线下活动优先、自动发券等非核心权益。

### 礼物（§9）

礼物目录通过 `GET/POST /admin/gifts`、`PUT /admin/gifts/:id` 与 `POST /admin/gifts/:id/status` 维护。落库字段：`code`（创建后不可改）、名称、图标、价格 `price_cents`、分类、**礼物自带分成**（与订单分成规则相互独立，`commission_mode` 为 `percent`（`commission_bp` 万分比）或 `fixed`（`commission_cents` 每笔固定））、高额档 `high_value`、年龄限制 `require_age`、排序与状态（`draft / on_sale / off_sale`）。编辑走 `version` 乐观锁，`code` 重复或版本不一致返回冲突；新建固定 `draft`。

送礼走 `POST /admin/gift-orders`（代客送礼），body 含 `customer_id / gift_id / player_id / quantity / order_id（仅追溯，可不填）/ confirmed / client_key`。事务内按序校验：客户须已绑定登录身份（`UserID` 非空）、陪玩存在且**不得为客户本人**、客户年龄为成年（未成年人 / 未核验拦截）、礼物为 `on_sale`、金额不溢出；**付款来源首版仅允许客户余额本金**（拒绝赠送权益、无外部直付通道），余额不足返回中文提示。**大额二次确认**：礼物为高额档或金额达到系统配置阈值（键 `app.esports.gift_high_value_threshold_cents`，默认 1000 元）时，`confirmed` 必须为 `true`。**每笔礼物仅一名接收陪玩，且支付前快照收礼方名称**。支付成功时在同一事务内原子扣减余额（`entry_type=consume, source_type=gift`）、按礼物自带分成生成陪玩礼物收入、写入成长值（`source_type=gift`，按礼物金额）并即时重算客户等级；幂等键 `send_gift` 保证重复提交返回既有单据。

礼物收入**并入既有分成总账一起结算**：礼物收入写 `EsportsCommissionEntry`（`kind=gift`、`order_id` 可空、`status=pending_settlement`），`settlePlayerCommission` 按陪玩 + 状态聚合时会自动并入，明细 `kind` 与订单分成（`service`）保持区分。退款走 `POST /admin/gift-orders/:id/refund`（仅 `paid` 且未退款的单据）：回退客户本金，冲回陪玩分成（仅当该笔已生成分成明细时才写 `kind=gift_reversal` 负数明细，避免为尚未释放分成的挂起礼物凭空造出负数）、冲回成长值（`source_id` 加 `:refund` 后缀）。

**榜单**走 `GET /admin/gift-leaderboard?board=player|customer&period=daily|weekly|monthly`，分别给出陪玩收礼榜与客户贡献榜，已全额退款的不计入。**防刷榜**：同一「客户 → 陪玩」在 24 小时内笔数达到 5 笔即标记该笔为 `review_status=flagged` 且**不生成可提现分成**（收入挂起），由 `POST /admin/gift-orders/:id/review`（`approve` 补建分成并置 `approved` / `reject` 置 `rejected`）人工复核。

未实现的 `ponytail` 项：外部直付 / 真实收款通道（首版仅客户余额本金）、储值卡支付、客服提成开关（§12.2 未实现）、疑似关联身份的独立争议 / 退款申请与审核流程、跨身份「往返送礼」检测（防刷仅按同向笔数启发式）、抽奖类礼物（P2）。

### 考官端与客户评价（§7）

考核权重通过 `GET/POST /admin/assessment-rules` 维护，按考核类型（`entry` 入职 / `upgrade` 升级 / `spot_check` 抽查复核 / `retry` 复考）分组，覆盖六个维度（`game_skill 游戏技能 / teaching 教学能力 / attitude 服务态度 / communication 沟通能力 / compliance 合规意识 / stability 稳定性`），权重以基点 `weight_bp` 表示。写入为**覆盖式**：须覆盖全部六个维度、每维权重为正且合计**恰为 10000**，否则返回参数错误；未配置任何权重时按等权回落（`10000/6` 取整，余数分给靠前维度，合计仍为 10000）。

考核任务走 `GET/POST /admin/assessments`、`GET /admin/assessments/:id`。新建时 `task_no` 由服务端生成，入职默认目标等级 `novice`，非入职类型必须指定目标等级；状态机为 `pending`（待分配考官）→ `scheduled`（已约考核，`POST /admin/assessments/:id/assign` 写入考官与约定时间）→ `passed / failed`。打分走 `POST /admin/assessments/:id/score`（仅分配到的考官可打分，未分配时任意管理员可打），body 逐维度给出 `{dimension, score(0~100), comment(必填)}`，**须覆盖全部权重为正的维度**；服务端按各维权重快照加权得总分（`Σ score*weight_bp/10000`），与全局通过线（系统配置键 `app.esports.assessment_pass_score`，默认 60）比较判定通过。结论落地：入职 / 升级 / 复考通过时按目标等级写入 `level`、置 `qualification=qualified` 并刷新 `level_appraised_at`；抽查通过仅刷新考核时间，抽查不通过则降一档（`mentor→senior→skilled→novice`）并置 `qualification=suspended` 暂停接单。`GET /admin/assessments/:id` 返回任务 + 逐维度打分 + 申诉记录，构成完整考核记录。

申诉走 `POST /user/player/assessments/:id/appeal`（陪玩端自助，body `{reason}`，仅已出结论的考核可发、每任务仅一次）与 `POST /admin/assessments/:id/appeal/review`（`decision` 为 `approve` 改判并重放定级 / `reject` 维持原结论）。

客户评价：已完成订单的已核验身份客户可走 `POST /user/orders/:id/reviews`（body `{player_id, rating(1~5), comment}`）对**实际服务成员**逐人评价；客服代录走 `POST /admin/player-reviews`（`verified=false`，不计入综合评分）。同「订单 + 客户 + 陪玩」仅保留一份当前有效评价（唯一索引），修改时递增 `version` 并向只增表 `EsportsPlayerReviewRevision` 追加一条修订历史。综合评分走 `GET /admin/players/:id/rating`，仅纳入 `verified=true` 且 `status=normal` 的评价，返回平均分、样本数、是否样本不足（低于系统配置键 `app.esports.review_min_samples`，默认 5）与规则版本（`app.esports.review_rule_version`，默认 `v1`）。异常刷评 / 评价申诉由管理员走 `POST /admin/player-reviews/:id/status` 复核为 `normal / disputed / invalid`，`disputed / invalid` 不计入评分。

未实现的 `ponytail` 项：等级 → 分成率的自动换算（等级与接单权限联动已实现，具体分成率仍按既有「按陪玩」分成规则维护）、按考核类型的差异化通过线（当前统一用 `assessment_pass_score`）、复考的关联与自动触发、申诉多次发起（每任务仅一次）、考核记录与结论的专用导出。

### 经营统计报表（§17）

报表入口走单一路由 `GET /admin/reports/:type`，`:type` 为 `owner_overview`（老板经营总览，订单量/营收/退款/净额 + 按周期分段 + 下钻明细）、`cs_performance`（客服个人业绩，按负责客服 = 业绩归属分组并按净额倒序）、`dispatch_efficiency`（派单效率，席位邀请/接单/拒单/过期计数、平均接单时长，以及按 §5.9 时间线埋点派生的四类平均等待时长）、`aftersale_quality`（售后质量，状态分布、赔付金额与按类型/责任方拆分）、`finance`（财务资金与结算，钱包流水按账户与流水类型汇总、结算单汇总、支付回执按支付方式聚合）之一，均为**只读聚合**，不新增落库表。

筛选参数：`period`（`daily / weekly / monthly`）、`start` 与 `end`（自定义区间）、`responsible_admin_id`（负责客服）、`created_by_admin_id`（创建管理员）、`channel_source`（客户建档渠道快照）、`entry_point`（报单入口快照）、`sku_id` 与 `game`（游戏/商品）、`appointment_type`（即时/预约）、`seat_type`（单人/多人）、`payment_channel`（支付方式）、`kind` 与 `fault_party`（售后类型与责任方）、`limit`（下钻明细上限）。区间缺省时按 `period` 取当前自然日/周/月；财务总表的独立资金不能按订单渠道归属，因此拒绝 `channel_source` 与 `entry_point` 筛选。

权限与数据边界：`owner_overview / cs_performance / dispatch_efficiency / aftersale_quality` 对管理员开放，非超级管理员（客服）被强制收敛到**本人负责**的订单 / 本人处理的售后；`finance` 仅超级管理员可见（老板与财务），其余返回 403。报表聚合与下钻明细同在一次响应内返回，下钻到原单据沿用既有订单/售后详情接口。

未实现的 `ponytail` 项：报表导出与查看凭证的独立审计表（当前依赖管理端操作日志中间件留痕，与商品编辑同一口径）、财务总表尚不能按订单渠道来源归属独立钱包与结算资金、报表快照表（`ponytail:` 应用层内存聚合，`reportScanLimit=5000`，量级到十万级时改为 SQL 分组聚合）、报表级缓存与定时预聚合。

### 页面装修（§16）

首版仅支持运营人员在后台配置 **uni-app 客户自助端首页**（渠道固定 `uni_customer_home`，陪玩端与客服移动端首页不在装修范围）。装修配置只决定展示位置、样式与筛选规则，**不复制价格、余额等资金数据**，也不得绕过商品上架、陪玩接单资格、未成年人限制、价格与支付等服务端规则。

配置读写走 `GET /admin/pages`（列出渠道文档）、`GET /admin/pages/:channel`（读取工作副本与草稿组件，未装修时回系统默认模板）、`PUT /admin/pages/:channel/draft`（保存草稿，body `{components, expected_version}`，`expected_version` 与 `draft_version` 不符返回冲突，避免多人覆盖）。组件为纵向模块编排（`pageComponent` = `type` + `visible` + 自由 `props`），不开放任意坐标、脚本或自定义 HTML；首版允许类型为 `search / carousel / notice / quick_nav / category / product_list / player_recommend / activity / member_entry / support_entry`，数量上限 20，且**必须保留 `support_entry`（客服与订单入口）**，否则保存 / 发布返回参数错误。

发布走 `POST /admin/pages/:channel/publish`（body `{mode, scheduled_at, note}`）：`mode=immediate` 生成**不可变快照**并把旧生效版本转 `history`，`document.published_version_no` 指向新版本；`mode=scheduled` 要求 `scheduled_at` 晚于当前时间，先落 `status=scheduled` 待系统任务 `esports_publish_scheduled_pages` 到点激活。发布前校验草稿配置（组件类型合法、数量上限、必需入口存在）；发布状态为 `草稿 draft → 待发布 pending → 已发布 published → 历史版本 history`，同渠道同一时刻只生效一版。

回滚走 `POST /admin/pages/:channel/rollback`（body `{version_no}`）：以历史快照为模板**重新发布为新版本**（记录 `rollback_from_version_no`），不原地修改旧记录。发布记录走 `GET /admin/pages/:channel/versions`。素材库走 `GET/POST /admin/page-assets` 与 `DELETE /admin/page-assets/:id`（校验地址仅允许 `https://` 或站内相对路径）。客户端读取走 **公开** `GET /page?channel=uni_customer_home`（AuthPublic），只返回已发布配置与版本号，不含任何个人数据；未发布或读取失败时回系统默认模板（含搜索、快捷导航、商品列表与客服入口）。

未实现的 `ponytail` 项：拖拽画布与任意坐标、素材字节存储 / 上传 / 裁剪、外部域名白名单强校验（当前仅限协议与长度）、发布前图片可访问性 / 链接合法性 / 商品与活动有效性 / 文本超限校验（当前仅校验组件结构与必需入口）、按客户端能力下发的兼容快照（当前按单一渠道下发同一份配置）、多渠道与底部导航文案 / 图标在线配置。

### 客服端（§8）

客服端不新建端侧逻辑，**PC 工作台 / 移动客服端（uni-app）/ 企业微信侧边栏三个入口共用同一套接口与业务规则**，全部走 `AuthAdmin`（客服即管理员账号）。快捷报单复用既有 `POST /admin/orders`，订单跟进 / 待办复用既有订单与售后接口，业绩看板复用 `GET /admin/reports/cs_performance`，代客送礼复用 `POST /admin/gift-orders`。本模块新增的是**客户档案扩展、跟进记录、话术库与工作台聚合**：

- **客户档案（§8.3）**：在既有 `EsportsCustomer` 上扩展 `phone`（手机号）、`birthday`（生日，`MM-DD`，用于生日礼遇待办 §10.4）、`consumption_preference`（消费偏好）、`tags`（客户标签，JSON 数组，最多 20 个、单个 ≤30 字，写入时去空去重）、`source`（建档来源 `manual / wecom / self / import`）、`follow_status`（跟进状态 `new / following / pending / dormant / closed`）、`last_contact_at`（最近联系时间）。汇总走 `GET /admin/customers/:id/profile`，返回客户基本信息 + 累计充值（余额充值流水本金和）+ 累计消费 / 累计退款（已付款订单实付额与已退额）+ 累计送礼（礼物单据净额）+ 当前余额（本金 / 赠送分列，由流水求和派生）+ 成长值（成长值流水求和）+ 订单数，均为 SQL 聚合。编辑走 `PUT /admin/customers/:id/profile`。
- **跟进记录（§8.3）**：只增表 `EsportsCustomerFollowUp`（`customer_id` / `admin_id` / `channel`（`pc / mobile / wecom / phone / other`）/ `summary` / `next_follow_at`）。走 `GET/POST /admin/customers/:id/follow-ups`；写记录与刷新 `last_contact_at` 在同一事务内完成。记录人（`admin_id`）不随转交改变，离职交接后接手人据此快速了解客户。
- **企微自动建档（§8.4）**：走 `POST /admin/customers/wecom-intake`（body `{corp_id, external_id, display_name, phone}`）。以「企业主体 + 外部联系人标识」组合**幂等**（`(wecom_corp_id, wecom_external_id)` 唯一索引，非企微来源两字段为空 NULL，不参与冲突），已存在直接返回既有档案（`created=false`），并发出并发建档由唯一索引兜底。只填基础字段，不覆盖客服已维护的偏好与标签。
- **话术库（§8.2）**：表 `EsportsScript`（标题 / 分类 / 内容 / 排序 / 状态），走 `GET/POST /admin/scripts`、`PUT /admin/scripts/:id` 与 `POST /admin/scripts/:id/status`；编辑走 `version` 乐观锁，版本不符返回冲突。三个入口共用。
- **工作台聚合（§8.2）**：走 `GET /admin/cs/workbench`，一次返回我的客户数、我的订单池（沿用 `loadDispatchBoard` 的按状态分桶 + 公共池脱敏摘要）、待办提醒（待付款已超时、待结单、售后待处理、跟进已到期、储值卡临近到期、今日生日客户）与本人业绩（`cs_performance` 口径）。**数据隔离**：非超级管理员一律只看归属本人（归属客服 / 负责订单 / 本人处理的售后）的数据，档案与跟进访问走 `loadOwnedCustomer` 校验归属，越权返回 403。

已实现**公共池脱敏（§5.1 / §8.2）**：排单看板 `GET /admin/dispatch/board` 与客服工作台 `order_pool` 的公共池对非超管一律掩码客户身份与金额字段（`customer_id` 与各 `*_cents` 清零），保留订单号 / 商品 / 席位 / 状态 / 时间等可供认领判断的非敏感字段，并在响应新增 `masked_public_pool_ids` 列出被掩码订单 ID 供前端以「***」展示；超管仍看完整数据。工作台公共池摘要本就只展示订单号与履约状态，无需额外裁剪。

未实现的 `ponytail` 项：企微侧边栏 H5 页面与「自动识别当前客户 / 发送到会话」的宿主能力未接入，仅提供服务端接口；话术「发送到会话」的通道能力未接入；陪玩推荐（§8.4 P2）未实现。（待办中的「储值卡临近到期」已随 §4.4 储值卡落地、「提成」列已随 §12.2 客服提成落地，见后两节。）

### 客户风控（§13.7）

- **风险标记**：`EsportsCustomer` 新增 `risk_status`（`normal` 正常 / `watch` 疑似滥用复核 / `restricted` 限制自助下单）、`risk_reason`、`risk_marked_at`。管理端「风险客户」独立菜单（`esports:risk:menu`）列出被标记客户（`GET /admin/risk-customers`），管理员可 `POST /admin/customers/:id/risk` 标记 / 解除（标记须填原因；`normal` 会清空原因与标记时间）。
- **只标记复核、不自动处罚**：定时任务 `esports_flag_high_risk_customers` 把高频退款客户标记为 `watch`——口径为窗口内「已完成且 `refund_cents > 0`」的售后单按客户归集次数与退款总额，任一达阈值即标记并写 `customer.risk_flagged` 外盒；已是 `restricted` 的客户**不会被自动降级**，受理与申诉入口照常。阈值配置 `app.esports.risk_window_days`（默认 30）/ `app.esports.risk_refund_count`（默认 3）/ `app.esports.risk_refund_cents`（默认 50000 分）。
- **限制自助下单**：只有 `restricted` 会在自助入口拦截——新增客户自助下单 `POST /api/v1/esports/user/orders`（须已绑定登录身份、成年且未被限制；归属客服作为订单负责客服，幂等键按 `customer:<id>` 隔离），被限制时返回「限制自助下单，请联系客服或先预付」；`watch` 不拦截。
- **赔付一律走系统流程**：系统不提供私下转账入口，退款走售后（§13.3）、由陪玩承担时走追偿单（§13.4）。

未实现的 `ponytail` 项：风险判定只有退款次数与退款金额两个启发式口径（未纳入投诉率、退单率、设备或联系方式关联等维度）；「要求预付」目前只体现在拦截提示语，未接入独立预付流程；客户自助端（uni-app）尚未建，客户自助下单目前只有服务端接口。

### 储值卡（§4.4 / §11.3）

储值卡是**独立预付账户**，不复用订单类型：`EsportsStoredValueCard` 登记卡号、合同确认、有效期、适用范围与开卡本金 / 赠送权益，卡内余额不落库，由 `EsportsWalletTx` 中 `account_type=stored_value_card`、`owner_id=卡 ID` 的流水求和派生（每张卡就是一个账户，`owner_id` 为卡 ID、归属人由卡上的 `customer_id` 得出，该类型不为卡建 `EsportsWalletAccount` 登记行，避免卡状态两处真相）。本金与赠送分列，预付款批次另记 `EsportsLedgerEntry`（`account_type=stored_value_prepay`，`account_id=卡 ID`），对应 §11.6 的「来源批次」追溯。

- **开卡（§4.4）**：`POST /admin/stored-value-cards`。外部收款先生成零余额的待核验卡；独立财务核对到账流水号和实收金额后调用 `POST /admin/stored-value-cards/:id/payment-verification` 激活，届时才写卡内余额与预付款批次。开卡申请人不能自审，同一到账流水只能激活一张卡；以有效天数开卡时从实际激活日计算期限。用余额购卡仍在同一事务划转本金和写配对流水，且不接受赠送权益。开卡必须确认合同；幂等键由卡上的唯一 `open_request_key` 约束。
- **消费（§4.4）**：`POST /admin/orders/:id/pay-by-stored-value-card`。报单时选择用储值卡足额核销，校验「卡属该客户 + 状态可用 + 在有效期内 + 商品命中适用范围」，扣减后置订单 `payment_channel=stored_value_card` 与 `stored_value_card_id`，并走完名额占用；余额用尽自动转 `exhausted`。
- **标记与一致性（§3.1 / §4.6）**：付款方式与关联卡是**订单上的独立字段**（储值卡不作为订单类型）。结单前校验「标记 / 关联卡 / 核销流水」三者一致，缺任一即拒绝结单。
- **退款两层（§4.4）**：订单退款与退预付款是两层资金动作、各有额度上限、不重复退现金。售后退款按**原账户原路退回**（储值卡订单退回原卡并冲回批次账本，而非客户余额）；退卡只处理未消费预付款。
- **退卡（§4.4）**：`GET /admin/stored-value-cards/:id/refund-quote` 试算（可退 = 未消费本金，未消费赠送按合同作废不折现），`POST .../refund-request` 申请并快照试算明细，`POST .../refund-decision` 由财务审核——**申请人不得自审**，通过时写 `refund_return` 冲减本金与赠送并置 `refunded`，驳回按余额与有效期还原为可用状态。
- **余额查询与到期（§4.4）**：`GET /admin/stored-value-cards` 支持按客户 / 状态 / `remind_days`（临近到期）查询，`GET /admin/stored-value-cards/:id` 返回卡信息 + 派生余额 + 流水。`esports_expire_stored_value_cards` 任务把已过有效期的可用卡标记为 `expired`，并**在同一事务内作废未消费赠送权益**（写 `entry_type=expire` 的赠送负数流水），**未消费本金保留**仍走退卡，避免到期直接没收预付款；赠送作废随「首次到期」（状态 `active` → `expired`）只执行一次，重复运行不再处理。
- **套餐（§4.4）**：`GET/POST /admin/stored-value-packages`、`PUT /admin/stored-value-packages/:id`、`POST /admin/stored-value-packages/:id/status`。套餐预设「本金 + 赠送」档位，`code` 唯一、编辑走 `version` 乐观锁、可单独上下架；开卡入参 `package_id` 选定**在售**套餐时，本金 / 赠送由套餐决定并写入卡 `package_id` 追溯（下架套餐开卡被拒）。开卡赠送额的来源优先级为**套餐值 > 手填值 > 会员等级比例**（§10.4：未选套餐且未手填时按 `本金 × prepay_bonus_bps / 10000` 计算；余额购卡不赠送）。

已实现**批次级逐笔扣减（§4.3 / §11.3）**：新增只增表 `EsportsLedgerAllocation`，记录「每笔消费（`consume_source_*`）动用了哪些正向批次（`batch_source_*`）各多少金额」。充值写入 `recharge_batch` 正向批次，余额消费与储值卡消费按批次 FIFO 分摊并落明细；`GET /admin/ledger-allocations?account_type=&account_id=` 查询分摊明细。

已实现**扣减顺序可配置（§4.5）**：系统配置 `app.esports.prepay_deduction_order` 决定本金 / 赠送的拆分口径——`bonus_first`（默认，先赠送后本金）/ `principal_first`（先本金后赠送）/ `proportional`（按本金 / 赠送剩余额比例分摊，本金向下取整、尾差归赠送）；缺失或非法值回落 `bonus_first`。余额核销、储值卡核销与增值服务落账共用同一拆分函数，拆分之和恒等于应付额。

已实现**到期作废赠送权益（§4.4）**：`esports_expire_stored_value_cards` 首次把到期卡置 `expired` 时同事务作废未消费赠送（`entry_type=expire` 负数流水），本金保留待退卡。

未实现的 `ponytail` 项：扣减顺序的「按比例」目前只作用于**本金 / 赠送拆分**，批次分摊仍按批次 FIFO、不做「按比例」分摊；到期**不自动发起退卡待审**（仅作废赠送、保留本金，退卡仍需人工申请）；礼物与打赏的「合格预付本金」尚未把储值卡本金纳入可选来源；开卡是否计入财务收款回执（`EsportsPaymentReceipt`）未接线，凭证以 `payment_ref` 与批次账本留痕。

### 客服提成（§12.2）

客服提成与陪玩分成**同结构、不同主体**：`EsportsCsCommissionRule`（提成规则）+ `EsportsCsCommissionEntry`（提成明细），规则同样不可原地修改，改规则＝新增递增 version 的新行、旧行转 history（§12.4）。挂在既有「结算」菜单下，不新增顶层菜单。

- **规则（§12.2）**：`GET/POST /admin/cs-commission-rules`、`POST /admin/cs-commission-rules/:id/activate`、`POST /admin/cs-commission-rules/:id/rollback`。适用范围 `global / admin`（admin 层优先于 global，同层取最高版本）；提成基数 `order_amount`（扣除结单前退款后的报单金额）/ `order_contribution`（再扣陪玩分成后的可分余额）/ `order_count`（每单 1）；计费 `percent`（万分比，可叠加阶梯，周期累计净基数分档）或 `fixed_per_order`（每单固定额）；提成周期 `daily / weekly / monthly` 决定阶梯累计窗口。单量基数 + 按比例会在建规则时直接拒绝（恒零提成）。
- **计提（§12.2）**：结单时在 `closeOrder` 同一事务内、陪玩分成之后计提；**提成只在订单完成后生成**，未开工取消单不会走到结单；**结单前已发生的退款从最终计提基数扣除**；`ResponsibleAdminID` 为空（无业绩归属客服）不计提。上限：陪玩分成 + 客服提成不得超过可分基数，超出则截断并在备注注明（§12.1，渠道成本未实现）。
- **阶梯（§12.2）**：按「提成周期累计净基数（含本单）」匹配最高档；累计口径取该客服本周期内已计提明细的基数之和（含负数冲回）。
- **退款冲回（§12.2）**：结单后部分退款按 `提成额 × 退款额 ÷ 应付额` 冲回，**全额退款把未冲回的余额一次冲至零**；新增 `kind=reversal` 负数明细，退款金额落在退款当期（§12.6）；幂等靠 `idem_key` 唯一索引兜底。
- **结算与发放（§12.2 / §12.6）**：`POST /admin/cs-settlements` 生成**独立客服提成结算单**（`EsportsSettlement.owner_type=cs`，按周期汇总待结算明细与负向冲回），**不写猫粮钱包**——客服提成发放是财务打款：`POST /admin/settlements/:id/lock` 锁定后由 `POST /admin/settlements/:id/pay` 登记发放并留外部打款凭证（`payout_ref` / `paid_by_admin_id` / `paid_at`，仅 `paid` 时非空）。
- **明细与概览（§12.7）**：`GET /admin/cs-commission-entries` 查每笔订单的提成方式与数值、基数、提成金额与状态（含渠道成本快照 `channel_cost_cents` 与工作室剩余快照 `studio_remainder_cents`）；`GET /admin/cs-commission/summary` 返回待结算净额、已结算合计与本月累计（不传 `admin_id` 看本人）。
- **渠道成本与工作室剩余反算（§12.1）**：订单新增 `channel_cost_cents`（登记接口 `PUT /admin/orders/:id/channel-cost`，非负且不超过应付，结单后不可改）；结单计提客服提成时**可分基数 = 服务应付 − 陪玩分成 − 渠道成本**，并在 service 明细快照渠道成本与**工作室剩余 = 可分基数 − 客服提成**（百分比提成的舍入尾差自然落入工作室剩余，等价尾差单列）。
- **业绩看板提成列（§8.2 / §17）**：`cs_performance` 报表新增 `commission_cents`（区间内该客服提成明细净额），客服工作台与「报表 → 客服业绩」均展示。

已实现**自动生成结算单（§12.2）**：定时任务 `esports_generate_cs_settlements` 为各客服按**上一完整周期**自动生成草稿结算单——周期取该客服生效规则的 `daily / weekly / monthly`（`csCommissionPreviousPeriodWindow` 由当前周期起点回退一个周期算出），只处理「上一周期内有待结算明细」的客服，同客服同周期已存在结算单则跳过（幂等）；**不触碰进行中的当前周期**，锁定与登记发放仍由财务操作。

未实现的 `ponytail` 项：「按提成周期累计净基数分档」是**顺序相关**的——档位按计提时点的周期累计判断，不做事后重算；「单笔手动指定」优先级未实现；`effective_at` 调度生效未做（仅以 `status=active` 判定）；自动结算单生成后不自动锁定（需财务确认后锁定）。

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

### 结单表单、提交规则与审批流（§3.4）

把结单时填写的内容做成「字段库 + 模板」，并按 全局 / 游戏分区 / SKU + 金额区间绑定到订单；提交规则规定结单前「陪玩个人服务段上报」需多少成员完成；审批流则把「待复核」做成可配置的串行节点。管理端挂「结单表单」独立菜单（`esports:close_form:menu`）。

- **字段库**（`EsportsCloseFormField`）：`GET/POST /admin/close-form-fields` 建档，`PUT /admin/close-form-fields/:id` 原地编辑（乐观锁 `version`），`POST /admin/close-form-fields/:id/status` 启停。字段 `key` 为小写字母开头、仅含小写字母 / 数字 / 下划线的唯一标识，创建后不可改；`field_type` 覆盖 §3.4 自定义类型（`text` / `number` / `money` 金额只读 / `select` / `multiselect` / `datetime` / `image` / `switch` / `reference`）；单选与多选必须配置选项（写入时去空去重），`text` 可设 `max_length`（`0` = 不限）。字段库**不做版本化**，停用后新模板不可再引用、已生效模板不受影响。
- **模板**（`EsportsCloseFormTemplate`）：`GET/POST /admin/close-form-templates`、`PUT .../:id`（仅草稿可改，乐观锁）、`POST .../:id/activate`（同 code 现有 active 转 history）、`POST .../:id/rollback`（以历史版本为模板生成 max version + 1 的直接 active 行，原历史行不可变，§12.4）。模板 `fields` 是字段绑定列表（`field_key` + 该模板下的必填覆盖 + 排序），引用不存在或已停用的字段会被拒。
- **解析**：`resolveCloseFormTemplate` 按 `sku` > `game` > `global` 选层，同层取**命中金额区间**（`min_cents` ≤ 订单应付，且 `max_cents` 为 0 或 应付 < `max_cents`）且 `version` 最大的一条；无命中则不校验、不留痕。
- **结单校验与留痕**：`POST /admin/orders/:id/close` 正文新增 `form_values`（key = 字段库 key）。结单事务内按生效模板逐项校验——必填项缺一不可、`select` / `multiselect` 取值须在选项内、`text` 不超 `max_length`、`datetime` 须为 RFC3339；`money` 字段由系统按订单 `payable_cents` 自动填写、不取提交值。校验通过写一条 `EsportsCloseFormSnapshot`（订单 / 模板编号与版本 / 字段值 / 提交人与备注），同一订单可有多条（复核驳回后可重新结单）。管理端 `GET /admin/orders/:id/close-form` 返回订单适用模板、字段定义与最近一次快照，供结单弹窗渲染。

- **提交规则**（`EsportsCloseSubmitRule`）：`GET/POST /admin/close-submit-rules`、`PUT .../:id`（仅草稿）、`POST .../:id/activate`、`POST .../:id/rollback`（版本管理同结单规则）。口径：服务段 `status=finished` 且（上报服务量 > 0 或已填服务凭证）视为该成员「已提交」。`mode`：`single` 任一成员提交即可（需 1）／`multi` 所有实际服务成员逐人提交（需全部）／`custom` 按 `min_submit_ratio_bp` 向上取整（`0` = 不要求）；`allow_substitute` 允许客服在结单时代未上报成员补填服务量与凭证（写回服务段并留 `order.service_submitted_by_admin`）；`on_missing`：`block` 未达标禁止结单／`remind` 放行并写 `order.close_submit_remind` 事务外盒／`auto_accept` 放行并留 `order.close_submit_auto_accepted`。结单正文可带 `substitute_reports`（`seat_index` + `reported_units` + `evidence_ref`）。解析按 `sku` > `game` > `global`，同层取 version 最大。

- **审批流**（`EsportsCloseApprovalFlow` + `EsportsCloseApproval`）：`GET/POST /admin/close-approval-flows`、`PUT .../:id`、`POST .../:id/activate`、`POST .../:id/rollback`（版本管理同结单规则）。按 全局 / 游戏分区 / SKU 绑定，并以 `min_cents` 作为触发阈值（`0` = 所有结单都需审批）。`nodes` 是串行节点：`seq` 从 1 连续递增、`role` 为 `admin`（任一管理员，不得自审）/ `super_admin`（超级管理员）、`timeout_hours`（0~720，0 = 不限）、`auto_pass_on_timeout`、`involves_funds`；**涉及资金 / 争议的节点禁止自动通过**（建流 / 改流时直接拒绝）。订单结单命中生效流即建立审批进度 `EsportsCloseApproval`（`steps` 记录每个节点的处理人 / 时间 / 意见 / 是否自动通过），**逐个节点推进、末节点通过才真正结单**；未配置审批流时回落结单规则的 `review_threshold_cents`，行为等价内置单节点「管理员复核」（`flow_id = 0`）。`GET /admin/orders/:id/close-approval` 返回当前待审批节点；`POST /admin/orders/:id/close-approve` 推进当前节点（中间节点写 `order.close_approval_node_approved` 并推进订单版本），`POST /admin/orders/:id/close-reject` 驳回并回退待结单（审批进度转 `rejected`，重新结单时重建）。任务 `esports_check_close_approval_timeout` 处理节点超时：允许自动通过的节点超时自动推进（`auto_passed = true`），其余写 `close.approval_timeout` 外盒升级（幂等键含订单 / 节点 / 阈值）。

未实现的 `ponytail` 项：拖拽画布与任意坐标、字段级联与条件显示、字段库版本历史、按订单类型（即时 / 预约）绑定模板、模板在**下单时**快照（当前在结单时解析生效模板并记版本号）、同一层多模板叠加（同层只取版本最高的一条）；提交规则未接入「陪玩端自助提交结单表单」（首版由陪玩 `service/finish` 上报服务段、客服手动结单）；审批流只支持串行节点，并行 / 条件分支、按具体角色枚举（当前仅管理员 / 超级管理员）与超时自动升级角色未实现。

### 优先派单权与独占优先通知（§5.4 / §5.5 首版）

`EsportsDispatchPriorityGrant` 是「优先派单权」名单：按**游戏分区 + 周期**授予。业务口径为**独占优先通知**——订单先只给名单内陪玩，名单内全部拒绝或通知超时才释放给所有人。

> 口径来源：功能列表 §5.4 已按业务方澄清改为**独占优先通知**（原文「排序加权、不是独占、抢单大厅不受影响」已废弃），§2.1 / §5.2 / §5.5 / §5.6 / §5.8 / §5.9 / §6.1 的相关表述同步修正。硬性条件仍与人工指派一致（资质与在线）。

- **名单（§5.4）**：`GET/POST /admin/dispatch/priority`、`POST /admin/dispatch/priority/:id/revoke`。周期支持日 / 周 / 半月 / 月（半月为每月 1–15 日与 16 日至次月 1 日）；每游戏每期名额上限取系统配置 `app.esports.dispatch_priority_quota`（缺失回落 3）；获得方式 `ranking / points / threshold / rotation / manual`；`next_period=true` 可在切换点前预生成下一期名单。重复录入同一陪玩、超出名额、非法周期/来源/排名都会被拒（判重先于判名额，报错更精确）。移出名单须填原因，移出后名额立即释放。
- **周期重算**：`esports_rebuild_dispatch_priority` 任务把已过期的生效名单转 `expired`、把到点的待生效名单转 `active`，并为下一期预生成名单（延续本期生效名单、按排名、`source=rotation`、状态 `pending`）；下一期已存在名单时不覆盖、不重复生成。
- **开窗与通知（独占）**：订单进入待派单时统一走 `onOrderEnteredPendingDispatch`（资金核销、席位拒绝/撤回/过期回退等全部路径都已接入）。若订单 SKU → 分类能解析出游戏分区且该分区有生效名单，则开窗：订单记 `priority_notice_status=pending`、`priority_notice_until=now+app.esports.dispatch_priority_notice_minutes`（缺失回落 3 分钟）、`priority_game` 快照，并按名单逐人写 `EsportsDispatchPriorityNotice`（`notified`）与事务外盒事件 `order.priority_notice`。窗口内**只有被通知的人**在抢单大厅看得到这张单（`listGrabHall` 只对他的名单外订单做过滤）；其他人看不到、直接抢会被拒（403「该订单正在优先通知名单内陪玩，暂未对大厅开放」）。解析不到游戏分区或该分区没有名单时不开窗，订单留在待派单池由客服指派。
- **接受与闭合**：名单内陪玩走既有 `POST /user/player/orders/:id/grab` 接单——窗口内该接口额外放行「被通知的本人」，接单成功后订单直接转 `accepted`、本人通知转 `accepted`、其余待回应通知作废、订单 `priority_notice_status=accepted`。
- **拒绝与释放**：`GET /user/player/dispatch-notices` 看我的优先通知，`POST /user/player/dispatch-notices/:id/decline` 拒绝（只能拒自己的通知）。名单内仍有人待回应时订单留在待派单；**最后一人拒绝**时订单释放到抢单大厅（`fulfillment_status=grabbing`、`priority_notice_status=released`、原因「名单内全部拒绝」）。`esports_expire_dispatch_priority_notice` 任务把超过截止时间的窗口同样释放到大厅（原因「优先通知超时」）。释放后不再重复通知、名单不再有特权，任何人可抢。
- **客服干预**：`GET /admin/orders/:id/priority-notices` 看谁被通知/拒绝/接单；`POST /admin/orders/:id/release-priority` 由客服提前释放到大厅（§5.2「可随时人工干预」）；客服指派不受窗口限制，指派成功后窗口自然闭合。
- **派单候选（§5.5 首版）**：`GET /admin/orders/:id/dispatch-candidates` 列出**当前可派单对象**——窗口内只列被通知的名单内陪玩，窗口外列全部合格在线陪玩；硬性条件与人工指派门槛一致（资质 `qualified` 且在线），已在本订单席位上的陪玩不再列出。排序为：指定陪玩（订单 `preferred_player_id` 优先，接口参数 `preferred_player_id` 可覆盖；命中者标记 `Preferred` 置顶）→ 评分高者优先（**样本不足不当作低分**）→ 近 7 天在途席位数少者优先（轮转均衡）→ 档案 ID 兜底。

已实现 §5.6 的独立发布动作「发布到抢单大厅」（`POST /admin/orders/{id}/publish-grab`）：把已付款待派单的**单陪玩**订单直接转 `grabbing`（多人订单返回参数错误并提示改用指派），窗口仍开着时等价于「提前释放」；发布前把被撤回 / 过期的席位重置为 `open`，保证抢单大厅一定有可抢席位，并留 `order.published_to_grab_hall` 事件与事务外盒。

已实现 **「客户指定陪玩的订单跳过优先通知窗口」（§5.4）**：订单新增可空字段 `preferred_player_id`，报单时可选填并校验陪玩资质（非 `qualified` 返回参数错误，§4.6）；`onOrderEnteredPendingDispatch` 对指定单**不开优先通知窗口**，订单留在待派单池由客服指派；派单候选默认回落订单上的指定陪玩并置顶标记 `Preferred`。

已实现 **§5.9 的等待时长埋点**：订单新增 `first_pooled_at` / `first_invited_at` / `first_accepted_at` / `service_started_at` / `priority_released_at`（各自只写一次、永不改写），统一在进入待派单（`onOrderEnteredPendingDispatch`）、派出邀请、有效接单、开工与优先通知释放处落点；派单效率报表（`dispatch_efficiency`）据此派生 `avg_pool_to_invite_seconds` / `avg_invite_to_accept_seconds` / `avg_accept_to_start_seconds` / `avg_pool_to_start_seconds` 四类平均等待时长（时间点不全的历史订单不计入样本）。

已实现**接单上限（§5.5 / §6.4）**：陪玩档案新增 `daily_limit`（每日可履约席位数，按接单自然日）与 `concurrent_limit`（并发可履约席位数），在**指派、接受邀请、抢单**三处接单动作原子硬校验（超限返回参数错误并说明上限，`0` = 不限）；候选池未按上限过滤，仍在接单瞬间拦截。

已实现**游戏分区权限 + 技能等级（§5.5）**：陪玩档案新增 `games`（接单游戏分区列表，空 = 不限），SKU 新增 `required_level`（所需技能等级，报单时快照到订单）；在**指派、接受邀请、抢单**三处接单动作与**派单候选池**统一硬校验「陪玩已开通该分区 + 陪玩等级不低于订单所需等级」，不满足返回 403 / 从候选池剔除。

未实现的 `ponytail` 项：排名制 / 积分制 / 达标制的**自动计算**未接入（缺少排名、积分与达标数据源），自动重算只做「上一期名单延续到下一期」，人工指定与人工增减不受影响；历史合作、客户偏好、距离或时区等加分项未实现；真实推送通道未接入（写事务外盒事件 + 陪玩端轮询优先通知列表）；评分按每位候选单独计算，候选量级大时应改为一次聚合。

### 陪玩固排（§5.3 首版）

- **固排关系**：`EsportsTeammate` 是陪玩之间按游戏分区互加的固定队友关系（无方向，规范化存为 `player_id < teammate_player_id`，仅以 `initiator_player_id` 记录发起方）。双方确认后建立（`confirmed`），任一方可解除（`dissolved`）；邀请状态 `pending`。
- **陪玩端接口**：`POST /user/player/teammates` 发起（须指定对方陪玩档案 ID、游戏分区与 8–128 位幂等键）；`GET /user/player/teammates` 我的固排；`GET /user/player/teammate-requests` 待我确认的邀请；`POST /user/player/teammate-requests/:id/accept` / `.../decline` 确认 / 拒绝（仅被邀请方可操作）；`POST /user/player/teammates/:id/dissolve` 任一方解除；`GET /user/player/players?keyword=` 按昵称搜索可添加的陪玩（排除自己）。
- **约束**：同一对陪玩在同一游戏分区只允许一条「待确认 / 已建立」关系（重复发起 `errConflict`，幂等键重放返回既有记录）；自己加自己、被邀请方之外的操作均被拦截；解除 / 拒绝后可重新发起。
- **派单侧成队（§5.3）**：`GET /admin/orders/:id/teammate-candidates?main_player_id=` 列出主陪玩在该订单上可一起成队的合格固排；`POST /admin/orders/:id/team-dispatch`（`main_player_id` + `teammate_player_ids`）主陪玩占第一个空席位、选中固排依次占后续空席位，**同一事务一次成队**，席位 `source=fixed_team`、状态 `invited`，订单转待接单并写一条 `order.team_dispatched` 事件。硬性条件与人工指派一致（资质 + 在线 + 分区/等级 + 接单上限），且固排必须是与主陪玩在该分区已建立的关系；空席位不足 / 非固排 / 不合格均被拒。

未实现的 `ponytail` 项：固排成队只在管理端「席位」抽屉提供（按席位序依次邀请）；陪玩端「主陪玩主动从固排列表邀请队友」的宿主入口未接入（仅提供关系管理）；固排关系建立不校验双方资格与在线状态（仅需对方陪玩档案存在）。

### 陪玩转单与催单（§5.7 首版）

- **陪玩转单（§5.7）**：`EsportsSeatTransferRequest` 是转单申请单。陪玩端 `POST /user/player/orders/:id/transfer`（须 8–128 位幂等键）发起——仅本人在该订单上「席位为 `accepted` 且本单没有服务段」时可发起，订单须处于待接单 / 已接单；同一席位同时只允许一条待确认申请（重复提交同幂等键返回既有申请，换键则冲突）。**确认前不动席位与订单**：避免陪玩单方面退出占位。管理端 `GET /admin/seat-transfers` 查看、`POST /admin/seat-transfers/:id/decision` 确认：通过时席位回到 `open` 并清空陪玩（待补位），按剩余有效席位把订单退回 `pending_acceptance`（还有队友占位）或 `pending_dispatch`（无人占位，并调用 §5.4 的 `onOrderEnteredPendingDispatch` 重开优先通知）；驳回须填原因且保持原状。陪玩端 `POST /user/player/transfer-requests/:id/cancel` 可在确认前撤回。
- **催单时间阶梯（§5.7）**：`app.esports.remind_ladder_minutes`（逗号分隔、升序、去重，缺失或非法回落 `5,15,30`）。`esports_remind_dispatch_ladder` 任务对「待接单」订单的邀请席位，按邀请发出时间（由 `expires_at − 邀请时长` 反推）算已过分钟数，取满足的最大档位并补齐所有未发过的档位：第 1 档 `dispatch.remind_player` 提醒在线陪玩，第 2 档起 `dispatch.remind_admin` 升级提醒客服。幂等靠事务外盒 `event_key=dispatch_remind:<order>:<seat>:<level>` 唯一索引，因此重复运行只补发新档位。
- **服务超时提醒（§5.7）**：`app.esports.service_overtime_minutes`（0 表示关闭）。`esports_check_service_overtime` 任务对「服务中」订单的在服务服务段计算有效时长（`now − started_at − paused_seconds`），超过阈值即写 `dispatch.service_overtime` 事务外盒与 `order.service_overtime` 订单事件，提醒客服确认是否加钟；幂等键含阈值，调整阈值后可重新提醒。

未实现的 `ponytail` 项：**已开始服务后的转单**属售后补履约范畴（§13.6），首版直接拒绝并提示走售后（补履约任务只由售后方案「补钟 / 换人」产生，见 §13.6；转单通过后只把席位放回待补位）；**服务超时只提醒、不自动发起加钟**（加钟由客服在订单页手动发起、陪玩确认，见 §6.2 / §12.5）；**订单与 SKU 未声明每单位时长**，「服务时长超过订单约定」暂用可配置阈值近似，按承诺服务量换算时长与差异化口径未实现；催单渠道只有事务外盒（系统内消息），企微应用消息与电话 / 语音未接入；转单不校验「转出方是否仍需承担本单责任」，责任判定未实现。

## 开发环境人工检查入口

启动进程前设置 `CMF_APP_ENV=development` 与 `ESPORTS_EXPERIMENTAL=1`。用管理员为员工分配上述菜单与按钮权限。

- 商品：进入“商品”菜单，新增产品分类后新增商品并绑定分类；编辑商品、上下架；删除已被商品引用的分类应失败。
- 钱包：进入“钱包”菜单，为客户归属人充值（本金 + 赠送），余额应等于流水之和；重复提交同一幂等键应失败；调整不得把任一子账户调成负数。
- 订单：在“订单”菜单对未支付订单改价（份数变化应同步占用名额、旧报价版本作废）、改期、取消（名额释放）、余额核销（余额足够时转入待派单，余额不足应被拦截）。存单：仅**已付款**订单可点「存单」把订单冻结存起来，点「恢复」清掉标记；存单只是订单上的一个冻结标记，**不改变履约状态、不释放名额占用**，未付款单、已结束 / 已取消单、重复存单均被拒绝。加钟：对**服务中**订单点“加钟”填追加金额 / 追加服务量与备注（金额与服务量至少一项为正）后生成“待陪玩确认”的加钟单；非服务中订单、待结单订单发起与同一订单重复发起均应被拒。用陪玩身份调 `POST /api/v1/esports/user/player/addons/{id}/confirm` 确认：订单 `addon_cents` / `addon_units` 累加且 `payable_cents` 不变，按确认时点生效的分成规则生成 `kind=addon` 的分成明细（如全局 50% 规则 + 加钟 40 元 → 2000 分，基数记 4000）；非本单陪玩确认返回 403，重复确认失败。拒绝须填原因且订单不累加（写 `order.addon_rejected`）。订单存在“待陪玩确认”加钟时结单被拒，客服“撤回”该加钟后可正常结单。加钟收款：确认后对该加钟单点“余额收款”，应收追加金额应从客户余额扣减（含批次分摊）、加钟单转“已收”、留 `order.addon_paid` 事件；客户余额不足、加钟未确认或重复收款均应失败。加钟退款分摊：对有加钟分成明细的订单走一遍售后部分退款，到“结算 → 分成明细”应同时看到 `service` 与 `addon` 两条同比例负数冲回。取消政策（§3.3）：把 `app.esports.cancel_free_hours` 设为 2、`app.esports.cancel_fee_bp` 设为 5000，对一张已付款待派单订单调 `GET /api/v1/esports/admin/orders/{id}/cancel-preview`：付款后 2 小时内应显示免费取消、应退 = 应付；把订单 `paid_at` 回拨 3 小时后再查询应显示手续费 = 应付的一半、应退减半。此后点“取消”应转“已取消”、SKU 已售名额退回（占用记录转 `returned`）、该单未终结席位作废，并在“售后”菜单看到一条 `退款申请` 售后单（来源为该订单、渠道为客服），退款按该单审批与执行；对已有服务段的订单点“取消”应被拒并提示走售后。
- 报单：为两个不同员工分配 `esports:age_checks:create`、`esports:age_checks:list`、`esports:age_checks:review` 及对应按钮权限，提交人与复核人必须不同。进入“报单 → 客户”，创建客户并上传证据至 `private/esports/age-checks`，提交年龄核验；另一人到“年龄资格复核”查看证据、批准或驳回。批准后客户 `age_status` 及 `version` 更新，拒绝不改变资格；重复提交、本人自审、旧版本审批和删除引用中的附件均应失败。报单时可选填“指定陪玩”（陪玩档案 ID）：填 `qualified` 陪玩可正常下单，该订单进入待派单后**不开优先通知窗口**（派单候选中本人置顶）；填未通过考核或已停用的陪玩应返回参数错误（§4.6）。
- 排单：进入“排单”菜单查看看板（在线陪玩数、待派单池/公共池/抢单中/待接单/已接单/服务中/待结单/异常分桶）。对已付款待派单订单“指派”陪玩（陪玩须 `qualification=qualified` 且 `presence=online`），席位转 `invited` 且订单转待接单；从“席位”抽屉撤回邀请后订单退回待派单；公共池订单可“认领”，重复认领应失败；转单改变负责客服；手动催单仅留痕（自动阶梯催单见下条）。邀请有效期取系统配置 `app.esports.dispatch_invite_ttl_minutes`（缺失按 10 分钟），超时由 `esports_expire_dispatch_invites` 任务回收。对没有优先名单（或窗口已闭合）的已付款待派单单陪玩订单可点“发布抢单”：订单转“抢单中”，此后任意合格陪玩在抢单大厅可见并可抢；重复发布、未付款单与多人订单均应失败（多人订单提示改用指派）；若该单此前有被撤回 / 过期的席位，发布后席位应重置为“待接单”的前置状态 `open`。会员优先：把某会员等级的 `priority_dispatch` 设为开启并把某客户挂到该等级后，其待派单订单应置顶于看板“待派单池”（公共池同理）并在 `member_priority_order_ids` 中返回；把该等级关闭 `priority_dispatch` 后应不再置顶。接单上限：在“陪玩”编辑里设「并发接单上限=1」保存后，该陪玩已有 1 个 `accepted` 席位时，再对其指派、接受邀请或抢单均应返回参数错误（提示并发上限）；设「每日接单上限=1」后，当日接满 1 单再接单同样被拒；把上限改回 0（不限）后恢复正常。游戏分区 + 技能等级：在“商品”编辑里把某 SKU 的「所需技能等级」设为“资深”，在“陪玩”编辑里给陪玩设「接单游戏分区」为不含该订单所属分区、或等级低于“资深”，再对其指派该 SKU 订单应返回 403（提示未开通分区 / 等级不足）；把陪玩分区与等级设到满足条件后应可正常指派，且“派单候选”中不再出现不满足条件的陪玩。
- 陪玩：进入“陪玩”菜单可搜索陪玩档案、新建（`user_id` + 昵称）与编辑（昵称 / 资格 / 在线状态）。陪玩端自助接口面向 uni-app 客户端（当前无独立前端页面），可用登录用户身份调用：`dashboard` 查看在线状态与待接邀请/进行中订单/当日猫粮收入；`presence` 上下线；`invites` 列表并 `accept`/`reject`（`:id` 为席位 ID，接受在唯一席位接满时订单转 `accepted`，拒绝在无剩余席位时订单退回 `pending_dispatch`）；`grab-hall` 列出 `grabbing` 单席位订单并 `grab`（合格陪玩，人工下线仍可抢单）；`orders` 按 `bucket` 查看我的订单；`service/start` 开始服务（订单 `accepted`→`in_service`），`service/finish` 结束服务（记上报服务量与系统秒数，全部席位结束后订单转 `pending_close`）。心跳（§6.4）：用陪玩身份调 `POST /api/v1/esports/user/player/heartbeat` 应只刷新 `last_heartbeat_at`、不改变 `presence`；把 `app.esports.player_heartbeat_timeout_minutes` 设为 10 并把该陪玩的 `last_heartbeat_at` 回拨 30 分钟后跑 `esports_auto_offline_players`：无在途订单应转 `offline`，若其在待接单 / 已接单 / 服务中订单上持有已接单席位则应转 `protected_abnormal` 并写一条 `player.heartbeat_timeout` 外盒；重复跑不再处理。服务售后申诉（§13.2）：用本单服务成员身份调 `POST /api/v1/esports/user/player/order-after-sales`（填 `order_id`、`kind`、`demand`）应建立待受理售后单；非本单成员返回 403，`kind` 填 `refund_request` 返回参数错误。
- 结单：进入“订单”菜单，对 `pending_close` 订单点“结单”填写备注。未上传服务凭证或服务时长为零时结单失败并提示具体原因，订单保持待结单；成功则订单转 `completed`、写入 `closed_at` 与 `order.closed` 事件，重复结单失败。结单规则：进入「结算 → 结单规则」新建规则（如全局、凭证 2 张、复核阈值 10 元）并“生效”：凭证 1 张的订单结单应失败（提示凭证不足）；应付达 10 元且由负责客服本人结单应失败，换管理员结单应成功；“回滚”历史版本应生成更高版本并直接生效。结单复核：金额达复核阈值结单时订单转“待复核”（`pending_close_review`）并留 `order.close_review_requested`，**不**直接完成；负责客服本人复核应返回 403（不得自审），换管理员“复核通过”后订单 `completed` 并计提分成 / 提成 / 成长值，或“复核驳回”（须填原因）后回退 `pending_close`、留 `order.close_review_rejected`。结单表单：进入「结单表单」菜单先在“字段库”新建字段（如类型选“单选”、选项填“满意 / 不满意”），再到“表单模板”新建模板（如全局、绑定该字段并勾选必填）并“生效”；此后对 `pending_close` 订单点“结单”应弹出该表单项，不填必填项时前端拦截、直接调接口也返回中文错误（提示缺哪一项）；填全后结单成功，调 `GET /api/v1/esports/admin/orders/{id}/close-form` 的 `latest_snapshot` 应能看到提交值与模板版本；把模板“回滚”应生成更高版本号的新记录。提交规则：到「结单表单 → 提交规则」新建规则（模式选“多人提交”、未达标选“禁止结单”）并“生效”：一张有 2 名服务成员、仅 1 人上报（服务量或凭证）的待结单订单点“结单”应被拒（提示需 2 / 共 2，当前 1）；把该规则的「允许客服代提交」打开后重启新版本并生效，结单弹窗应出现“代提交未上报陪玩”区块，填第 2 席的服务量或凭证后应结单成功；改选“放行并催办提醒”后同样订单应结单成功且事务外盒多一条 `order.close_submit_remind`；改选“视为已提交”后应结单成功并留 `order.close_submit_auto_accepted`。审批流：到「结单表单 → 审批流」新建流程（触发金额 10 元、节点 1「主管复核」角色任一管理员、节点 2「管理员终审」角色超级管理员）并“生效”：对一张应付 20 元的待结单订单点“结单”应进入待复核；订单详情「结单审批」应显示「待主管复核（第 1 / 2 节点）」；用普通管理员“复核通过”后订单**仍在待复核**、详情显示「待管理员终审（第 2 / 2 节点）」；用普通管理员再通过应返回 403（该节点须超级管理员），用超级管理员通过后订单 `completed` 并计提；把节点 2 的「资金 / 争议节点」勾上再点「超时自动通过」应被拒（提示资金节点不能自动通过）。驳回：任一节点“复核驳回”后订单回待结单、审批进度转“已驳回”，再次“结单”应重建进度回到第 1 节点。超时：把节点超时设为 1 小时且勾选“超时自动通过”，把该订单的审批进度创建时间回拨 2 小时后跑 `esports_check_close_approval_timeout`，应自动通过该节点并推进；资金节点超时则只写 `close.approval_timeout` 外盒、不自动通过。
- 售后：进入“售后”菜单新建售后单（来源订单 ID + 类型 + 来源 + 诉求），同来源同问题已有进行中售后时创建失败。受理后提交方案（责任方 / 方案 / 赔付金额 / 处理说明），方案先进入**待客户确认**（§13.3）：此时直接审批应被拒（提示状态冲突）；客户自助调 `POST /api/v1/esports/user/after-sales/{id}/confirm` 后转待审批，调 `.../decline`（须填原因）则退回处理中、客服可改方案重新提交（再次进入待客户确认）；客户线下已同意时也可由客服点「代客户确认」。随后由**另一名员工**审批：`退原核销款` 仅支持原订单以客户余额核销的退款，审批通过即按 `refunded_cents + refund_reserved_cents <= payable_cents` 预占额度；执行后客户钱包新增 `refund_return`（退还本金 / 赠送）或 `adjust`（补偿权益）流水、订单 `refunded_cents` 累加且 `refund_status` 转 `partial`/`full`，重复执行失败。无余额核销流水或“补履约”方案执行时返回未接入提示（补履约见下条）。驳回 / 撤回进入终态。补履约：提交方案时选「补履约（补钟 / 换人）」，填承接陪玩 ID（须 `qualified`）、席位序号（须在 1..订单席位数内）、补足服务量与公司承担报酬；**带赔付金额、缺类型 / 缺陪玩 / 补钟量为零、陪玩未合格均应被拒**。由**另一名员工**审批通过后执行：应生成一条补履约任务，且订单 `refunded_cents` 与 `refund_reserved_cents` 均不变、无退款流水，订单事件为 `order.makeup_created`；同一售后单重复执行应失败。到页面下方「补履约任务」卡片：任务为“待服务” → 点“开始服务”转“服务中” → 点“验收”填说明转“已验收”（记录系统服务秒数与验收人）；重复开始、重复验收均应失败。限时与超时升级（§13.5）：系统配置 `app.esports.aftersale_accept_hours` / `app.esports.aftersale_plan_hours` / `app.esports.aftersale_customer_confirm_hours` / `app.esports.aftersale_execute_hours`（单位小时，`0` = 该阶段不限，缺失或非法按 2 / 24 / 48 / 24 回落）分别约束「待受理 / 处理中 / 待客户确认 / 待审批与待执行」；各阶段锚点为创建时间 / 受理时间 / 方案提交时间 / 审批时间（待审批时审批时间缺失则回落到方案提交时间）。把 `app.esports.aftersale_accept_hours` 设为 1、造一张 3 小时前创建的待受理单，跑 `esports_check_aftersale_sla` 应写一条 `aftersale.sla_breach` 事务外盒（含阶段、阈值与截止时间），**重复跑不重复升级**；把阈值放宽到 6 小时后再跑不再升级；售后列表「阶段时限」列应显示剩余分钟、超时标红（详情与列表均返回 `sla_limit_hours` / `sla_deadline` / `sla_overdue`）。陪玩追偿（§13.4）：提交方案时责任方选“陪玩”会多出「向陪玩追偿（元）」与「追偿陪玩 ID」，追偿金额超过赔付金额、责任方非陪玩或未填陪玩 ID 均应被拒；审批执行后到「结算 → 陪玩追偿」应看到一条 `待审批` 追偿单（金额 / 原因 / 来源售后）；点“审批”由**非申请人**通过后转“待扣回”，点“执行扣回”从该陪玩猫粮扣回同等金额（猫粮不足时返回中文错误、可待结算后再执行），重复执行失败；“驳回”须填原因且不得自审；陪玩端 `GET /api/v1/esports/user/player/recoveries` 可查看本人的追偿明细。
- 结算：进入“结算”菜单。先在“分成规则”新建草稿（全局 / 按游戏 / 按陪玩，按比例 / 每单固定 / 每单位固定），点“生效”切换为 `active`、原生效版本转 `history`；“回滚”以当前生效版本为模板生成更高版本并直接生效。结单后到“分成明细”按陪玩 / 订单 / 状态查询：应看到 `service` 正数明细（无命中规则或无服务成员时不生成）；对已结单订单发起并执行售后退款后，应出现同比例 `reversal` 负数明细。到“结算单”按陪玩 ID 生成结算单（填写周期开始 / 结束），应结金额为周期内待结算明细之和；点“锁定”后结算单转 `locked`、对应明细转 `settled` 并写入陪玩猫粮流水（金额为 0 时不写流水），重复锁定失败。
- 客服提成：进入“结算”菜单 → “客服提成规则”，新建草稿（适用范围选“全局默认”或“指定客服”；基数选报单金额 / 订单贡献 / 单量；计费选按比例或每单固定额；周期选日 / 周 / 月结）。**单量基数配按比例应被前端拦截、直接调接口也应返回参数无效**；阶梯阈值不严格递增、admin 范围不给 key 同样被拒。点“生效”后，用负责客服结单一张订单：到“客服提成明细”应看到 `订单提成` 明细，金额 = 基数 × 比例，且**不超过「报单金额 − 陪玩分成」**（超出时备注注明已截断）；同一周期内第二单的阶梯档位应按累计净基数上移。对该订单执行售后部分退款后应出现同比例负数 `退款冲回` 明细，全额退款后净额应为 0。到“结算单”点“生成客服提成结算单”（填客服 ID 与周期）：应结金额为周期内待结算提成之和、明细转 `settled`，**且不产生猫粮钱包流水**；点“锁定”后出现“登记发放”，填外部打款凭证号后结算单转 `paid` 并记录打款人与凭证；未锁定就发放、或重复发放均应失败。到“报表 → 客服业绩”应看到该客服的“提成”列；客服工作台“工作台”页的业绩行也应含提成。自动生成：造两条待结算提成明细（一条落在上一周期、一条落在当前周期），跑 `esports_generate_cs_settlements` 后应只生成上一周期的**草稿**结算单且该周期明细转 `settled`，当前周期明细保持待结算；重复跑不再生成。风控：进入「风险客户」菜单点“标记 / 修改”给某客户标记为“限制自助下单”并填原因，列表应显示 `限制自助下单` 与原因；该客户自助调 `POST /api/v1/esports/user/orders` 应返回「限制自助下单，请联系客服或先预付」且不产生订单；改为“疑似滥用复核”后自助下单不再被该标记拦截；点“解除”后风险名单应不再包含该客户。自动标记：给同一客户造 3 张「已完成且退款金额 > 0」的售后单，跑 `esports_flag_high_risk_customers` 应把其标记为“疑似滥用复核”并写 `customer.risk_flagged` 外盒，重复跑不重复标记、也不会把已“限制自助下单”的客户降级。
- 提现：先给陪玩结算一笔（见“结算”），使其猫粮余额非零。用该陪玩的登录用户身份调用陪玩端自助接口：`GET /api/v1/esports/user/player/dashboard` 应看到 `catfood_balance_cents` 等于可提现余额；`POST /api/v1/esports/user/player/withdrawals` 申请提现（金额、方式 `bank_card / wechat / alipay`、收款账号、8 位以上幂等键）应成功，**同一幂等键重复提交返回同一张提现单且猫粮只扣一次**；金额超过余额、金额低于 `app.esports.withdrawal_min_cents`、距上次申请不足 `app.esports.withdrawal_min_interval_hours`、收款账号为空、幂等键过短均应返回中文错误。申请后 `GET /api/v1/esports/user/player/withdrawals/summary` 的可提现余额应已扣减、待审金额增加。到管理端“结算 → 提现审核”：点“通过”后提现单转“已通过待打款”（点“登记打款”必须填外部打款凭证号，成功后转“已打款”并记录完税凭证、到账时间与打款人）；对另一张单点“驳回”必须填原因，成功后**申请金额退回陪玩猫粮且不重复退回**；重复审核、重复打款、未通过审核就打款、空打款凭证均应失败。打款前后猫粮余额不变（钱在申请时已离开猫粮）。
- 礼物：进入“礼物”菜单先在“礼物目录”新建礼物（填编号 / 名称 / 价格 / 分成方式）并点“上架”。到“送礼单据”点“代客送礼”，选上架礼物 + 收礼陪玩 ID：客户须已绑定登录身份、不得给本人送礼、须为成年、礼物须上架，高额档或超过阈值的大额礼物必须打开“大额二次确认”，付款来源仅为客户余额本金（余额不足提示中文错误）。送礼成功后客户余额减少、陪玩生成 `gift` 分成明细（并入“结算”一起结算）、客户成长值增加。同一「客户 → 陪玩」24 小时内第 6 笔会转“待复核”且暂不生成分成，复核“通过”后补建分成；对已付款单据“退款”会回退本金、冲回分成与成长值。到“礼物榜单”按日 / 周 / 月切换陪玩收礼榜与客户贡献榜，已退款不计入。
- 考核：进入“考核”菜单，先在“考核权重配置”选考核类型、填写六个维度权重（合计须 10000）并保存。到“考核任务”新建入职考核（陪玩 ID + 类型，目标等级默认新手），“分配考官”指定管理员 ID 与约定时间；用分配到的考官“打分”，逐维度填 0~100 分与评语（合计达标则任务转“通过”、陪玩被定级并开通接单，未达标转“不通过”且不改权限）；对 `spot_check` 抽查全打低分应触发降一档并暂停接单。陪玩端可对自己的已出结论考核发起申诉，管理员“复核申诉”选“申诉成立”应改判结论并重放定级。到“客户评价”用已完成订单的服务成员身份（客户自助接口）评价，同一订单同陪玩再次提交应只更新已有评价并追加修订历史；“客服代录”评价不计入评分；对评价“标记无效”后到“陪玩综合评分”查询应看到该评价被剔除、样本不足时显示提示。
- 报表：进入“报表”菜单，在“老板经营总览 / 客服个人业绩 / 派单效率 / 售后质量 / 财务资金与结算”五类 tab 间切换，用日 / 周 / 月与筛选维度（负责客服、游戏、即时/预约、单人/多人等）查询，核对订单量 / 营收 / 退款 / 净额与时间轴分段并下钻明细。用客服（非超级管理员）账号查询订单类报表应只看到本人负责 / 协作的订单、售后质量只看本人处理的售后；`财务资金与结算` tab 仅超级管理员可见，其余账号不显示该 tab 且直接调用 `finance` 返回 403。
- 页面装修：进入“页面装修”菜单，在“装修”tab 从左侧组件库添加组件（上移 / 下移 / 显隐 / 复制 / 删除），选中组件在右侧属性面板编辑属性 JSON 并“应用属性”。“保存草稿”只影响后台预览，**不影响客户端**；删掉“客服与订单入口”组件或组件数与类型非法时保存 / 发布应失败。点“发布”生成不可变快照，到“发布记录”应看到该版本转 `已发布`、旧生效版本转 `历史版本`；再次发布后版本号递增。选一个历史版本“回滚”应生成**更高版本号**的新记录并记录回滚来源，旧记录不被改写。选未来时间“定时发布”后文档转 `待发布`，到点由 `esports_publish_scheduled_pages` 任务激活。客户端读取走公开 `GET /api/v1/esports/page?channel=uni_customer_home`：未装修时返回系统默认模板（版本号 0），发布后返回已发布配置与版本号，且响应不含任何个人 / 资金数据。素材库新增仅接受 `https://` 或站内 `/` 路径，非法协议被拒。
- 客服工作台：进入“客服工作台”菜单，“工作台”tab 应看到我的客户数、本人业绩（报单量 / 成交额 / 退款 / 净额，口径同 `cs_performance`）、按状态分桶的我的订单池与公共池摘要，以及待办提醒（待付款已超时、待结单、售后待处理、跟进已到期、储值卡临近到期、今日生日客户）。到“我的客户”tab 点客户“档案”打开抽屉：核对汇总（累计充值 / 消费 / 退款 / 送礼 / 余额本金与赠送分列 / 成长值 / 订单数）、编辑手机号 / 生日（`MM-DD`，非法格式被拒）/ 消费偏好 / 标签（写入时应去空去重，超 20 个或单个超 30 字被拒）/ 跟进状态；把某客户生日改为今天（UTC+8）后工作台待办「今日生日客户」应 +1；记录一条跟进后 `最近联系` 应刷新，记录人固定为当前员工。到“企微自动建档”填企业主体 ID + 外部联系人标识：首次自动建档，**同一组合再次提交只返回既有档案**（提示已返回既有档案）且不重复建客户、不覆盖已维护的偏好与标签。到“话术库”tab 新建话术并编辑（版本不符返回冲突）、启停话术。用非超级管理员账号访问他人客户档案或跟进应返回 403。
- 储值卡：进入“储值卡”菜单点“开卡”。外部收款开卡（商户码 / 企微 / 对公）必须填支付凭证号且勾选“已确认储值卡合同”，否则被拒；填本金 100 元、赠送 20 元、有效天数 30 后开卡成功，列表应显示本金 100 / 赠送 20 / 卡内合计 120，到期提醒按有效期计算。用同一幂等键重复提交应返回冲突且不重复入账。选“客户余额划转”开卡：客户余额本金减少同等金额、卡内本金增加、两侧各留一条配对流水，且带赠送金额会被拒、余额不足会被拒。到“订单”菜单对未付款订单点“储值卡核销”填卡 ID：未填卡 ID、卡不属于该客户、卡已过期 / 待审 / 已退、商品不在适用范围、卡内余额不足均被拒并给出中文原因；成功则订单转待派单、付款方式为储值卡并记关联卡 ID，重复核销同一订单返回冲突。到“储值卡”详情点“退卡试算”应看到可退金额等于未消费本金、未消费赠送列为作废；“申请退卡”后卡转待审，用**申请人本人**审核应被拒（不得自审），换另一账号审核通过后本金退回、赠送作废、卡转已退；驳回则状态还原为可用且余额不变。走一遍售后“退原核销款”：储值卡订单的退款应回到**原卡**而不是客户余额，并冲回预付款批次。跑 `esports_expire_stored_value_cards` 任务后，已过有效期的可用卡应转“已过期”，未消费赠送被作废（赠送余额清零、多一条赠送负数作废流水）、本金保留仍可申请退卡；重复跑任务不再变化。扣减顺序：把系统配置 `app.esports.prepay_deduction_order` 设为 `principal_first` 后，用余额或储值卡核销订单应**先扣本金**（本金不足才扣赠送）；缺省或设回 `bonus_first` 则先扣赠送；设为 `proportional` 则按本金 / 赠送剩余额比例分摊（本金向下取整、尾差归赠送）。套餐：在“储值卡”菜单下方“储值卡套餐”卡片新建套餐（编码唯一、本金 + 赠送、可上下架，编辑走版本乐观锁）；开卡时选“套餐”下拉，本金 / 赠送应自动带出且输入框禁用，开出的卡详情应显示所选套餐；下架该套餐后再开卡选它应返回「该储值卡套餐已下架」。会员等级赠送比例：给某会员等级设「储值卡赠送比例」为非 0 并把客户挂到该等级，开卡**不选套餐、不填赠送额**时应自动按「本金 × 比例 / 10000」赠送（等级越高赠送越多）；手填赠送额时以手填为准；无等级客户不赠送。批次分摊：先给客户余额充值，再报单并用余额核销；到“钱包”菜单对客户余额点“流水”，抽屉下方「批次分摊明细」应列出该笔消费（`order / 单号`）动用了哪个充值批次（`wallet_tx / 流水号`）及分摊金额；储值卡核销订单后同理。
- 优先派单权（独占优先通知）：先把系统配置 `app.esports.dispatch_priority_quota` 设为 2、`app.esports.dispatch_priority_notice_minutes` 设为 3。进入“排单”菜单下方的“优先派单权”卡片，授予某陪玩某游戏分区的优先派单权（周期选周结、获得方式选人工指定）：当期应直接“生效中”；**同一游戏当期再录入第三名应提示名额已满**，**同一陪玩同一期重复录入应提示已在本期名单中**；勾选“预生成下一期名单”再录入应得到“待生效”条目。点“移出名单”必须填原因，移出后状态转“已移出”且名额立即释放。跑 `esports_rebuild_dispatch_priority` 任务后：上一期名单应转“已过期”、到点的“待生效”应转“生效中”，并为下一期预生成“待生效”名单；重复跑不应重复生成。准备一张属于该游戏分区 SKU 的订单并核销到待派单：看板“优先通知”列应显示“通知中 · 至 <截止时间>”；点“通知明细”应只看到名单内若干陪玩、状态“待回应”。此时用**名单外**陪玩登录调 `GET /api/v1/esports/user/player/grab-hall` **不应看到该单**，直接 `POST .../orders/{id}/grab` 应返回 403 与「该订单正在优先通知名单内陪玩，暂未对大厅开放」；用**名单内**陪玩调 `GET /api/v1/esports/user/player/dispatch-notices` 应看到该单，`POST .../dispatch-notices/{id}/decline` 拒绝后订单仍在待派单（名单内还有人待回应），把名单内最后一人也拒绝后订单应转“抢单中”、看板“优先通知”列显示“已释放到大厅”、释放原因为「名单内全部拒绝」，此后**任意陪玩**在抢单大厅都能看到并可抢。另一单走超时路径：不做回应，跑 `esports_expire_dispatch_priority_notice` 任务后同样应释放到大厅（原因为「优先通知超时」）。名单内陪玩在窗口内直接 `grab` 接单应成功，订单转“已接单”、其通知转“已接单”、其余待回应通知作废。对窗口内的订单点“释放到大厅”可由客服提前放开。到“排单”看板对订单点“指派”后点“按规则排序”：窗口内候选应**只包含被通知的名单内陪玩**，窗口结束后候选为全部资质合格且在线陪玩（已占席位的除外）。
- 陪玩转单与催单：先把系统配置 `app.esports.dispatch_invite_ttl_minutes` 设为 60、`app.esports.remind_ladder_minutes` 设为 `5,15,30`、`app.esports.service_overtime_minutes` 设为 30。用陪玩登录对一张已接单（未开始服务）的订单调 `POST /api/v1/esports/user/player/orders/{id}/transfer`（填原因与 8 位以上幂等键）：应返回“待确认”；**重复同一幂等键应返回同一条申请**，换幂等键应返回「该席位已有待确认的转单申请」；**确认前**到“排单 → 席位”看该席位仍为 `accepted`、订单仍为已接单。用本人调 `POST .../transfer-requests/{id}/cancel` 可撤回；撤回后再确认应冲突。重新发起后用管理员到“排单 → 陪玩转单申请”点“通过”：席位应回到“待补位”（陪玩清空）、订单退回待接单；单席位订单应退回待派单，且若该游戏分区有优先名单会重新开优先通知窗口。点“驳回”必须填原因，驳回后席位与订单都不变；重复确认应失败。对已有服务段的订单发起转单应返回「已开始服务后不可转单，请走售后补履约」。催单阶梯：把邀请席位的到期时间回拨到「已过 20 分钟」（有效期按 60 分钟反推发出时间），跑 `esports_remind_dispatch_ladder` 任务应补发第 1、2 档提醒（`dispatch.remind_player` 与 `dispatch.remind_admin` 两条事务外盒 + 两条 `order.reminded` 事件）；**重复跑不应重复发**，时间推进到第 3 档后再跑只补发 1 条。服务超时：把某订单置为 `in_service` 并造一条 40 分钟前开始的服务段，跑 `esports_check_service_overtime` 应写一条 `dispatch.service_overtime` 与 `order.service_overtime`；把阈值改为 120 后重复跑不应再提醒。

对应接口为 `GET/POST /api/v1/esports/admin/skus`、`PUT /api/v1/esports/admin/skus/{id}`、`GET/POST /api/v1/esports/admin/categories`、`PUT/DELETE /api/v1/esports/admin/categories/{id}`、`PUT /api/v1/esports/admin/orders/{id}/quote`、`PUT /api/v1/esports/admin/orders/{id}/appointment`、`POST /api/v1/esports/admin/orders/{id}/cancel`、`GET /api/v1/esports/admin/orders/{id}/cancel-preview`、`POST /api/v1/esports/admin/orders/{id}/pay-by-balance`、`POST /api/v1/esports/admin/orders/{id}/deposit`、`POST /api/v1/esports/admin/orders/{id}/restore`、`GET /api/v1/esports/admin/wallets`、`GET /api/v1/esports/admin/wallets/{id}/transactions`、`POST /api/v1/esports/admin/wallets/{id}/recharge`、`POST /api/v1/esports/admin/wallets/{id}/adjust`、`POST /api/v1/esports/admin/customers/{id}/age-checks`、`GET /api/v1/esports/admin/age-checks`、`POST /api/v1/esports/admin/age-checks/{id}/review`、`GET /api/v1/esports/admin/dispatch/board`、`GET /api/v1/esports/admin/orders/{id}/seats`、`POST /api/v1/esports/admin/orders/{id}/seats/{seat_index}/assign`、`POST /api/v1/esports/admin/orders/{id}/seats/{seat_index}/revoke`、`POST /api/v1/esports/admin/orders/{id}/claim`、`POST /api/v1/esports/admin/orders/{id}/transfer`、`POST /api/v1/esports/admin/orders/{id}/remind`、`GET/POST /api/v1/esports/admin/orders/{id}/addons`、`POST /api/v1/esports/admin/order-addons/{id}/cancel`、`POST /api/v1/esports/admin/order-addons/{id}/pay-by-balance`。结单与售后接口为 `POST /api/v1/esports/admin/orders/{id}/close`、`POST /api/v1/esports/admin/orders/{id}/close-approve`、`POST /api/v1/esports/admin/orders/{id}/close-reject`、`GET/POST /api/v1/esports/admin/after-sales`、`GET /api/v1/esports/admin/after-sales/{id}`、`POST /api/v1/esports/admin/after-sales/{id}/accept`、`POST /api/v1/esports/admin/after-sales/{id}/plan`、`POST /api/v1/esports/admin/after-sales/{id}/approve`、`POST /api/v1/esports/admin/after-sales/{id}/reject`、`POST /api/v1/esports/admin/after-sales/{id}/execute`、`POST /api/v1/esports/admin/after-sales/{id}/withdraw`。待客户确认（§13.3）接口管理端为 `POST /api/v1/esports/admin/after-sales/{id}/confirm`（客服代确认）；客户自助为 `POST /api/v1/esports/user/after-sales/{id}/confirm`、`POST /api/v1/esports/user/after-sales/{id}/decline`。陪玩追偿（§13.4）接口管理端为 `GET /api/v1/esports/admin/player-recoveries`、`POST /api/v1/esports/admin/player-recoveries/{id}/approve`、`POST /api/v1/esports/admin/player-recoveries/{id}/reject`、`POST /api/v1/esports/admin/player-recoveries/{id}/execute`；陪玩端为 `GET /api/v1/esports/user/player/recoveries`。客户风控（§13.7）接口管理端为 `GET /api/v1/esports/admin/risk-customers`、`POST /api/v1/esports/admin/customers/{id}/risk`。客户自助端（§13.2 / §10，供 uni-app 客户端调用，均为 `AuthUser`）接口为 `POST /user/orders`（自助下单）、`GET /user/orders`（我的订单，`bucket` 分组）、`GET /user/orders/{id}`（我的订单详情）、`GET /user/aftersales`（我的售后）、`POST /user/aftersales`（自助发起售后）、`GET /user/wallets`（余额与储值卡）、`GET /user/profile`（会员等级 / 成长值 / 风险只读）。结单表单设计器（§3.4）接口为 `GET/POST /api/v1/esports/admin/close-form-fields`、`PUT /api/v1/esports/admin/close-form-fields/{id}`、`POST /api/v1/esports/admin/close-form-fields/{id}/status`、`GET/POST /api/v1/esports/admin/close-form-templates`、`PUT /api/v1/esports/admin/close-form-templates/{id}`、`POST /api/v1/esports/admin/close-form-templates/{id}/activate`、`POST /api/v1/esports/admin/close-form-templates/{id}/rollback`、`GET /api/v1/esports/admin/orders/{id}/close-form`；结单提交规则（§3.4）接口为 `GET/POST /api/v1/esports/admin/close-submit-rules`、`PUT /api/v1/esports/admin/close-submit-rules/{id}`、`POST /api/v1/esports/admin/close-submit-rules/{id}/activate`、`POST /api/v1/esports/admin/close-submit-rules/{id}/rollback`；结单审批流（§3.4）接口为 `GET/POST /api/v1/esports/admin/close-approval-flows`、`PUT /api/v1/esports/admin/close-approval-flows/{id}`、`POST /api/v1/esports/admin/close-approval-flows/{id}/activate`、`POST /api/v1/esports/admin/close-approval-flows/{id}/rollback`、`GET /api/v1/esports/admin/orders/{id}/close-approval`。补履约任务（§13.6）接口为 `GET /api/v1/esports/admin/fulfillment-makeups`、`POST /api/v1/esports/admin/fulfillment-makeups/{id}/start`、`POST /api/v1/esports/admin/fulfillment-makeups/{id}/complete`。结单规则（§3.4）接口为 `GET/POST /api/v1/esports/admin/close-rules`、`PUT /api/v1/esports/admin/close-rules/{id}`、`POST /api/v1/esports/admin/close-rules/{id}/activate`、`POST /api/v1/esports/admin/close-rules/{id}/rollback`。结算接口为 `GET/POST /api/v1/esports/admin/commission-rules`、`POST /api/v1/esports/admin/commission-rules/{id}/activate`、`POST /api/v1/esports/admin/commission-rules/{id}/rollback`、`GET /api/v1/esports/admin/commission-entries`、`GET/POST /api/v1/esports/admin/settlements`、`POST /api/v1/esports/admin/settlements/{id}/lock`、`GET /api/v1/esports/admin/settlements/{id}`；陪玩端为 `GET /api/v1/esports/user/player/commission`、`GET /api/v1/esports/user/player/commission/summary`。陪玩模块管理端接口为 `GET/POST /api/v1/esports/admin/players`、`PUT /api/v1/esports/admin/players/{id}`；陪玩端自助接口为 `GET /api/v1/esports/user/player/dashboard`、`POST /api/v1/esports/user/player/presence`、`GET /api/v1/esports/user/player/invites`、`POST /api/v1/esports/user/player/invites/{id}/accept`、`POST /api/v1/esports/user/player/invites/{id}/reject`、`GET /api/v1/esports/user/player/grab-hall`、`POST /api/v1/esports/user/player/orders/{id}/grab`、`GET /api/v1/esports/user/player/orders`、`POST /api/v1/esports/user/player/orders/{id}/service/start`、`POST /api/v1/esports/user/player/orders/{id}/service/finish`（供 uni-app 客户端使用）。陪玩端保活（§6.4）为 `POST /api/v1/esports/user/player/heartbeat`。陪玩端服务售后申诉（§13.2）为 `POST /api/v1/esports/user/player/order-after-sales`、`GET /api/v1/esports/user/player/order-after-sales`。陪玩端加钟（§6.2）接口为 `GET /api/v1/esports/user/player/addons`、`POST /api/v1/esports/user/player/addons/{id}/confirm`、`POST /api/v1/esports/user/player/addons/{id}/reject`。。充值活动接口为 `GET/POST /api/v1/esports/admin/campaigns`、`PUT /api/v1/esports/admin/campaigns/{id}`、`POST /api/v1/esports/admin/campaigns/{id}/status`、`POST /api/v1/esports/admin/wallets/{id}/recharge-campaigns`。会员等级接口为 `GET/POST /api/v1/esports/admin/member-levels`、`PUT /api/v1/esports/admin/member-levels/{id}`、`POST /api/v1/esports/admin/member-levels/{id}/status`、`GET /api/v1/esports/admin/customers/{id}/member`、`POST /api/v1/esports/admin/customers/{id}/member/level`。礼物接口为 `GET/POST /api/v1/esports/admin/gifts`、`PUT /api/v1/esports/admin/gifts/{id}`、`POST /api/v1/esports/admin/gifts/{id}/status`、`GET/POST /api/v1/esports/admin/gift-orders`、`POST /api/v1/esports/admin/gift-orders/{id}/refund`、`POST /api/v1/esports/admin/gift-orders/{id}/review`、`GET /api/v1/esports/admin/gift-leaderboard`。考官端接口为 `GET/POST /api/v1/esports/admin/assessment-rules`、`GET/POST /api/v1/esports/admin/assessments`、`GET /api/v1/esports/admin/assessments/{id}`、`POST /api/v1/esports/admin/assessments/{id}/assign`、`POST /api/v1/esports/admin/assessments/{id}/score`、`POST /api/v1/esports/admin/assessments/{id}/appeal/review`、`GET/POST /api/v1/esports/admin/player-reviews`、`POST /api/v1/esports/admin/player-reviews/{id}/status`、`GET /api/v1/esports/admin/players/{id}/rating`；陪玩端为 `GET /api/v1/esports/user/player/assessments`、`POST /api/v1/esports/user/player/assessments/{id}/appeal`；客户自助为 `POST /api/v1/esports/user/orders/{id}/reviews`。报表接口为 `GET /api/v1/esports/admin/reports/{type}`（`:type` 为 `owner_overview` / `cs_performance` / `dispatch_efficiency` / `aftersale_quality` / `finance`）。页面装修接口为 `GET /api/v1/esports/admin/pages`、`GET /api/v1/esports/admin/pages/{channel}`、`PUT /api/v1/esports/admin/pages/{channel}/draft`、`POST /api/v1/esports/admin/pages/{channel}/publish`、`POST /api/v1/esports/admin/pages/{channel}/rollback`、`GET /api/v1/esports/admin/pages/{channel}/versions`、`GET/POST /api/v1/esports/admin/page-assets`、`DELETE /api/v1/esports/admin/page-assets/{id}`，客户端读取为公开 `GET /api/v1/esports/page`（仅返回已发布配置，无个人数据）。客服端（§8）接口为 `GET /api/v1/esports/admin/cs/workbench`、`GET/PUT /api/v1/esports/admin/customers/{id}/profile`、`GET/POST /api/v1/esports/admin/customers/{id}/follow-ups`、`POST /api/v1/esports/admin/customers/wecom-intake`、`GET/POST /api/v1/esports/admin/scripts`、`PUT /api/v1/esports/admin/scripts/{id}`、`POST /api/v1/esports/admin/scripts/{id}/status`，PC 工作台 / 移动客服端 / 企微侧边栏三个入口共用这一套接口。储值卡（§4.4）接口为 `GET/POST /api/v1/esports/admin/stored-value-cards`、`GET /api/v1/esports/admin/stored-value-cards/{id}`、`GET /api/v1/esports/admin/stored-value-cards/{id}/refund-quote`、`POST /api/v1/esports/admin/stored-value-cards/{id}/refund-request`、`POST /api/v1/esports/admin/stored-value-cards/{id}/refund-decision`、`POST /api/v1/esports/admin/orders/{id}/pay-by-stored-value-card`。客服提成（§12.2）接口为 `GET/POST /api/v1/esports/admin/cs-commission-rules`、`POST /api/v1/esports/admin/cs-commission-rules/{id}/activate`、`POST /api/v1/esports/admin/cs-commission-rules/{id}/rollback`、`GET /api/v1/esports/admin/cs-commission-entries`、`GET /api/v1/esports/admin/cs-commission/summary`、`POST /api/v1/esports/admin/cs-settlements`、`POST /api/v1/esports/admin/settlements/{id}/pay`。提现（§6.3 / §12.6）接口管理端为 `GET /api/v1/esports/admin/withdrawals`、`GET /api/v1/esports/admin/withdrawals/{id}`、`POST /api/v1/esports/admin/withdrawals/{id}/review`、`POST /api/v1/esports/admin/withdrawals/{id}/pay`；陪玩端为 `POST /api/v1/esports/user/player/withdrawals`、`GET /api/v1/esports/user/player/withdrawals`、`GET /api/v1/esports/user/player/withdrawals/summary`。优先派单权与优先通知（§5.4 / §5.5）接口管理端为 `GET/POST /api/v1/esports/admin/dispatch/priority`、`POST /api/v1/esports/admin/dispatch/priority/{id}/revoke`、`GET /api/v1/esports/admin/orders/{id}/dispatch-candidates`、`GET /api/v1/esports/admin/orders/{id}/priority-notices`、`POST /api/v1/esports/admin/orders/{id}/release-priority`、`POST /api/v1/esports/admin/orders/{id}/publish-grab`；陪玩端为 `GET /api/v1/esports/user/player/dispatch-notices`、`POST /api/v1/esports/user/player/dispatch-notices/{id}/decline`（接受复用既有 `POST /api/v1/esports/user/player/orders/{id}/grab`）。陪玩转单（§5.7）接口管理端为 `GET /api/v1/esports/admin/seat-transfers`、`POST /api/v1/esports/admin/seat-transfers/{id}/decision`；陪玩端为 `POST /api/v1/esports/user/player/orders/{id}/transfer`、`GET /api/v1/esports/user/player/transfer-requests`、`POST /api/v1/esports/user/player/transfer-requests/{id}/cancel`。这组开发入口不提供真实支付能力。

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

客服先通过 `POST /admin/gift-intents` 创建 30 分钟有效的明细快照；客户通过 `GET /user/gift-intents` 查看礼物、收礼人、数量、单价、总价与付款来源，并以已验证账号调用 `POST /user/gift-intents/{id}/confirm` 明确确认。客服随后只用 `intent_id` 调用 `POST /admin/gift-orders`。服务端在扣余额本金的同一事务中校验并消费确认单，礼物版本、价格或收礼人变化会拒绝执行；原有 `confirmed` 布尔值不再被管理接口接受。管理端列表与退款按客户当前归属隔离，疑似礼物复核不得由业绩归属客服自审。现有退款仍只支持全额直接执行，独立争议申请、审批和部分退款仍待补齐。

## 关联资源权限与售后客户确认（2026-10）

客服访问礼物、售后、储值卡、客户余额钱包与加钟独立入口时按当前归属客户或订单过滤。客服建售后单会自动绑定来源订单客户，后台“代客户确认方案”入口已删除；客户须在自助端以已验证账号确认后进入审批。跨客服审批和资金操作仍需按角色权限配置，现阶段不可视为完整生产权限验收。

## 人工调价底价与超限审批（2026-10）

商品 SKU 增加每份最低议价及人工调整免审幅度（基点），报单时快照到订单。改价预览与执行均使用订单价格和客户当前会员权益重新计算报价；人工议价后的标价小计不得低于订单快照底价 × 份数，会员折扣独立计算。超出免审幅度时，负责客服提交包含订单版本、份数、调整金额、应付和原因的审批单，由非申请人的超级管理员复核；审批通过后申请人才能按完全相同的报价应用，应用与审批单消费同事务。改价作废旧支付意图并抬升报价版本，客户须重新确认。已下架 SKU 不再生成未付款新报价。接口为 `POST /admin/orders/{id}/quote-preview`、`POST /admin/orders/{id}/price-adjustments`、`GET /admin/price-adjustments`、`POST /admin/price-adjustments/{id}/decision`、`PUT /admin/orders/{id}/quote`。尚需人工端到端验收和权限角色配置。

## 储值卡本金送礼（2026-10）

开卡合同增加 `allow_gifts`，默认关闭；旧卡不会自动取得送礼资格。客服发起礼物确认单时可选择客户余额本金或已授权储值卡本金，确认单保存付款来源、卡号和金额，客户移动端逐项确认。执行时在同一事务内再次检查卡归属、状态、有效期与本金余额，只扣卡内本金并写储值卡预付款批次分摊；赠送权益不能买礼物。现有全额退款按原付款来源返还本金，储值卡消费返原卡。独立礼物退款申请、审批和部分退款仍待补齐。

## 分成与提成规则锁定（2026-10）

陪玩接受邀请或抢单时，逐成员锁定当时适用的分成规则；无规则也锁定空值，后续新规则不追溯旧单。开工时把锁定结果写入本人服务段，改派也不会覆盖旧成员约定。结单时把扣除结单前退款的服务应付按成员有效服务秒数分配，再分别应用其规则，分成合计不超过净服务应付减已知渠道成本。加钟确认也按确认人本人规则计算。客服提成在余额、储值卡或已验签外部付款核销时锁定业绩客服与规则，结单时使用该快照。历史订单若没有锁定字段，则仍按结单时规则兼容计算，正式启用前需处理历史数据。单笔手动指定、陪玩等级优先级及按具体争议成员冲回尚未完成。

## 结单模板与退款冲回快照（2026-10）

新订单在下单时锁定适用的结单表单模板，包含当时无模板的情况；模板生效时固定字段类型、选项、必填设置和版本，后续发布模板或编辑字段库不会改变旧单的结单要求。结单时另保存未退款的净服务应付额；结单后服务分成和客服提成按该额冲回，退款执行会读取事务内更新后的累计退款额。历史无模板/字段/净服务额快照的订单仍使用兼容逻辑，正式启用前需迁移或审计。

客户自助发起已完成或已取消服务订单的售后时，申请窗口读取 `app.esports.customer_aftersale_window_days`，缺省 30 天，配置 0 为不限时；在途订单仍可即时申诉。该窗口不限制客服代客户建单。礼物、储值卡及充值来源接入统一售后后仍需补同类校验。

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

`esports_auto_offline_players` 任务按 `app.esports.player_heartbeat_timeout_minutes`（默认 10 分钟，缺失或非法回落默认值）处理：只挑「在线且已有心跳基线」的陪玩，无心跳记录的存量档案不动（避免升级后一次性把在线陪玩全部离线）。若该陪玩在待接单 / 已接单 / 服务中订单上持有已接单席位，说明存在在途履约，此时**不置离线**而是转 `protected_abnormal` 并写 `player.heartbeat_timeout` 外盒提醒客服核实；其余置 `offline`。外盒幂等键含心跳时戳，同一次失联只提醒一次，重新上线后再次失联才会再提醒。

## 终态售后新证据复核（2026-10）

已驳回 / 已完成的售后单不再只能另建一张不同问题单：`POST /admin/after-sales/{id}/reviews` 提交复核申请（`reason` 必填、`evidence_ref` 存新证据引用），`GET /admin/after-sales/{id}/reviews` 查看复核历史，`POST /admin/after-sale-reviews/{id}/decision` 由异人审查（`decision` 取 `uphold` 维持原结论或 `reopen` 发回重新处理），`POST /admin/after-sale-reviews/{id}/withdraw` 由申请人撤回待审查申请。状态机为 `pending → upheld | reopened | withdrawn`。

约束：只有终态售后可申请，同一售后单同一时刻只允许一张进行中复核（撤回后可再申请）；审查人不得是申请人，也不得是原审批人——发回重处理会推翻原审批结论，由原审批人自己审查等于自审。`reopen` 会把售后单由终态置回 `processing`、清空结案时间、重新冻结该订单待结算分成，之后可重新提交方案、重走客户确认与审批；若该订单的分成已结算或已纳入结算单，改判无法把退款冲回已结算分成，此时直接拒绝并提示转人工财务处理（复核申请与售后单状态均保持不变）。全流程写 `order.after_sale_review_requested` / `order.after_sale_review_decided` 订单事件与 `aftersale.review_requested` 外盒。

人工检查：把一张售后单驳回到 `rejected`，用处理客服提交复核申请，再用同一账号审查应返回 403；换第三个管理员选 `reopen` 后，该售后单回到「处理中」且可重新提交方案；对分成已结算的订单重复上述「发回重处理」应被拒并提示已结算。

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

隔离规则（与常规报单的关键差别）：入口固定 `entry_point=admin_backfill`（调用方不能指定）；只允许 `completed` / `cancelled` 两个终态，**不占 SKU 名额、不建支付意图与支付回执、不建派单席位**，避免历史单消耗当期容量；订单 `created_at` 与 `paid_at` 写实际成交时间，报表按业务发生期归集；`completed` 单同时写 `closed_at` 与 `close_net_service_cents` 快照，保证后续退款冲回口径正确。有 SKU 时沿用 SKU 快照价格 / 服务量；无 SKU 时不绑定 SKU（`sku_id=0`），用 `unit_price_cents` 兜底定价、服务量按数量 1:1、席位默认 1。**陪玩分成与成长值默认正常计提**（与普通订单一致）；**客服提成默认不计入**，写一条 `order.cs_commission_excluded` 外盒留痕，只有显式 `count_cs_commission=true` 才在同一事务内按结单口径计提。补录必须记录补录原因、补录人与补录时刻，写 `order.backfilled` 订单事件与外盒；客服只能为本人负责的客户补录。

结单路径也加了同一守卫（`orderCountsCsCommission`）：即使有人把补录单推进到结单流程，补录单仍默认不计提客服提成，只写客服提成排除外盒，不会把历史流水算进当期业绩。

人工检查：用客服身份对本人客户补录一张 30 天前的已完成单，应看到 `entry_point=admin_backfill`、`created_at` 为 30 天前、`closed_at` 已写入、SKU 的已售与预占名额均未变化、`order.cs_commission_excluded` 外盒存在、成长值流水已计提且客服提成明细为空；同一 `Idempotency-Key` 重复提交返回同一张单，改数量后同键提交应报冲突；把 `count_cs_commission` 置 `true` 再补录不应出现排除外盒且按规则产出客服提成明细；省略 `sku_id` 并给 `unit_price_cents` 可补录一张 `sku_id=0` 的历史单。管理端补录表单已接入，字段名为 `count_cs_commission`（旧名 `count_commission` 已废弃且不再生效），收款状态固定为已收款、不再提供「未收款」选项。

## 已核销收款的渠道撤销 / 拒付（2026-10）

核销后的钱可能被渠道或客户事后撤回（商户撤销、客户拒付），此前系统只记录「到账」、核销后没有任何独立风险事件，账面上订单仍是已收款。现在：`POST /admin/payment-reversals` 由财务登记撤销 / 拒付（`receipt_id` 指向一笔已核销支付回执，`kind` 取 `revoked` 渠道撤销 / `chargeback` 客户拒付，`amount_cents` 不得超过该回执实际到账额，`reason` 4–255 字，`evidence_ref` 存渠道通知或银行回单引用），`GET /admin/payment-reversals` 按订单与状态查询，`POST /admin/payment-reversals/{id}/decision` 处置。

一笔回执只允许登记一次（`receipt_id` 唯一索引兜底）。**登记只记账与告警，不改订单资金状态**——订单怎么处置由人工在 decision 里明确，避免系统替人做资金判断。登记写 `order.payment_reversal_registered` 订单事件与 `payment.reversal_registered` 外盒。

处置动作（`pending → handled`，`ignore` 记为 `rejected`），处置人不得是登记人：`mark_exception` 把订单 `paid/partial` 改判为 `exception`（资金异常转人工，必须填处置说明）；`suspend_customer` 在其上再把客户置 `restricted` 并写风控原因（阻断客户自助下单，必须同时填处置说明与风控原因）；`ignore` 认定为误报，只留结论不动订单。处置写 `order.payment_reversal_handled` 订单事件。

人工检查：走完「提交到账凭证 → 财务核销」拿到回执后，用财务身份登记一笔超额撤销应报参数错误，正常登记后订单 `payment_status` 仍为 `paid` 且出现 `payment.reversal_registered` 外盒；重复登记同一回执应报冲突；换另一个管理员选 `suspend_customer` 并填风控原因后，订单应转 `exception`、客户 `risk_status` 变 `restricted`，客户自助下单被拒；用 `ignore` 处置另一笔时应只留结论、订单与客户都不变。真实渠道回调自动登记仍需商户密钥与回调地址，当前为财务人工登记闭环。

## 2026-10-17 修复批次：资金口径、通知投递、在线收款与客户端补齐

本章节记录本轮 9 项修复的最终口径。**前文凡与本批次冲突的表述（如「外部渠道退款未实现」「真实推送通道未接入」「管理端补录表单尚未接入」「在线支付验签回调尚未开通」）均以本章节为准。**

### 1. 外部退款：纳入预占、禁止自执行、同步冲回（P0）

`executeExternalRefund` 补齐三条规则：

- **不得自执行**：`registered_by_admin_id == 执行人` 直接 403（与售后「不得自审」同口径），避免同一人既登记凭证又核验放款。
- **额度分两种**：关联售后单（`aftersale_id` 非空）时，金额已在售后审批阶段预占进 `refund_reserved_cents`，执行时要求 `refund_reserved_cents >= amount`，扣减预占并累加 `refunded_cents`，不再重复占用额度；未关联售后单时按 `refunded_cents + refund_reserved_cents + amount <= payable_cents` 校验，只累加已退额，**不得侵占已被售后占用的额度**。两种情况都用条件表达式防并发，累计已退额不会超过应付额。
- **同事务冲回三件套**：退款落账后在同事务内冲回陪玩分成（`reverseOrderCommissionFor`）、客服提成（`reverseOrderCsCommissionFor`）与客户成长值（`reverseOrderGrowthFor`）。三者由 `refundReversalContext` 统一承载退款金额、幂等键与争议陪玩，售后链路与外部退款链路共用同一套冲回算法，只是幂等键不同（售后用 `AfterSaleNo`，外部退款用 `RefundNo`）。成长值流水沿用 `source_type=after_sale` + `source_id=退款单号`，语义为「退款冲回」；未改 schema。

登记阶段同步加基本校验：未关联售后单时按「已退 + 已预占 + 该订单已登记未执行的外部退款 + 本次 ≤ 应付」拦截，避免多笔待执行登记合计透支订单额度；关联售后单的登记金额已含在 `refund_reserved_cents` 中，不重复计入。

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

- 前后端统一为 `count_cs_commission`（旧名 `count_commission` 不再兼容且不生效，前后端须同批发布）。管理端表单已改为提交该字段。
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

本轮未新增数据库迁移；外部退款冲回、通知收件人、在线收款、钱包展示均复用既有表结构与索引。
