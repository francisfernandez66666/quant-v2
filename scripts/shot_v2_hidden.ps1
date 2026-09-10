# shot_v2_hidden.ps1 - screenshot + window list, no console popup (ASCII source)
$code = @'
using System; using System.Text; using System.Runtime.InteropServices;
public static class S6 {
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc cb, IntPtr l);
  [DllImport("user32.dll")] public static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll")] public static extern int GetClassName(IntPtr h, StringBuilder s, int n);
  public delegate bool EnumProc(IntPtr h, IntPtr l);
  public static string T(IntPtr h) { var s = new StringBuilder(256); GetWindowText(h, s, 256); return s.ToString(); }
  public static string C(IntPtr h) { StringBuilder s = new StringBuilder(256); GetClassName(h, s, 256); return s.ToString(); }
}
'@
Add-Type -TypeDefinition $code
$lines = New-Object System.Collections.Generic.List[string]
[S6]::EnumWindows([S6+EnumProc]{ param($h, $l)
    if ([S6]::IsWindowVisible($h) -and [S6]::T($h)) { $lines.Add(('[' + [S6]::C($h) + '] ' + [S6]::T($h))) }
    return $true
}, [IntPtr]::Zero) | Out-Null
$lines | Set-Content -Encoding UTF8 C:\qmt\tops_now.txt
Add-Type -AssemblyName System.Windows.Forms, System.Drawing
$b = [System.Windows.Forms.SystemInformation]::VirtualScreen
$bmp = New-Object System.Drawing.Bitmap $b.Width, $b.Height
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.CopyFromScreen($b.X, $b.Y, 0, 0, $bmp.Size)
$bmp.Save('C:\qmt\ui_one.png')
$bmp.Dispose()
Set-Content C:\qmt\shot_done.txt OK
