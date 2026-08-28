// ── 全局错误边界组件 ErrorBoundary.jsx ──
// 用途：捕获其子树（即包裹的路由/页面内容）在渲染期间抛出的 JavaScript 错误，
// 防止单个页面组件崩溃导致整页白屏（React 错误会向上冒泡直到被边界捕获或白屏）。
// 这是 React 官方推荐的"兜底"机制：类组件可通过生命周期捕获渲染期错误。
//
// Props：
//   children  {ReactNode}            被保护的子树（通常为整个路由出口内容）
//   fallback  {ReactNode|Function}   可选，自定义兜底 UI；不传则使用内置中文兜底界面
//
// 状态：
//   hasError  {boolean}  是否已进入错误状态
//   error     {Error}    捕获到的错误信息对象
import React from 'react'

/**
 * 全局错误边界类组件
 * 通过 componentDidCatch 捕获子树渲染异常，并通过 getDerivedStateFromError 切换到兜底 UI。
 */
export default class ErrorBoundary extends React.Component {
  // 初始化状态：尚未出错
  constructor(props) {
    super(props)
    // hasError=false 表示当前处于正常渲染；error=null 暂未捕获到错误
    this.state = { hasError: false, error: null }
  }

  // 静态方法：当子树抛出错误时由 React 调用，返回需要合并进 state 的更新
  // 用于把组件切换为"已出错"状态并保存错误对象，以便渲染兜底界面
  static getDerivedStateFromError(error) {
    // 返回的新状态会覆盖/合并进当前 state，触发重新渲染（进入兜底 UI）
    return { hasError: true, error }
  }

  // 生命周期：错误被捕获后回调，可用于上报日志；此处仅打印到控制台便于排查
  componentDidCatch(error, errorInfo) {
    // 打印错误堆栈与组件栈信息，方便本地开发定位问题
    console.error('ErrorBoundary 捕获到渲染错误:', error, errorInfo)
  }

  // 点击"刷新重试"时重置错误状态并尝试重新渲染子树；失败仍会再次被捕获
  handleReload = () => {
    // 清空错误状态，给子树一次重新渲染的机会
    this.setState({ hasError: false, error: null })
    // 若提供自定义刷新回调则执行，否则回退到整页刷新
    if (typeof this.props.onReset === 'function') {
      this.props.onReset()
    } else {
      window.location.reload()
    }
  }

  render() {
    // 若已捕获错误，渲染兜底 UI（优先使用外部传入的 fallback）
    if (this.state.hasError) {
      // 支持 fallback 为函数（接收 error 参数）或普通节点
      if (this.props.fallback) {
        if (typeof this.props.fallback === 'function') {
          return this.props.fallback(this.state.error)
        }
        return this.props.fallback
      }

      // 内置中文兜底界面：提示页面出错并提供刷新入口
      return (
        <div
          style={{
            padding: 32,
            textAlign: 'center',
            fontFamily: 'system-ui, -apple-system, sans-serif',
            color: '#333',
            minHeight: '60vh',
            display: 'flex',
            flexDirection: 'column',
            justifyContent: 'center',
            alignItems: 'center',
            gap: 16,
          }}
        >
          <h2 style={{ margin: 0, fontSize: 20 }}>页面出错了，请刷新</h2>
          <p style={{ margin: 0, color: '#888', fontSize: 13, maxWidth: 480, wordBreak: 'break-all' }}>
            {(this.state.error && this.state.error.message) || '发生未知错误'}
          </p>
          <button
            onClick={this.handleReload}
            style={{
              padding: '8px 20px',
              border: 'none',
              borderRadius: 6,
              background: '#0052d9',
              color: '#fff',
              fontSize: 14,
              cursor: 'pointer',
            }}
          >
            刷新重试
          </button>
        </div>
      )
    }

    // 正常状态：直接渲染受保护的子树
    return this.props.children
  }
}
