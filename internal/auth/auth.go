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
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Token     string `json:"token,omitempty"`
	TokenExp  int64  `json:"token_exp,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// ConfigEntry 用户配置键值项。
type ConfigEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	UserID string `json:"user_id"`
}

// DB 认证数据库结构（用户与配置列表）。
type DB struct {
	Users   []User         `json:"users"`
	Configs []ConfigEntry  `json:"configs"`
}

// Manager 用户与登录认证管理器。
type Manager struct {
	mu       sync.RWMutex
	dataDir  string
	dbPath   string
	db       *DB
}

// NewManager 创建认证管理器实例。
func NewManager(dataDir string) *Manager {
	return &Manager{
		dataDir: dataDir,
		dbPath:  filepath.Join(dataDir, "auth.json"),
	}
}

// Init 初始化认证数据库（不存在时新建）。
func (m *Manager) Init() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	os.MkdirAll(m.dataDir, 0755)

	data, err := os.ReadFile(m.dbPath)
	if err == nil {
		m.db = &DB{}
		if e := json.Unmarshal(data, m.db); e != nil {
			m.db = &DB{}
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("read auth db: %w", err)
	}

	m.db = &DB{}
	return m.save()
}

func (m *Manager) save() error {
	data, err := json.MarshalIndent(m.db, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.dbPath, data, 0644)
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Register 注册新用户并返回生成的用户记录。
func (m *Manager) Register(username, password string) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, u := range m.db.Users {
		if u.Username == username {
			return nil, fmt.Errorf("username already exists")
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	token, err := generateToken()
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	user := User{
		ID:       fmt.Sprintf("u_%d", time.Now().UnixNano()),
		Username: username,
		PasswordHash: string(hash),
		Token:    token,
		TokenExp: 0,
		CreatedAt: time.Now().Unix(),
	}
	m.db.Users = append(m.db.Users, user)
	if err := m.save(); err != nil {
		return nil, err
	}
	return &user, nil
}

// CreateTemp 创建有效期为 duration 的临时账户。
func (m *Manager) CreateTemp(duration time.Duration) (*User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	token, err := generateToken()
	if err != nil {
		return nil, err
	}

	user := User{
		ID:       fmt.Sprintf("tmp_%d", time.Now().UnixNano()),
		Username: fmt.Sprintf("temp_%s", token[:8]),
		Token:    token,
		TokenExp: time.Now().Add(duration).Unix(),
		CreatedAt: time.Now().Unix(),
	}
	m.db.Users = append(m.db.Users, user)
	if err := m.save(); err != nil {
		return nil, err
	}
	return &user, nil
}

// Login 校验用户名密码并返回对应用户。
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

// SetConfig 写入用户配置项（键不存在则新增）。
func (m *Manager) SetConfig(userID, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.db.Configs {
		if m.db.Configs[i].UserID == userID && m.db.Configs[i].Key == key {
			m.db.Configs[i].Value = value
			return m.save()
		}
	}
	m.db.Configs = append(m.db.Configs, ConfigEntry{
		Key:    key,
		Value:  value,
		UserID: userID,
	})
	return m.save()
}

// GetConfig 读取用户配置项，不存在时返回 false。
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
func (m *Manager) MarkInitialized() error {
	return m.SetConfig("system", "initialized", "true")
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}
