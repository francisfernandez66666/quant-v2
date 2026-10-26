// 模拟盘菜单可见性口径（§PAPER-CFG-DESYNC 2026-10-26）。
//
// 为什么管理员必须始终可见：模拟盘的启用开关在模拟盘页的「⚙ 设置」里，而菜单此前只看
// 引擎的 enabled。一旦开关被关掉，菜单消失，管理员就再也进不去把开关打开（死锁）。
// 普通用户仍只在模拟盘启用时显示入口，避免无权页面的空入口。
//
// English: the admin must always keep the paper-tab entry because the enable switch lives inside
// that page; hiding it on disabled would leave no way back. Non-admins see it only when enabled.
export function shouldShowPaperTab(paperEnabled, canAdmin) {
  return !!(paperEnabled || canAdmin)
}
