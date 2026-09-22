// ── ESLint 扁平配置 eslint.config.js ──
// §N-1（2026-09-22 傍晚批）前端 lint 门：一天两例「未定义符号静默蒸发」
// （Dashboard.jsx 的 setQMTState ReferenceError 被空 catch 吞 + Positions.jsx showToast 未导入）
// 是上门的实证理由。**最小集起步（owner 裁决 7）**：只开 no-undef 与 no-unused-vars 两条——
// 全量推荐集会一次爆发存量红烧掉清扫预算，故不引 eslint-plugin-react 等推荐规则。
// 取舍：no-undef 锁 error（未定义符号=运行时炸弹，正是本批事故形态）；
// no-unused-vars 降 warn（存量红多为无害死码，error 会让 lint 门首日即红、失去可用性，
// owner 预授权"若存量红过多可降 warn"）。存量计数见 §N-1 修复批报告。
// English: minimal-start lint gate for §N-1 — only no-undef (error; the silent-ReferenceError
// class this batch actually hit) and no-unused-vars (warn; too many legacy hits to gate on day 1).
import js from '@eslint/js'
import globals from 'globals'

// reactHooksStub：仓内 16 处行内 `eslint-disable(-next)-line react-hooks/exhaustive-deps`
// 指令在"未注册该插件"时会让 ESLint 直接报「Definition for rule not found」错误（门首日即红，
// 且红因与最小集承诺无关）。为满足行内指令可解析而注册一个 **no-op 桩规则**——不引入
// eslint-plugin-react-hooks 依赖、不启用任何 react-hooks 真实检查（最小集边界不破）。
// English: a no-op stub so the 16 existing inline `react-hooks/exhaustive-deps` disable
// directives resolve without adding the plugin dependency; the rule never reports.
const reactHooksStub = {
  meta: { name: 'react-hooks-stub-for-inline-directives' },
  rules: {
    'exhaustive-deps': {
      meta: { docs: { description: '§N-1 最小集占位：仅服务行内 disable 指令解析，永不上报' } },
      create: () => ({}),
    },
  },
}

// vitest globals（vitest.config.js 开了 globals:true，部分旧用例不显式 import describe/it/expect）
const vitestGlobals = {
  describe: 'readonly',
  it: 'readonly',
  test: 'readonly',
  expect: 'readonly',
  vi: 'readonly',
  beforeEach: 'readonly',
  afterEach: 'readonly',
  beforeAll: 'readonly',
  afterAll: 'readonly',
}

export default [
  {
    // 构建产物/依赖不在 lint 范围；e2e/ 走 playwright 自身体系
    ignores: ['dist/**', 'node_modules/**', 'public/**', 'e2e/**'],
  },
  {
    files: ['src/**/*.{js,jsx}'],
    languageOptions: {
      ecmaVersion: 'latest',
      sourceType: 'module',
      parserOptions: { ecmaFeatures: { jsx: true } },
      globals: {
        ...globals.browser,
        // 移动端桥（Android/iOS WebView 注入），api/native 路径使用
        Android: 'readonly',
        webkit: 'readonly',
        // Vite define 注入的构建期常量（vite.config.js §A7 build_commit 下发）
        __BUILD_COMMIT__: 'readonly',
      },
    },
    plugins: { 'react-hooks': reactHooksStub },
    rules: {
      'no-undef': 'error',
      // React 自动 JSX runtime（@vitejs/plugin-react）下 `import React` 全仓必判 unused，
      // 无 react 插件无从识别 JSX 引用，以 varsIgnorePattern 单点豁免，不改最小集承诺；
      // caughtErrors 保持默认 all：catch(e){} 吞错形参计入 warn，正对本批「空 catch 掩盖炸弹」形态。
      'no-unused-vars': ['warn', { varsIgnorePattern: '^React$', args: 'after-used' }],
    },
  },
  {
    files: ['src/__tests__/**/*.{js,jsx}'],
    languageOptions: {
      globals: {
        ...globals.browser,
        ...vitestGlobals,
        // 测试里惯用 global.fetch = vi.fn() 挂桩（vitest node/jsdom 混跑环境存在 global 别名）
        global: 'readonly',
      },
    },
  },
]
