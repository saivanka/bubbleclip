# BubbleClip Windows agent — headless two-way clipboard sync.
#   Ctrl+C anywhere on Windows → sent to the network
#   Copy on any other device   → lands in this PC's clipboard + toast notification
#
# Run:   powershell -ExecutionPolicy Bypass -File bubbleclip-agent.ps1
# Env:   BUBBLECLIP_SERVER, BUBBLECLIP_CODE (access code from server logs),
#        BUBBLECLIP_DEVICE, BUBBLECLIP_ASK_SEND=1 (confirm dialog before sending)

$Server   = if ($env:BUBBLECLIP_SERVER)  { $env:BUBBLECLIP_SERVER }  else { "http://localhost:8080" }
$Code     = if ($env:BUBBLECLIP_CODE)    { $env:BUBBLECLIP_CODE }    else { "" }
$Device   = if ($env:BUBBLECLIP_DEVICE)  { $env:BUBBLECLIP_DEVICE }  else { $env:COMPUTERNAME }
$Interval = if ($env:BUBBLECLIP_INTERVAL){ [int]$env:BUBBLECLIP_INTERVAL } else { 1 }
$AskSend  = $env:BUBBLECLIP_ASK_SEND -eq "1"

$Headers  = @{ "X-Access-Code" = $Code }

Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

$notifyIcon = New-Object System.Windows.Forms.NotifyIcon
$notifyIcon.Icon = [System.Drawing.SystemIcons]::Information
$notifyIcon.Visible = $true
$notifyIcon.Text = "BubbleClip agent"

function Show-Toast($title, $msg) {
    $notifyIcon.BalloonTipTitle = $title
    $notifyIcon.BalloonTipText  = if ($msg) { $msg } else { " " }
    $notifyIcon.ShowBalloonTip(3000)
}

function Get-Remote {
    try {
        $r = Invoke-WebRequest -Uri "$Server/api/clipboard?plain=1" -Headers $Headers -UseBasicParsing -TimeoutSec 5
        return @{
            Text   = [System.Text.Encoding]::UTF8.GetString($r.RawContentStream.ToArray())
            Id     = "$($r.Headers['X-Id'])"
            Device = "$($r.Headers['X-Device'])"
        }
    } catch { return $null }
}

function Send-Clip($text) {
    try {
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($text)
        Invoke-RestMethod -Uri "$Server/api/clipboard?device=$([uri]::EscapeDataString($Device))" -Headers $Headers `
            -Method Post -Body $bytes -ContentType "text/plain; charset=utf-8" -TimeoutSec 5 | Out-Null
        return $true
    } catch { return $false }
}

function Get-LocalClip {
    try { $t = Get-Clipboard -Raw -ErrorAction SilentlyContinue; if ($null -eq $t) { "" } else { $t } }
    catch { "" }
}

Write-Host "BubbleClip agent -> $Server (device: $Device, ask-send: $AskSend)"

# fail fast on a bad access code so we're not silently out of sync
try {
    Invoke-WebRequest -Uri "$Server/api/clipboard" -Headers $Headers -UseBasicParsing -TimeoutSec 5 | Out-Null
} catch {
    $status = $_.Exception.Response.StatusCode.value__
    if ($status -eq 401 -or $status -eq 429) {
        Write-Host "ERROR: server rejected the access code (HTTP $status)."
        Write-Host "Set BUBBLECLIP_CODE to the code shown in the server logs."
        exit 1
    }
}

$last = ""; $lastId = ""

$init = Get-Remote
if ($init) {
    $last = $init.Text; $lastId = $init.Id
    if ($init.Text) { Set-Clipboard -Value $init.Text }
    Write-Host "Connected. Synced current clipboard."
} else {
    Write-Host "WARNING: cannot reach $Server yet — will keep retrying."
}

while ($true) {
    $local = Get-LocalClip

    if ($local -ne $last -and $local -ne "") {
        # ---- local Ctrl+C detected ----
        $doSend = $true
        if ($AskSend) {
            $preview = if ($local.Length -gt 120) { $local.Substring(0,120) + "..." } else { $local }
            $answer = [System.Windows.Forms.MessageBox]::Show(
                "Copy action detected — send to network?`n`n$preview",
                "BubbleClip", [System.Windows.Forms.MessageBoxButtons]::YesNo,
                [System.Windows.Forms.MessageBoxIcon]::Question)
            $doSend = $answer -eq [System.Windows.Forms.DialogResult]::Yes
        }
        if ($doSend) {
            if (Send-Clip $local) { Show-Toast "BubbleClip" "Sent to network" }
            else { Show-Toast "BubbleClip" "Send failed — server unreachable" }
        }
        $last = $local
        $r = Get-Remote; if ($r) { $lastId = $r.Id }
    }
    else {
        # ---- check for copies from other devices ----
        $r = Get-Remote
        if ($r -and $r.Id -and $r.Id -ne $lastId) {
            $lastId = $r.Id
            if ($r.Text -ne $last -and $r.Text) {
                Set-Clipboard -Value $r.Text
                $last = $r.Text
                Show-Toast "Copy detected on $($r.Device)" "Captured — press Ctrl+V to paste"
            }
        }
    }

    Start-Sleep -Seconds $Interval
}
