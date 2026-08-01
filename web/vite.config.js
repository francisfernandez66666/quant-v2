// ── Vite 构建配置 vite.config.js ──
// 负责前端项目的构建与本地开发服务器配置
//
// 职责说明：
// 1. 集成 Vue 3 插件（@vitejs/plugin-vue），使 Vite 能够编译 .vue 单文件组件；
// 2. 配置开发服务器端口（5173）与代理转发规则，实现前后端联调：
//    - /api 前缀请求转发到 Go 后端的 REST 接口（http://localhost:8080）；
//    - /ws 前缀请求转发到 Go 后端的 WebSocket 连接（ws://localhost:8080）。
//
// 代理原理：开发模式下若浏览器直连后端会触发跨域限制，Vite 通过本地开发
// 服务器中转，将匹配前缀的请求转发到后端，从而规避 CORS 并隐藏真实后端地址。
// 生产构建不受本配置影响，打包产物直接对接用户配置的服务器地址。

// 导入 Vite 配置工厂函数与 Vue 官方插件
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 导出 Vite 配置对象（defineConfig 自带类型提示与配置校验）
export default defineConfig({
  // 插件列表：vue() 负责解析与编译 .vue 单文件组件（含模板与 <script setup>）
  plugins: [vue()],
  server: {
    // 开发服务器端口：本地访问地址为 http://localhost:5173
    port: 5173,
    proxy: {
      // REST API 代理：以 /api 开头的请求转发至本地 Go 后端
      // changeOrigin: true 重写请求头中的 Host，使后端认为请求来自同源
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
      // WebSocket 代理：以 /ws 开头的连接转发至后端（ws: true 开启 WebSocket 代理支持）
      '/ws': { target: 'ws://localhost:8080', ws: true },
    }
  }
})
