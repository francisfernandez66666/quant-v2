// Package auth 提供用户认证与配置管理：
// 用户注册/登录、令牌生成与校验、临时账户、以及用户级 key-value 配置读写，
// 数据持久化在数据目录下的 auth.json 文件中。
// （Package auth provides user authentication and config management: register/login, token generation
// and validation, temporary accounts, and per-user key-value config storage, persisted to auth.json.）
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User 用户账号记录。
// （User is a user account record.）
type User struct {
	ID           string `json:"id"`                  // 用户唯一标识（u_ 前缀）
	Username     string `json:"username"`            // 登录用户名
	PasswordHash string `json:"password_hash"`       // bcrypt 密码哈希（临时账户为空）
	Token        string `json:"token,omitempty"`     // 认证令牌
	TokenExp     int64  `json:"token_exp,omitempty"` // 令牌过期 Unix 时间戳（0 表示永不过期）
	CreatedAt    int64  `json:"created_at"`          // 创建时间 Unix 时间戳
}

// ConfigEntry 用户配置键值项。
// （ConfigEntry is a user-config key-value entry.）
type ConfigEntry struct {
	Key    string `json:"key"`     // 配置键名
	Value  string `json:"value"`   // 配置值
	UserID string `json:"user_id"` // 所属用户 ID（系统级配置用 "system"）
}

// DB 认证数据库结构（用户与配置列表）。
// （DB is the authentication database structure: user and config lists.）
type DB struct {
	Users   []User        `json:"users"`   // 全部用户列表
	Configs []ConfigEntry `json:"configs"` // 用户/系统级配置项列表
}

// Manager 用户与登录认证管理器。
// （Manager is the user & login authentication manager.）
type Manager struct {
	mu      sync.RWMutex // 并发读写锁，保护 db
	dataDir string       // 数据存储目录
	dbPath  string       // auth.json 数据库文件路径
	db      *DB          // 内存中的认证数据库
}

// NewManager 创建认证管理器实例。
// （NewManager creates an authentication manager instance.）
func NewManager(dataDir string) *Manager {
	return &Manager{
		dataDir: dataDir,
		dbPath:  filepath.Join(dataDir, "auth.json"),
	}
}

// Init 初始化认证数据库（不存在时新建）。
// 若 auth.json 已存在则解析加载；解析失败时重置为空库，保证进程可继续运行。
// （Init initializes the auth database, creating it if absent. If auth.json exists it is parsed and
// loaded; on parse failure the db is reset to empty so the process can keep running.）
func (m *Manager) Init() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	os.MkdirAll(m.dataDir, 0755)

	data, err := os.ReadFile(m.dbPath)
	if err == nil {
		m.db = &DB{}
		if e := json.Unmarshal(data, m.db); e != nil {
			// 数据库文件损坏：回退为空库，避免启动崩溃
			m.db = &DB{}
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("read auth db: %w", err)
	}

	// 首次运行：创建空库并落盘
	m.db = &DB{}
	return m.save()
}

// save 将内存数据库序列化为 JSON 并写入 auth.json。
// （save serializes the in-memory DB to JSON and writes it to auth.json.）
func (m *Manager) save() error {
	data, err := json.MarshalIndent(m.db, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.dbPath, data, 0644)
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
func (m *Manager) Register(username, password string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

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

	token, err := generateToken()
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	user := User{
		ID:           fmt.Sprintf("u_%d", time.Now().UnixNano()),
		Username:     username,
		PasswordHash: string(hash),
		Token:        token,
		TokenExp:     0, // 0 表示令牌永不过期
		CreatedAt:    time.Now().Unix(),
	}
	m.db.Users = append(m.db.Users, user)
	if err := m.save(); err != nil {
		return nil, err
	}
	return &user, nil
}

// CreateTemp 创建有效期为 duration 的临时账户。
// 临时账户无密码（不可登录），仅持有令牌，到期后令牌校验自动失效。
// （CreateTemp creates a temporary account valid for duration. Temp accounts have no password
// (cannot log in) and only hold a token, which auto-expires for validation once lapsed.）
func (m *Manager) CreateTemp(duration time.Duration) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	token, err := generateToken()
	if err != nil {
		return nil, err
	}

	user := User{
		ID:        fmt.Sprintf("tmp_%d", time.Now().UnixNano()),
		Username:  fmt.Sprintf("temp_%s", token[:8]),
		Token:     token,
		TokenExp:  time.Now().Add(duration).Unix(), // 令牌过期时间
		CreatedAt: time.Now().Unix(),
	}
	m.db.Users = append(m.db.Users, user)
	if err := m.save(); err != nil {
		return nil, err
	}
	return &user, nil
}

// Login 校验用户名密码并返回对应用户。
// 临时账户无密码哈希，禁止通过密码登录。
// （Login verifies username+password and returns the matching user. Temp accounts have no password
// hash, so password login is forbidden for them.）
func (m *Manager) Login(username, password string) (*User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, u := range m.db.Users {
		if u.Username == username {
			if u.PasswordHash == "" {
				return nil, fmt.Errorf("cannot login with temp account")
			}
			if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
				return nil, fmt.Errorf("wrong password")
			}
			return &u, nil
		}
	}
	return nil, fmt.Errorf("user not found")
}

// ValidateToken 校验令牌是否有效并返回对应未过期用户。
// TokenExp>0 且已过期时视为无效，返回 nil。
// （ValidateToken checks whether a token is valid and returns the matching, still-valid user.
// If TokenExp>0 and the expiry has passed, it returns nil.）
func (m *Manager) ValidateToken(token string) *User {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, u := range m.db.Users {
		if u.Token == token {
			if u.TokenExp > 0 && time.Now().Unix() > u.TokenExp {
				return nil
			}
			return &u
		}
	}
	return nil
}

// UserToken 返回指定用户名当前 token；用户不存在或无 token 时返回空串。
// （UserToken returns the current token of a username, or an empty string if the user is missing or has none.）
func (m *Manager) UserToken(username string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.db.Users {
		if u.Username == username {
			return u.Token
		}
	}
	return ""
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
