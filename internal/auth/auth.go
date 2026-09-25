// Package auth 提供用户认证与配置管理：
// 用户注册/登录、令牌生成与校验、临时账户、以及用户级 key-value 配置读写，
// 数据持久化在数据目录下的 auth.json 文件中。
// （Package auth provides user authentication and config management: register/login, token generation
// and validation, temporary accounts, and per-user key-value config storage, persisted to auth.json.）
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	dataio "quant-trading-v2/internal/data"
)

// User 用户账号记录。
// （User is a user account record.）
type User struct {
	// 用户唯一标识（u_ 前缀）
	ID string `json:"id"`
	// 登录用户名
	Username string `json:"username"`
	// bcrypt 密码哈希（临时账户为空）
	PasswordHash string `json:"password_hash"`
	// 认证令牌（§多会话迁移：仅作旧数据兼容槽位，新签发一律写入 Sessions）
	Token string `json:"token,omitempty"`
	// 令牌过期 Unix 时间戳（0 表示永不过期）
	TokenExp int64 `json:"token_exp,omitempty"`
	// 多会话列表：每个登录设备一个会话（手机 App 与 Web 可同时在线互不顶踢）。
	// §历史缺陷：此前单 token 槽位"登录即轮换"，手机/电脑任一端重新登录即把
	// 另一端顶成 401——前端拉配置失败回退本地缓存，表现为"实盘开关自动关闭"。
	Sessions []Session `json:"sessions,omitempty"`
	// 角色：admin=管理员 / user=普通用户（空按 user 处理）
	Role string `json:"role,omitempty"`
	// 细粒度权限位列表（如 research_approve），管理员隐式拥有全部
	Perms []string `json:"perms,omitempty"`
	// 账号是否启用（默认 true；禁用后登录/令牌失效）
	// §D3 修复：去掉 omitempty——Enabled=false 是敏感状态，序列化时必须显式出现，
	// 否则 /api/admin/users 返回缺字段 → 前端 `enabled ?? true` 兜底把禁用账号渲染成启用。
	// English: Enabled must always serialize; omitempty was hiding disabled state.
	Enabled bool `json:"enabled"`
	// 创建时间 Unix 时间戳
	CreatedAt int64 `json:"created_at"`
	// 账号有效期截止 Unix 时间戳（0=永久）
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// §MT 租户归属（空=t_default 系统租户，兼容全部存量账号）。
	// 租户边界=账号管理/配额/用户级配置的隔离域；运营数据归属仍按 AdminID（平台运营者）。
	// English: §MT tenant membership (empty ⇒ t_default, fully backward compatible).
	TenantID string `json:"tenant_id,omitempty"`
}

// ── §MT 多租户（Multi-Tenancy, 2026-09-17）──
// 模型：Tenant = 账号管理 + 配额的最小隔离单元。角色体系不变（admin/user），
// 规则为「admin 默认只能管理本租户成员；唯一例外是 t_default（系统租户）里的 admin，
// 即平台运营者，可跨租户管理并签发租户」。存量单租户部署升级后全员落入 t_default，
// 原有 admin 天然就是平台运营者，行为与升级前完全一致。
// English: a Tenant is the smallest isolation unit for account management & quota. Roles stay
// admin/user; an admin manages only its own tenant, except admins of t_default (the platform
// operator) who span tenants. Legacy installs migrate everyone into t_default unchanged.

// DefaultTenantID 系统租户 ID：存量/未分租户账号的归属，其中的 admin 为平台运营者。
const DefaultTenantID = "t_default"

// defaultMaxUsers 租户成员数默认配额上限（MaxUsers<=0 时生效）。
const defaultMaxUsers = 20

// defaultAPIRatePerMin 租户业务 API 每分钟默认限流（APIRatePerMin<=0 时生效）。
const defaultAPIRatePerMin = 600

// TenantQuota 租户配额。零值字段按系统默认值执行。
type TenantQuota struct {
	// MaxUsers 成员账号数上限（不含 temp_ 临时号；0=默认 20）
	MaxUsers int `json:"max_users"`
	// APIRatePerMin 该租户成员业务 API 调用合计每分钟上限（0=默认 600）
	APIRatePerMin int `json:"api_rate_per_min"`
}

// MaxUsersOr 返回生效的成员上限。
func (q TenantQuota) MaxUsersOr() int {
	if q.MaxUsers <= 0 {
		return defaultMaxUsers
	}
	return q.MaxUsers
}

// APIRateOr 返回生效的每分钟限流。
func (q TenantQuota) APIRateOr() int {
	if q.APIRatePerMin <= 0 {
		return defaultAPIRatePerMin
	}
	return q.APIRatePerMin
}

// Tenant 租户记录（落盘 auth.json）。
type Tenant struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Enabled   bool        `json:"enabled"`
	CreatedAt int64       `json:"created_at"`
	Quota     TenantQuota `json:"quota"`
}

// ErrTenantQuota 租户配额耗尽哨兵错误（成员数超限）。HTTP 层映射 409。
var ErrTenantQuota = errors.New("租户成员数已达配额上限")

// ErrTenantDisabled 租户被停用（禁止新增成员）。
var ErrTenantDisabled = errors.New("租户已停用")

// Session 单个登录会话：一个设备/客户端对应一条，落盘仅存令牌哈希。
// （Session is a single login session, one per device/client; only the token hash is persisted.）
type Session struct {
	// 令牌 SHA-256 哈希（不落明文）
	Token string `json:"token"`
	// 会话过期 Unix 时间戳
	Exp int64 `json:"exp"`
	// 会话创建时间 Unix 时间戳（审计用）
	CreatedAt int64 `json:"created_at,omitempty"`
}

// maxSessions 每账号最大并发会话数：超出时淘汰最旧会话（FIFO），防止 auth.json 无限膨胀。
const maxSessions = 8

// 角色常量。
// （Role constants.）
const (
	RoleAdmin = "admin" // 管理员：拥有全部权限，可管理账号/配置
	RoleUser  = "user"  // 普通用户：权限由 Perms 决定
)

// 权限位常量：细粒度功能权限，管理员角色隐式拥有全部权限位。
// （Permission bit constants: fine-grained feature permissions; the admin role implicitly holds all of them.）
const (
	// PermResearchApprove 研究候选审批/驳回权限（自动研究 B5 的审批应用操作）。
	PermResearchApprove = "research_approve"
)

// PermResearchApprove 之外可按需扩展更多权限位。

// AllPerms 返回当前定义的全体权限位（供前端渲染权限清单）。
// （AllPerms returns all defined permission bits for frontend rendering.）
func AllPerms() []string {
	return []string{PermResearchApprove}
}

// HasPerm 判断用户是否拥有指定权限位。
// 管理员角色隐式拥有全部权限；临时/空角色用户仅当权限位命中才返回 true。
// （HasPerm reports whether the user holds a permission bit. Admin role implicitly holds all;）
func (u *User) HasPerm(perm string) bool {
	if u == nil {
		return false
	}
	if u.Role == RoleAdmin {
		return true
	}
	for _, p := range u.Perms {
		if p == perm {
			return true
		}
	}
	return false
}

// IsAdmin 判断用户是否为管理员角色。
func (u *User) IsAdmin() bool {
	return u != nil && u.Role == RoleAdmin
}

// AdminID 返回第一个正式管理员账号的 ID（剔除临时账号 tmp_），作为运营数据归属账号。
// 后端据此把量化/模拟盘/看板/告警/LLM 等运营数据统一归属管理员，并按角色鉴权。
// English: returns the first real admin account's ID (excluding tmp_ accounts); used as the
// operator that owns all operational data, with role-based access enforced at the API layer.
func (m *Manager) AdminID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.db.Users {
		u := &m.db.Users[i]
		if u.Role == RoleAdmin && !strings.HasPrefix(u.ID, "tmp_") {
			return u.ID
		}
	}
	return ""
}

// ConfigEntry 用户配置键值项。
// （ConfigEntry is a user-config key-value entry.）
type ConfigEntry struct {
	// 配置键名
	Key string `json:"key"`
	// 配置值
	Value string `json:"value"`
	// 所属用户 ID（系统级配置用 "system"）
	UserID string `json:"user_id"`
}

// DB 认证数据库结构（用户与配置列表）。
// （DB is the authentication database structure: user and config lists.）
type DB struct {
	// 全部用户列表
	Users []User `json:"users"`
	// 用户/系统级配置项列表
	Configs []ConfigEntry `json:"configs"`
	// §GAP2-W2 邀请码注册（owner 决策 D7）：公网开放注册/临时号是隔离违例的放大器，
	// 改为管理员签发、一次性使用的邀请码制。English: §GAP2-W2 invite-code registration (decision D7).
	// 邀请码列表
	Invites map[string]*Invite `json:"invites,omitempty"`
	// SchemaVersion §A1 库结构版本：兼容迁移只跑一次。此前迁移每次启动无条件把
	// Enabled=false 改回 true——管理员禁用的账号重启即复活，封禁形同虚设。
	// 数据 schema 版本
	SchemaVersion int `json:"schema_version,omitempty"`
	// §MT 租户表（key=租户 ID）。t_default 恒存在（ensureTenantsLocked 兜底创建）。
	Tenants map[string]*Tenant `json:"tenants,omitempty"`
}

// Manager 用户与登录认证管理器。
// （Manager is the user & login authentication manager.）
type Manager struct {
	mu      sync.RWMutex // 并发读写锁，保护 db
	dataDir string       // 数据存储目录
	dbPath  string       // auth.json 数据库文件路径
	db      *DB          // 内存中的认证数据库
	// rawTokens §GAP6.2 token 哈希存储的运行时补充：最近签发的原始令牌仅存进程内存
	//（落盘的是 SHA-256 哈希），重启后清空——客户端需重新登录获取新令牌。
	rawTokens map[string]string // userID → 最近签发原始令牌（不落盘）
}

// hashToken 令牌哈希：SHA-256 hex。落盘只存哈希，泄露 auth.json 不再等于接管全部账号。
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// NewManager 创建认证管理器实例。
// （NewManager creates an authentication manager instance.）
func NewManager(dataDir string) *Manager {
	return &Manager{
		dataDir:   dataDir,
		dbPath:    filepath.Join(dataDir, "auth.json"),
		rawTokens: make(map[string]string),
	}
}

// Init 初始化认证数据库（不存在时新建）。
// 若 auth.json 已存在则解析加载；解析失败时重置为空库，保证进程可继续运行。
// 加载后执行兼容迁移：默认角色 user、默认启用；首个 "admin" 用户提升为管理员。
// （Init initializes the auth database, creating it if absent. If auth.json exists it is parsed and
// loaded; on parse failure the db is reset to empty so the process can keep running.
// After loading it runs compatibility migration: default role "user", default enabled;
// the first user named "admin" is promoted to the admin role.）
func (m *Manager) Init() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	os.MkdirAll(m.dataDir, 0755)

	data, err := os.ReadFile(m.dbPath)
	if err == nil {
		m.db = &DB{}
		if e := json.Unmarshal(data, m.db); e != nil {
			// §A6 损坏保护（fail-closed）：坏文件改名保留并拒绝启动，绝不静默清空用户库——
			// 清空会让 IsInitialized 归零、/setup 重新敞开被匿名者抢占重建 admin。
			// English: corrupt auth.json is preserved (renamed) and startup FAILS instead of
			// silently wiping all accounts, which would reopen /setup for a takeover.
			ts := time.Now().Format("20060102-150405")
			_ = os.Rename(m.dbPath, m.dbPath+".corrupt-"+ts)
			return fmt.Errorf(
				"auth.json 解析失败已备份为 %s.corrupt-%s；拒绝以空库启动（防止 /setup 被抢占）。"+
					"确认备份后可删除该文件重新初始化", m.dbPath, ts)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read auth db: %w", err)
	} else {
		// 首次运行：创建空库并落盘（§A1 新库即标记当前版本，永不进兼容迁移分支）
		m.db = &DB{SchemaVersion: schemaVersion}
		m.ensureTenantsLocked()
		return m.save()
	}

	// （§MT 租户兜底放在 §A1 老迁移之后执行，见下）

	// 兼容迁移：老库没有 Role/Enabled/Perms 字段，需补齐默认值并提升 admin。
	// §A1 修复：迁移只在 SchemaVersion<1 时执行一次——此前每次启动都强制 Enabled=true，
	// SetEnabled(false) 的封禁在重启后失效。新库版本≥1 永不再进此分支。
	// English: one-time legacy migration gated by SchemaVersion so disabled accounts stay disabled.
	migrated := false
	adminPromoted := false
	if m.db.SchemaVersion < 1 {
		for i := range m.db.Users {
			u := &m.db.Users[i]
			if u.Role == "" {
				u.Role = RoleUser
				migrated = true
			}
			if !u.Enabled && u.ID != "" {
				u.Enabled = true
				migrated = true
			}
			if u.Username == "admin" && u.Role != RoleAdmin && !adminPromoted {
				u.Role = RoleAdmin
				adminPromoted = true
				migrated = true
			}
			if u.Perms == nil {
				u.Perms = []string{}
			}
		}
		// 无任何 admin 时，把最早创建的非临时用户提升为管理员，保证系统始终可管理
		// （If no admin exists, promote the earliest non-temp user so the system stays manageable.）
		if !adminPromoted {
			for i := range m.db.Users {
				u := &m.db.Users[i]
				if u.Role == RoleAdmin {
					adminPromoted = true
					break
				}
			}
		}
		if !adminPromoted {
			var earliest *User
			for i := range m.db.Users {
				u := &m.db.Users[i]
				if strings.HasPrefix(u.ID, "tmp_") || u.Username == "" {
					continue
				}
				if earliest == nil || u.CreatedAt < earliest.CreatedAt {
					earliest = u
				}
			}
			if earliest != nil {
				earliest.Role = RoleAdmin
				adminPromoted = true
				migrated = true
			}
		}
		m.db.SchemaVersion = 1
		migrated = true
	} // §A1 end SchemaVersion<1

	// §MT 租户兜底（幂等，每次加载都跑）：t_default 缺失则补建；空 TenantID 的
	// 存量账号迁入 t_default。必须在 §A1 老迁移之后执行，避免抢先升版本吞掉老迁移。
	// English: §MT tenant invariant runs after the §A1 legacy migration so it can never
	// preempt (and skip) the role/enabled promotion pass.
	if m.ensureTenantsLocked() || m.db.SchemaVersion < schemaVersion {
		m.db.SchemaVersion = schemaVersion
		migrated = true
	}
	if migrated {
		if err := m.save(); err != nil {
			return fmt.Errorf("migrate auth db: %w", err)
		}
	}
	return nil
}

// schemaVersion 当前库结构版本（§MT 起为 2）。
const schemaVersion = 2

// ensureTenantsLocked 幂等保证租户不变式（须持写锁）：
//  1. t_default 恒存在且启用（平台运营者所在租户，不可删除）；
//  2. 空 TenantID 的存量账号全部迁入 t_default（§MT 升级路径）。
//
// 返回是否发生了修改（调用方据此决定是否落盘）。
// English: idempotent tenant invariant — the default tenant always exists; legacy users with an
// empty tenant are folded into it. Reports whether anything changed so the caller can persist.
func (m *Manager) ensureTenantsLocked() bool {
	changed := false
	if m.db.Tenants == nil {
		m.db.Tenants = make(map[string]*Tenant)
	}
	if _, ok := m.db.Tenants[DefaultTenantID]; !ok {
		m.db.Tenants[DefaultTenantID] = &Tenant{
			ID: DefaultTenantID, Name: "系统租户", Enabled: true, CreatedAt: time.Now().Unix(),
		}
		changed = true
	}
	for i := range m.db.Users {
		if m.db.Users[i].TenantID == "" {
			m.db.Users[i].TenantID = DefaultTenantID
			changed = true
		}
	}
	return changed
}

// CreateTenant 新建租户（仅平台运营者路径调用）。名称去空白、租户 ID 自动生成 t_ 前缀纳秒串。
// English: creates a tenant (platform-operator path).
func (m *Manager) CreateTenant(name string, q TenantQuota) (*Tenant, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("租户名称必填")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTenantsLocked()
	for _, t := range m.db.Tenants {
		if t.Name == name {
			return nil, fmt.Errorf("租户名称已存在")
		}
	}
	t := &Tenant{
		ID: fmt.Sprintf("t_%d", time.Now().UnixNano()), Name: name,
		Enabled: true, CreatedAt: time.Now().Unix(), Quota: q,
	}
	m.db.Tenants[t.ID] = t
	if err := m.save(); err != nil {
		return nil, err
	}
	cp := *t
	return &cp, nil
}

// UpdateTenant 局部更新租户（名称/启停/配额；nil=保持原值）。t_default 禁止停用。
// English: partially updates a tenant; the default tenant can never be disabled.
func (m *Manager) UpdateTenant(id string, name *string, enabled *bool, q *TenantQuota) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.db.Tenants[id]
	if !ok {
		return fmt.Errorf("租户不存在")
	}
	if id == DefaultTenantID && enabled != nil && !*enabled {
		return fmt.Errorf("系统租户不可停用")
	}
	if name != nil {
		n := strings.TrimSpace(*name)
		if n == "" {
			return fmt.Errorf("租户名称必填")
		}
		for _, o := range m.db.Tenants {
			if o.ID != id && o.Name == n {
				return fmt.Errorf("租户名称已存在")
			}
		}
		t.Name = n
	}
	if enabled != nil {
		t.Enabled = *enabled
	}
	if q != nil {
		t.Quota = *q
	}
	return m.save()
}

// SchemaVersion 返回当前库结构版本（测试/诊断用）。
func (m *Manager) SchemaVersion() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.db.SchemaVersion
}

// TenantByID 返回租户副本（不存在返回 nil）。
func (m *Manager) TenantByID(id string) *Tenant {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.db.Tenants[id]
	if !ok {
		return nil
	}
	cp := *t
	return &cp
}

// ListTenants 返回全部租户（按创建时间升序，t_default 恒最先）。
func (m *Manager) ListTenants() []Tenant {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Tenant, 0, len(m.db.Tenants))
	for _, t := range m.db.Tenants {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].ID == DefaultTenantID) != (out[j].ID == DefaultTenantID) {
			return out[i].ID == DefaultTenantID
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// TenantOf 返回用户所属租户 ID（空/未知一律归 t_default，与 ensureTenantsLocked 口径一致）。
func (m *Manager) TenantOf(userID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.db.Users {
		if m.db.Users[i].ID == userID {
			if m.db.Users[i].TenantID == "" {
				return DefaultTenantID
			}
			return m.db.Users[i].TenantID
		}
	}
	return ""
}

// TenantName 返回租户名称（未知返回原 ID）。
func (m *Manager) TenantName(tid string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if t, ok := m.db.Tenants[tid]; ok {
		return t.Name
	}
	return tid
}

// UsersInTenant 返回指定租户的全部成员（公开视图）。
func (m *Manager) UsersInTenant(tid string) []User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]User, 0)
	for _, u := range m.db.Users {
		ut := u.TenantID
		if ut == "" {
			ut = DefaultTenantID
		}
		if ut == tid {
			out = append(out, u.PublicUser())
		}
	}
	return out
}

// countMembersLocked 统计租户内正式成员数（不含 tmp_ 临时号；须持锁）。
func (m *Manager) countMembersLocked(tid string) int {
	n := 0
	for i := range m.db.Users {
		u := &m.db.Users[i]
		if strings.HasPrefix(u.ID, "tmp_") {
			continue
		}
		ut := u.TenantID
		if ut == "" {
			ut = DefaultTenantID
		}
		if ut == tid {
			n++
		}
	}
	return n
}

// TenantMemberCount 返回租户正式成员数。
func (m *Manager) TenantMemberCount(tid string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.countMembersLocked(tid)
}

// checkTenantAdmissionLocked 新增成员前置校验（须持锁）：租户存在、启用且未满配额。
func (m *Manager) checkTenantAdmissionLocked(tid string) error {
	t, ok := m.db.Tenants[tid]
	if !ok {
		return fmt.Errorf("租户不存在: %s", tid)
	}
	if !t.Enabled {
		return ErrTenantDisabled
	}
	if m.countMembersLocked(tid) >= t.Quota.MaxUsersOr() {
		return fmt.Errorf("%w（上限 %d）", ErrTenantQuota, t.Quota.MaxUsersOr())
	}
	return nil
}

// TenantAPIRate 返回租户生效的每分钟 API 限流（未知租户按默认值）。
func (m *Manager) TenantAPIRate(tid string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if t, ok := m.db.Tenants[tid]; ok {
		return t.Quota.APIRateOr()
	}
	return defaultAPIRatePerMin
}

// IsPlatformAdmin 判断用户是否平台运营者：admin 且归属 t_default，可跨租户管理与签发租户。
// English: platform operator = an admin of the default tenant; spans tenants and can mint them.
func (m *Manager) IsPlatformAdmin(userID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.db.Users {
		if m.db.Users[i].ID == userID {
			u := &m.db.Users[i]
			tid := u.TenantID
			if tid == "" {
				tid = DefaultTenantID
			}
			return u.Role == RoleAdmin && tid == DefaultTenantID
		}
	}
	return false
}

// save 将内存数据库序列化为 JSON 并写入 auth.json。
// （save serializes the in-memory DB to JSON and writes it to auth.json.）
func (m *Manager) save() error {
	dataBytes, err := json.MarshalIndent(m.db, "", "  ")
	if err != nil {
		return err
	}
	// §A3 0600：auth.json 含令牌与密码哈希，同机其他用户不可读
	// §W3-c 统一原子写（fsync+唯一临时名）：auth.json 截断=全员无法登录，必须最坚固
	return dataio.AtomicWrite(m.dbPath, dataBytes, 0o600)
}

// tokenTTL §A3 访问令牌有效期：注册/创建默认 30 天。
// 此前 TokenExp=0 永不过期且明文存 0644 文件——一次泄漏终身有效。
// §0925EVE-W3-F（⑳/D5 注释漂移对齐）：原注释宣称「登录成功滑动续期」，与实现不符——
// ValidateToken 只校验过期、对任何有效令牌都不改 Exp（grep 无续期写路径），**不存在**
// 按请求滑动。真实语义是「定长 + 登录重签」：每次登录经 issueSession 签发**新**会话，
// 新令牌自登录时刻起固定 30 天；旧会话到期即失效不续命。效果上常用设备重复登录会各自
// 拿到新 30 天窗口（多端互不踢线，maxSessions 上限内 FIFO 淘汰），但**长期不登录的设备
// 到期必掉线**——这正是当初不选「登录轮换新令牌踢旧设备」方案的同一枚硬币。
// （不选"登录轮换新令牌"：会踢掉 APK 等其他已登录设备。）
// English: fixed 30-day TTL minted per login (issueSession), NOT sliding on requests —
// ValidateToken never rewrites Exp; a device that stops logging in expires hard at TTL.
const tokenTTL = 30 * 24 * time.Hour

// newTokenExpiry 返回新的令牌过期时间戳。
func newTokenExpiry() int64 { return time.Now().Add(tokenTTL).Unix() }

// issueSession 为用户签发一个新会话：生成原始令牌、追加会话（哈希落盘）、
// 超出 maxSessions 时淘汰最旧会话，并把最新会话同步到旧 Token/TokenExp 兼容槽位。
// 返回原始令牌（仅本次响应可见，落盘的是哈希）。
// （issueSession issues a new login session: generates a raw token, appends a hashed session,
// evicts the oldest one past maxSessions, and mirrors the newest session to the legacy slot.）
func issueSession(u *User, ttl time.Duration) (string, error) {
	raw, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	now := time.Now().Unix()
	exp := now + int64(ttl.Seconds())
	// 惰性清理：追加前先剔除已过期会话
	live := u.Sessions[:0]
	for _, s := range u.Sessions {
		if s.Exp == 0 || s.Exp > now {
			live = append(live, s)
		}
	}
	u.Sessions = append(live, Session{Token: hashToken(raw), Exp: exp, CreatedAt: now})
	// 超上限淘汰最旧（FIFO）
	if len(u.Sessions) > maxSessions {
		u.Sessions = u.Sessions[len(u.Sessions)-maxSessions:]
	}
	// 旧槽位同步最新会话（兼容读取路径与既有语义：最新签发即生效）
	u.Token = u.Sessions[len(u.Sessions)-1].Token
	u.TokenExp = exp
	return raw, nil
}

// generateToken 生成 32 字节随机令牌的十六进制字符串（密码学安全随机源）。
// （generateToken produces a hex string of a 32-byte random token from a cryptographic RNG.）
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Register 注册新用户并返回生成的用户记录。
// 用户名重复时报错；密码以 bcrypt 哈希存储，同时生成访问令牌。
// （Register creates a new user and returns the generated record. Duplicate usernames error out;
// the password is stored as a bcrypt hash and an access token is generated.）
// ErrInvalidInvite 邀请码无效/已用的哨兵错误：HTTP 层据此返回 403（与 temp 端点口径一致），
// 其余注册错误（如用户名冲突）仍走 409。
// English: sentinel for invite failures so the HTTP layer can map them to 403.
var ErrInvalidInvite = errors.New("邀请码无效或已被使用")

// Invite 邀请码：管理员签发、一次性使用；记录使用者便于审计。
// English: an invite code issued by admin, single-use, with usage audit fields.
type Invite struct {
	// 邀请码（QT+16hex）
	Code string `json:"code"`
	// 签发时间
	CreatedAt int64 `json:"created_at"`
	// 使用者账号 ID（空=未用）
	UsedBy string `json:"used_by,omitempty"`
	// 使用时间
	UsedAt int64 `json:"used_at,omitempty"`
	// §MT 签发时指定的归属租户（空=t_default）；注册者落入该租户并受其配额约束。
	TenantID string `json:"tenant_id,omitempty"`
}

// CreateInvite 签发一个 t_default 租户的邀请码（兼容既有调用方）。
func (m *Manager) CreateInvite() (string, error) {
	return m.CreateInviteFor(DefaultTenantID)
}

// CreateInviteFor §MT 为指定租户签发邀请码：先过租户准入（存在/启用/配额）。
// English: issues a single-use invite code bound to a tenant, admission-checked up front.
func (m *Manager) CreateInviteFor(tenantID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTenantsLocked()
	if tenantID == "" {
		tenantID = DefaultTenantID
	}
	if err := m.checkTenantAdmissionLocked(tenantID); err != nil {
		return "", err
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	code := "QT" + hex.EncodeToString(buf)
	if m.db.Invites == nil {
		m.db.Invites = make(map[string]*Invite)
	}
	m.db.Invites[code] = &Invite{Code: code, CreatedAt: time.Now().Unix(), TenantID: tenantID}
	if err := m.save(); err != nil {
		return "", err
	}
	return code, nil
}

// ListInvites 返回全部邀请码（含已用的，供管理页审计）。
func (m *Manager) ListInvites() []*Invite {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Invite, 0, len(m.db.Invites))
	for _, v := range m.db.Invites {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// consumeInviteLocked 校验并消费邀请码（须持写锁）：不存在/已用即拒绝。
// English: validates and consumes an invite code; caller must hold the write lock.
func (m *Manager) consumeInviteLocked(code string) error {
	if code == "" {
		return fmt.Errorf("%w: 邀请码必填", ErrInvalidInvite)
	}
	inv, ok := m.db.Invites[code]
	if !ok {
		return fmt.Errorf("%w: 无效", ErrInvalidInvite)
	}
	if inv.UsedBy != "" {
		return fmt.Errorf("%w: 已被使用", ErrInvalidInvite)
	}
	return nil
}

// markInviteUsedLocked 标记邀请码已被 userID 使用（须持写锁）。
func (m *Manager) markInviteUsedLocked(code, userID string) {
	if inv, ok := m.db.Invites[code]; ok {
		inv.UsedBy = userID
		inv.UsedAt = time.Now().Unix()
	}
}

// Register 注册永久账号。§GAP2-W2：必须携带有效且未使用的邀请码（一次性消费）。
func (m *Manager) Register(username, password, inviteCode string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// §GAP2-W2 先验邀请码（失败不落任何状态）
	if err := m.consumeInviteLocked(inviteCode); err != nil {
		return nil, err
	}

	// §MT 注册者落入邀请码绑定的租户；签发到使用之间租户可能停用/满额，此处二次准入。
	tenantID := DefaultTenantID
	if inv, ok := m.db.Invites[inviteCode]; ok && inv.TenantID != "" {
		tenantID = inv.TenantID
	}
	m.ensureTenantsLocked()
	if err := m.checkTenantAdmissionLocked(tenantID); err != nil {
		return nil, err
	}

	// 用户名唯一性校验
	for _, u := range m.db.Users {
		if u.Username == username {
			return nil, fmt.Errorf("username already exists")
		}
	}

	// 密码加盐哈希，不存明文
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	user := User{
		ID:           fmt.Sprintf("u_%d", time.Now().UnixNano()),
		Username:     username,
		PasswordHash: string(hash),
		Role:         RoleUser, // 注册用户默认为普通用户
		Enabled:      true,
		CreatedAt:    time.Now().Unix(),
		TenantID:     tenantID, // §MT 跟随邀请码租户
	}
	token, err := issueSession(&user, tokenTTL) // 首个会话（§A3 默认 30 天）
	if err != nil {
		return nil, err
	}
	m.db.Users = append(m.db.Users, user)
	m.rawTokens[user.ID] = token                // 原始令牌仅内存，供本次响应返回
	m.markInviteUsedLocked(inviteCode, user.ID) // §GAP2-W2 邀请码一次性消费
	if err := m.save(); err != nil {
		return nil, err
	}
	user.Token = token // 返回副本携带原始令牌（落盘为哈希）
	return &user, nil
}

// CreateTemp 创建有效期为 duration 的临时账户。
// 临时账户无密码（不可登录），仅持有令牌，到期后令牌校验自动失效。
// （CreateTemp creates a temporary account valid for duration. Temp accounts have no password
// (cannot log in) and only hold a token, which auto-expires for validation once lapsed.）
// CreateTemp 创建临时演示账号。§GAP2-W2：同样必须携带有效邀请码——
// 匿名领 14 天 token 是公网隔离违例的头号放大器（W1 已封 qmt report 滥用面，此处关闸门）。
func (m *Manager) CreateTemp(duration time.Duration, inviteCode string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.consumeInviteLocked(inviteCode); err != nil {
		return nil, err
	}

	// §MT 临时号跟随邀请码租户（不计入正式成员配额，但归属明确便于审计与隔离）。
	tempTenant := DefaultTenantID
	if inv, ok := m.db.Invites[inviteCode]; ok && inv.TenantID != "" {
		tempTenant = inv.TenantID
	}
	user := User{
		ID:        fmt.Sprintf("tmp_%d", time.Now().UnixNano()),
		Role:      RoleUser,
		Enabled:   true,
		CreatedAt: time.Now().Unix(),
		TenantID:  tempTenant,
	}
	token, err := issueSession(&user, duration) // 临时账户：会话有效期=账户有效期
	if err != nil {
		return nil, err
	}
	user.Username = fmt.Sprintf("temp_%s", token[:8]) // 用户名取自令牌前缀（保持既有命名规则）
	m.db.Users = append(m.db.Users, user)
	m.markInviteUsedLocked(inviteCode, user.ID) // §GAP2-W2
	m.rawTokens[user.ID] = token
	if err := m.save(); err != nil {
		return nil, err
	}
	user.Token = token // 响应返回原始令牌
	return &user, nil
}

// Login 校验用户名密码并返回对应用户。
// 临时账户无密码哈希，禁止通过密码登录。
// （Login verifies username+password and returns the matching user. Temp accounts have no password
// hash, so password login is forbidden for them.）
func (m *Manager) Login(username, password string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 按用户名定位用户：临时账号/禁用/过期直接拒绝，命中后校验密码哈希。
	for i := range m.db.Users {
		u := &m.db.Users[i]
		if u.Username == username {
			if u.PasswordHash == "" {
				return nil, fmt.Errorf("cannot login with temp account")
			}
			if !u.Enabled {
				return nil, fmt.Errorf("account disabled")
			}
			if u.expired() {
				return nil, fmt.Errorf("account expired")
			}
			if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
				return nil, fmt.Errorf("wrong password")
			}
			// §多会话：登录签发新会话而非轮换覆盖——手机/电脑可同时在线，
			// 旧令牌继续有效至其自然过期（此前"登录即踢旧端"导致另一端 401，
			// 前端拉配置失败回退缓存，表现为实盘开关"自动关闭"）。
			raw, err := issueSession(u, tokenTTL)
			if err != nil {
				return nil, err
			}
			m.rawTokens[u.ID] = raw
			if err := m.save(); err != nil {
				return nil, err
			}
			resp := *u
			resp.Token = raw
			return &resp, nil
		}
	}
	return nil, fmt.Errorf("user not found")
}

// SetupInitialAdmin §A6 原子首次初始化：检查-创建-标记在同一临界区完成，
// 杜绝 IsInitialized 与 CreateUser 之间的 TOCTOU（并发双 /setup 产生两个 admin）。
// English: atomic first-run setup — the initialized-check, admin creation and marker land in one
// critical section, closing the TOCTOU window that allowed duplicate admins.
func (m *Manager) SetupInitialAdmin(username, password string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.db.Configs {
		if c.Key == "initialized" {
			return nil, fmt.Errorf("already initialized")
		}
	}
	for i := range m.db.Users {
		if u := &m.db.Users[i]; u.Role == RoleAdmin && !strings.HasPrefix(u.ID, "tmp_") {
			return nil, fmt.Errorf("already initialized")
		}
	}
	m.ensureTenantsLocked() // §MT 保证 t_default 存在（首个 admin 归属其中=平台运营者）
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	user := User{
		ID:           fmt.Sprintf("u_%d", time.Now().UnixNano()),
		Username:     username,
		PasswordHash: string(hash),
		Role:         RoleAdmin,
		Enabled:      true,
		CreatedAt:    time.Now().Unix(),
		TenantID:     DefaultTenantID,
	}
	token, err := issueSession(&user, tokenTTL) // 首个会话
	if err != nil {
		return nil, err
	}
	m.db.Users = append(m.db.Users, user)
	m.db.Configs = append(m.db.Configs, ConfigEntry{UserID: user.ID, Key: "initialized", Value: "1"})
	m.rawTokens[user.ID] = token
	if err := m.save(); err != nil {
		return nil, err
	}
	user.Token = token // 响应返回原始令牌
	return &user, nil
}

// ValidateToken 校验令牌是否有效并返回对应未过期用户。
// 多会话：先在用户的 Sessions 列表匹配；未命中时回退旧 Token 单槽位（存量 auth.json 兼容，
// 命中后原地迁入 Sessions）。任一匹配后仍需通过会话过期、账号过期、账号启用三重校验。
// （ValidateToken checks whether a token is valid and returns the matching, still-valid user.
// It matches the per-device session list first, then falls back to the legacy single slot.）
func (m *Manager) ValidateToken(token string) *User {
	m.mu.Lock()
	defer m.mu.Unlock()

	if token == "" {
		return nil
	}
	presented := hashToken(token)
	now := time.Now().Unix()
	for i := range m.db.Users {
		u := &m.db.Users[i]

		// ── 路径一：多会话列表匹配（每个设备一条，哈希常量时间比较）──
		matched := -1
		for j := range u.Sessions {
			if subtle.ConstantTimeCompare([]byte(u.Sessions[j].Token), []byte(presented)) == 1 {
				matched = j
				break
			}
		}
		if matched >= 0 {
			s := u.Sessions[matched]
			// 会话过期：剔除并落盘，返回无效
			if s.Exp > 0 && now > s.Exp {
				u.Sessions = append(u.Sessions[:matched], u.Sessions[matched+1:]...)
				if err := m.save(); err != nil {
					log.Printf("[auth] 过期会话清理落盘失败: %v", err)
				}
				return nil
			}
			if u.expired() || !u.Enabled {
				return nil
			}
			// 内存补记原始令牌（仅空缺时，如进程重启恢复；不覆盖最近签发记录）
			if _, ok := m.rawTokens[u.ID]; !ok {
				m.rawTokens[u.ID] = token
			}
			cp := *u
			cp.Token = ""
			return &cp
		}

		// ── 路径二：旧单槽位兼容（存量明文令牌命中后原地升级哈希并迁入会话列表）──
		if u.Token == "" {
			continue
		}
		matchHash := subtle.ConstantTimeCompare([]byte(u.Token), []byte(presented)) == 1
		matchPlain := !matchHash && subtle.ConstantTimeCompare([]byte(u.Token), []byte(token)) == 1
		if matchHash || matchPlain {
			if u.TokenExp > 0 && now > u.TokenExp {
				return nil
			}
			if u.expired() || !u.Enabled {
				return nil
			}
			if matchPlain {
				u.Token = presented
			}
			// 迁入多会话列表（继承原过期时间；0=永不过期原样保留）
			u.Sessions = append(u.Sessions, Session{Token: u.Token, Exp: u.TokenExp, CreatedAt: now})
			if err := m.save(); err != nil {
				log.Printf("[auth] 令牌会话迁移落盘失败: %v", err)
			}
			// 内存补记原始令牌（仅空缺时，如进程重启恢复；不覆盖最近签发记录）
			if _, ok := m.rawTokens[u.ID]; !ok {
				m.rawTokens[u.ID] = token
			}
			cp := *u
			cp.Token = ""
			return &cp
		}
	}
	return nil
}

// UserToken 返回指定用户名当前原始令牌（仅内存缓存；进程重启后为空，需重新登录）。
// 用户不存在或无内存缓存的原始令牌时返回空串。
// （UserToken returns the current RAW token of a username from the in-memory cache only;
// after a process restart it is empty and the client must log in again.）
func (m *Manager) UserToken(username string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.db.Users {
		if u.Username == username {
			return m.rawTokens[u.ID]
		}
	}
	return ""
}

// PublicUser 返回不包含敏感字段（密码哈希/令牌/会话哈希/过期时间）的用户公开视图。
// §D1 修复：此前只清 PasswordHash+Token，Sessions 列表（含每设备令牌 SHA-256 哈希与过期时间）
// 原样序列化到 /api/admin/users → 管理员视图泄露会话凭据；配合 ValidateToken 路径二 matchPlain
// 分支存在，虽然 Sessions 里存的是哈希而非明文，但**哈希即凭据**（Presented 也是哈希比对，
// 详见 auth.go:613），拿到哈希即可回放冒用，故必须清空。
// English: PublicUser strips PasswordHash, Token, Sessions (device-token hashes) and TokenExp;
// previously Sessions leaked and could be replayed because ValidateToken hashes the presented token
// before comparing.
func (u *User) PublicUser() User {
	out := *u
	out.PasswordHash = ""
	out.Token = ""
	out.Sessions = nil
	out.TokenExp = 0
	return out
}

// HasActiveSession 报告账号是否仍有至少一条未过期会话（Exp==0 视为永不过期）。
// §U-5（2026-09-14 像素级 UAT）：temp 账号无密码，其生命周期完全由会话到期决定——
// 全部会话过期后即成"再也无法被使用"的僵尸行（长期部署 auth.json 积累数十条）。
// 清理端点以本方法判定"过期 temp"，绝不按创建时间误删仍在用的临时号。
// English: §U-5 — reports whether the account still holds at least one live session (Exp==0 =
// never expires). Password-less temp accounts live and die by their sessions; once all sessions
// lapse the row can never be used again, which is how dozens of zombie rows accumulated in
// long-lived deployments. The cleanup endpoint reaps exactly those, never a still-live temp.
func (m *Manager) HasActiveSession(userID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := time.Now().Unix()
	for _, u := range m.db.Users {
		if u.ID != userID {
			continue
		}
		for _, s := range u.Sessions {
			if s.Exp == 0 || s.Exp > now {
				return true
			}
		}
		return false
	}
	return false
}

// ListUsers 返回全部用户（公开视图，不含密码哈希与令牌）。
// （ListUsers returns all users as public views, without password hashes or tokens.）
func (m *Manager) ListUsers() []User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]User, 0, len(m.db.Users))
	for _, u := range m.db.Users {
		out = append(out, u.PublicUser())
	}
	return out
}

// UserByID 返回指定 ID 的用户（公开视图）；不存在返回 nil。
func (m *Manager) UserByID(userID string) *User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.db.Users {
		if u.ID == userID {
			pu := u.PublicUser()
			return &pu
		}
	}
	return nil
}

// RevokeSession 吊销当前令牌对应的会话（自助退出）：从 Sessions 剔除哈希命中项并同步
// 旧 Token/TokenExp 兼容槽（若命中的正是槽位会话）。找不到匹配返回 false（幂等）。
// §D7 修复：此前无自助退出——清了 localStorage 令牌仍在 Sessions 中有效，
// 手机/浏览器换设备登录后旧令牌可长期复用；退出必须回收服务端会话。
// English: RevokeSession removes the presented token's session server-side; previously logout
// was client-only and stale bearer tokens remained valid until expiry.
func (m *Manager) RevokeSession(token string) bool {
	if token == "" {
		return false
	}
	presented := hashToken(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.db.Users {
		u := &m.db.Users[i]
		for j := range u.Sessions {
			if subtle.ConstantTimeCompare([]byte(u.Sessions[j].Token), []byte(presented)) == 1 {
				u.Sessions = append(u.Sessions[:j], u.Sessions[j+1:]...)
				// 兼容槽位若指向同一条哈希则一并清空（避免 ValidateToken 路径二复活）
				if u.Token == presented {
					u.Token = ""
					u.TokenExp = 0
				}
				if err := m.save(); err != nil {
					log.Printf("[auth] 会话吊销落盘失败: %v", err)
					return false
				}
				delete(m.rawTokens, u.ID)
				return true
			}
		}
		// 旧兼容槽（尚未迁入 Sessions 的存量数据）命中：直接清
		if u.Token != "" && subtle.ConstantTimeCompare([]byte(u.Token), []byte(presented)) == 1 {
			u.Token = ""
			u.TokenExp = 0
			if err := m.save(); err != nil {
				log.Printf("[auth] 兼容槽吊销落盘失败: %v", err)
				return false
			}
			delete(m.rawTokens, u.ID)
			return true
		}
	}
	return false
}

// updateUser 按 ID 定位用户并应用变更，写入磁盘。
// （updateUser locates a user by ID, applies a mutation and persists.）
func (m *Manager) updateUser(userID string, mutate func(u *User) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.db.Users {
		if m.db.Users[i].ID == userID {
			if err := mutate(&m.db.Users[i]); err != nil {
				return err
			}
			return m.save()
		}
	}
	return fmt.Errorf("user not found")
}

// SetRole 设置用户角色（admin / user）。
// §MT 保护：t_default（系统租户）的最后一名 admin 是唯一的平台运营者，禁止降级——
// 降级成功=整个平台再无人能签发/跨租户管理，等于自锁。
func (m *Manager) SetRole(userID, role string) error {
	if role != RoleAdmin && role != RoleUser {
		return fmt.Errorf("invalid role")
	}
	return m.updateUser(userID, func(u *User) error {
		if role != RoleAdmin && u.Role == RoleAdmin && u.effectiveTenant() == DefaultTenantID {
			n := 0
			for i := range m.db.Users {
				o := &m.db.Users[i]
				if o.Role == RoleAdmin && o.effectiveTenant() == DefaultTenantID {
					n++
				}
			}
			if n <= 1 {
				return fmt.Errorf("cannot demote the last platform admin")
			}
		}
		u.Role = role
		return nil
	})
}

// effectiveTenant 用户生效租户（空=t_default，与 ensureTenantsLocked 口径一致；调用方须持锁）。
func (u *User) effectiveTenant() string {
	if u.TenantID == "" {
		return DefaultTenantID
	}
	return u.TenantID
}

// MoveUser §MT 平台运营者把用户迁移到另一租户（目标租户过准入；不可移出/迁入自己？无此限制）。
// English: platform operator moves a user across tenants with admission check on the target.
func (m *Manager) MoveUser(userID, tenantID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTenantsLocked()
	if tenantID == "" {
		tenantID = DefaultTenantID
	}
	var cur *User
	for i := range m.db.Users {
		if m.db.Users[i].ID == userID {
			cur = &m.db.Users[i]
			break
		}
	}
	if cur == nil {
		return fmt.Errorf("user not found")
	}
	if cur.effectiveTenant() == tenantID {
		return nil // 幂等
	}
	// 准入时先视当前用户已离开原租户：直接查目标租户配额即可（当前用户不在目标内）
	if err := m.checkTenantAdmissionLocked(tenantID); err != nil {
		return err
	}
	cur.TenantID = tenantID
	return m.save()
}

// GrantPerm 给用户追加一个权限位。
func (m *Manager) GrantPerm(userID, perm string) error {
	return m.updateUser(userID, func(u *User) error {
		for _, p := range u.Perms {
			if p == perm {
				return nil
			}
		}
		u.Perms = append(u.Perms, perm)
		return nil
	})
}

// RevokePerm 撤销用户的一个权限位。
func (m *Manager) RevokePerm(userID, perm string) error {
	return m.updateUser(userID, func(u *User) error {
		out := u.Perms[:0]
		for _, p := range u.Perms {
			if p != perm {
				out = append(out, p)
			}
		}
		u.Perms = out
		return nil
	})
}

// SetPerms 整体覆盖用户的权限位列表。
func (m *Manager) SetPerms(userID string, perms []string) error {
	return m.updateUser(userID, func(u *User) error {
		u.Perms = perms
		return nil
	})
}

// HasPerm 判断用户是否拥有指定权限位（管理员隐式全部）。
func (m *Manager) HasPerm(userID, perm string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.db.Users {
		if u.ID == userID {
			return u.HasPerm(perm)
		}
	}
	return false
}

// IsAdmin 判断用户是否为管理员。
func (m *Manager) IsAdmin(userID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.db.Users {
		if u.ID == userID {
			return u.Role == RoleAdmin
		}
	}
	return false
}

// ChangePassword 修改用户密码（bcrypt 哈希）并吊销全部会话：所有设备需重新登录。
func (m *Manager) ChangePassword(userID, newPassword string) error {
	if newPassword == "" {
		return fmt.Errorf("password required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return m.updateUser(userID, func(u *User) error {
		u.PasswordHash = string(hash)
		// 吊销全部会话与旧槽位令牌（密码变更后旧 token 一律失效，需重新登录）
		u.Sessions = nil
		u.Token = ""
		u.TokenExp = 0
		delete(m.rawTokens, userID)
		return nil
	})
}

// SetEnabled 启用/禁用账号；禁用即吊销全部会话（所有设备下线），重新启用后需重新登录。
func (m *Manager) SetEnabled(userID string, enabled bool) error {
	return m.updateUser(userID, func(u *User) error {
		if u.Role == RoleAdmin && !enabled {
			return fmt.Errorf("cannot disable admin")
		}
		u.Enabled = enabled
		if !enabled {
			u.Sessions = nil
			u.Token = ""
			u.TokenExp = 0
			delete(m.rawTokens, userID)
		}
		return nil
	})
}

// SetExpiry 设置账号有效期：days>0 表示 days 天后到期，0 表示永久有效。
// 管理员账号不可设置有限有效期（避免锁死系统）。
// （SetExpiry sets an account expiry: days>0 lapses the account after that many days, 0 = permanent.
// Admin accounts cannot be given a finite expiry so the system can never lock itself out.）
func (m *Manager) SetExpiry(userID string, days int) error {
	if days < 0 {
		return fmt.Errorf("invalid expiry days")
	}
	return m.updateUser(userID, func(u *User) error {
		if u.Role == RoleAdmin && days > 0 {
			return fmt.Errorf("cannot set finite expiry for admin")
		}
		if days > 0 {
			u.ExpiresAt = time.Now().AddDate(0, 0, days).Unix()
		} else {
			u.ExpiresAt = 0
		}
		return nil
	})
}

// expired 判断账号是否已过有效期（ExpiresAt=0 表示永久，永不过期）。
// （expired reports whether an account has passed its expiry; ExpiresAt=0 means permanent.）
func (u *User) expired() bool {
	return u != nil && u.ExpiresAt > 0 && time.Now().Unix() > u.ExpiresAt
}

// CreateUser 由管理员创建正式用户：指定用户名/密码/角色/权限位/有效期天数。
// expiresDays>0 时账号在该天数后到期；0 表示永久有效。
// （CreateUser lets an admin create a real user with username/password/role/perms;
// a positive expiresDays makes the account lapse after that many days, 0 = permanent.）
// CreateUser 平台口径创建用户（归属 t_default，兼容既有调用方）。
func (m *Manager) CreateUser(username, password, role string, perms []string, expiresDays int) (*User, error) {
	return m.CreateUserInTenant(DefaultTenantID, username, password, role, perms, expiresDays)
}

// CreateUserInTenant §MT 管理员开户：在指定租户内创建成员，先过租户准入
// （存在/启用/成员配额，ErrTenantQuota/ErrTenantDisabled 哨兵可 errors.Is 判别）。
// English: §MT admin-side account creation inside a tenant, subject to tenant admission.
func (m *Manager) CreateUserInTenant(tenantID, username, password, role string, perms []string, expiresDays int) (*User, error) {
	if role == "" {
		role = RoleUser
	}
	if role != RoleAdmin && role != RoleUser {
		return nil, fmt.Errorf("invalid role")
	}
	if expiresDays < 0 {
		return nil, fmt.Errorf("invalid expiry days")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureTenantsLocked()
	if tenantID == "" {
		tenantID = DefaultTenantID
	}
	if err := m.checkTenantAdmissionLocked(tenantID); err != nil {
		return nil, err
	}
	for _, u := range m.db.Users {
		if u.Username == username {
			return nil, fmt.Errorf("username already exists")
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	user := User{
		ID:           fmt.Sprintf("u_%d", time.Now().UnixNano()),
		Username:     username,
		PasswordHash: string(hash),
		Role:         role,
		Perms:        perms,
		Enabled:      true,
		CreatedAt:    time.Now().Unix(),
		TenantID:     tenantID,
	}
	if expiresDays > 0 {
		user.ExpiresAt = time.Now().AddDate(0, 0, expiresDays).Unix()
	}
	token, err := issueSession(&user, tokenTTL) // 首个会话（§A3 默认 30 天）
	if err != nil {
		return nil, err
	}
	m.db.Users = append(m.db.Users, user)
	m.rawTokens[user.ID] = token // 原始令牌仅内存（对齐 Register/Login 的哈希化口径）
	if err := m.save(); err != nil {
		return nil, err
	}
	// §GAP2-W1 返回携带原始令牌的副本：auth.json 只存哈希，调用方（如管理员开户）仍能拿到
	// 原始令牌一次性交付用户；此后 ValidateToken 只认原始值的 SHA-256。
	out := user
	out.Token = token
	return &out, nil
}

// DeleteUser 删除指定用户（管理员账号不可删除）。
// （DeleteUser removes a user by ID; the admin account cannot be deleted.）
func (m *Manager) DeleteUser(userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.db.Users {
		u := &m.db.Users[i]
		if u.ID != userID {
			continue
		}
		if u.Role == RoleAdmin {
			return fmt.Errorf("cannot delete admin")
		}
		m.db.Users = append(m.db.Users[:i], m.db.Users[i+1:]...)
		return m.save()
	}
	return fmt.Errorf("user not found")
}

// SetConfig 写入用户配置项（键不存在则新增）。
// 以 (userID, key) 为唯一维度，已存在则覆盖值，否则追加新条目并落盘。
// （SetConfig writes a user config entry (adding it when absent). Keyed uniquely by (userID, key):
// existing entries get their value overwritten, otherwise a new entry is appended and persisted.）
func (m *Manager) SetConfig(userID, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 键已存在：更新值
	for i := range m.db.Configs {
		if m.db.Configs[i].UserID == userID && m.db.Configs[i].Key == key {
			m.db.Configs[i].Value = value
			return m.save()
		}
	}
	// 键不存在：追加新配置项
	m.db.Configs = append(m.db.Configs, ConfigEntry{
		Key:    key,
		Value:  value,
		UserID: userID,
	})
	return m.save()
}

// GetConfig 读取用户配置项，不存在时返回 false。
// （GetConfig reads a user config entry; returns false when absent.）
func (m *Manager) GetConfig(userID, key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, c := range m.db.Configs {
		if c.UserID == userID && c.Key == key {
			return c.Value, true
		}
	}
	return "", false
}

// IsInitialized 判断系统是否已完成初始化。
// 存在 "initialized" 配置项或已有用户注册即视为已初始化。
// （IsInitialized reports whether the system has been initialized: an "initialized" config entry or
// at least one registered user marks it as initialized.）
func (m *Manager) IsInitialized() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, c := range m.db.Configs {
		if c.Key == "initialized" {
			return true
		}
	}
	return len(m.db.Users) > 0
}

// MarkInitialized 标记系统已完成初始化。
// （MarkInitialized marks the system as initialized.）
func (m *Manager) MarkInitialized() error {
	return m.SetConfig("system", "initialized", "true")
}

// init 设置日志格式，包含文件名与行号，便于排查问题。
// （init sets the log format to include file name and line number for easier debugging.）
func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}
