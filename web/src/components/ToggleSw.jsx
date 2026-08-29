// 纯受控开关：视觉与交互 100% 由外部 checked 决定，点击仅回传一次新值（!checked）。
//
// 为何不用 TDesign 的 Switch：tdesign-react v1.18.2 的 Switch 在受控模式（传 value）下，
// 内部 innerChecked 不会随 value 重新同步；且一次点击会触发两次 onChange
// （一次 false 一次 true），表现为“关不掉 / 关了又开”。自实现可彻底规避该问题。
import React from 'react'

export default function ToggleSw({ checked, onChange, disabled }) {
  const on = !!checked
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      disabled={disabled}
      onClick={() => { if (!disabled) onChange(!on) }}
      style={{
        position: 'relative',
        width: 46,
        height: 24,
        borderRadius: 12,
        border: 'none',
        padding: 0,
        background: on ? '#00a870' : '#c0c4cc',
        cursor: disabled ? 'not-allowed' : 'pointer',
        transition: 'background 0.2s',
        flex: 'none',
        verticalAlign: 'middle',
      }}
    >
      <span
        style={{
          position: 'absolute',
          top: 3,
          left: on ? 23 : 3,
          width: 18,
          height: 18,
          borderRadius: '50%',
          background: '#fff',
          boxShadow: '0 1px 2px rgba(0,0,0,0.3)',
          transition: 'left 0.2s',
        }}
      />
    </button>
  )
}
