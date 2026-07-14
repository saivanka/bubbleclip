@echo off
:: Installs the BubbleClip agent to run hidden at login (no window).
:: Edit SERVER and CODE below first — the code is printed in the server logs.

set SERVER=http://localhost:5678
set CODE=PUT-YOUR-CODE-HERE
set SCRIPT=%~dp0bubbleclip-agent.ps1

powershell -Command "$ws = New-Object -ComObject WScript.Shell; $sc = $ws.CreateShortcut([Environment]::GetFolderPath('Startup') + '\BubbleClip.lnk'); $sc.TargetPath = 'powershell.exe'; $sc.Arguments = '-WindowStyle Hidden -ExecutionPolicy Bypass -File \"%SCRIPT%\"'; $sc.Save()"

setx BUBBLECLIP_SERVER %SERVER% >nul
setx BUBBLECLIP_CODE %CODE% >nul
echo Installed. BubbleClip agent will start hidden at every login.
echo Starting it now...
start "" powershell -WindowStyle Hidden -ExecutionPolicy Bypass -File "%SCRIPT%"
