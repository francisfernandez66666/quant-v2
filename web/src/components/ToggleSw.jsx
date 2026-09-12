// 纯受控开关：视觉与交互 100% 由外部 checked 决定，点击仅回传一次新值（!checked）。
//
// 为何不用 TDesign 的 Switch：tdesign-react v1.18.2 的 Switch 在受控模式（传 value）下，
// 内部 innerChecked 不会随 value 重新同步；且一次点击会触发两次 onChange
// （一次 false 一次 true），表现为“关不掉 / 关了又开”。自实现可彻底规避该问题。
import React from 'react'

/**
 * 纯受控开关组件
 * @param {{checked:boolean, onChange:Function, disabled:boolean}} props
 * @returns {JSX.Element}
 */
export default function ToggleSw({ checked, onChange, disabled }) {
  // 纯受控开关：渲染状态完全由 checked 决定，点击回传新值
  // 转为布尔：外部传入 undefined/null 时按「关」处理
  const on = !!checked
  return (
    // 按钮元素 + role="switch" + aria-checked：兼顾可访问性（读屏器可识别开关状态）
    <button
      type="button"
      role="switch"
      aria-checked={on}
      disabled={disabled}
      // 点击只回传一次新值（!on），状态翻转交给外部受控方，避免双重触发
      onClick={() => { if (!disabled) onChange(!on) }}
      // 按钮壳样式：固定 46×24 圆角胶囊，开=绿底、关=灰底，带 0.2s 背景过渡
      style={{
        // 胶囊外形：固定尺寸 + 大圆角，去默认边框内边距
        position: 'relative',
        width: 46,
        height: 24,
        borderRadius: 12,
        border: 'none',
        padding: 0,
        // 背景色即开关状态色：开=绿色、关=灰色；悬停指针/禁用指针区分交互态
        background: on ? 'var(--app-down)' : 'var(--app-faint)',
        cursor: disabled ? 'not-allowed' : 'pointer',
        transition: 'background 0.2s',
        // flex:none：不被父级 flex 布局拉伸；inline-block 对齐基线
        flex: 'none',
        verticalAlign: 'middle',
      }}
    >
      {
        // 滑块圆点：白色小圆，开时右移（left 23px）、关时左移（left 3px），带 0.2s 位移过渡
      }
      <span
        style={{
          // 白色圆形滑块：18×18 圆点，绝对定位于胶囊内（顶部留 3px）
          position: 'absolute',
          top: 3,
          // 左侧位移：开=23px（右侧）、关=3px（左侧），translate 过渡即滑动动画
          left: on ? 23 : 3,
          width: 18,
          height: 18,
          // 视觉细节：正圆 + 纯白 + 轻投影增强立体感
          borderRadius: '50%',
          background: '#fff',
          boxShadow: '0 1px 2px rgba(0,0,0,0.3)',
          transition: 'left 0.2s',
        }}
      />
    </button>
  )
}
