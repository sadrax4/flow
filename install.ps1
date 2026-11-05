# Install or update Teleprompter for the current user (no admin rights needed):
#   irm https://raw.githubusercontent.com/sadrax4/flow/main/install.ps1 | iex
& {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $name = 'Teleprompter'
    $version = '1.1.0'
    $url = 'https://raw.githubusercontent.com/sadrax4/flow/main/Teleprompter.exe'
    $dir = Join-Path $env:LOCALAPPDATA "Programs\$name"
    $exe = Join-Path $dir "$name.exe"
    $links = @(
        (Join-Path ([Environment]::GetFolderPath('Programs')) "$name.lnk"),
        (Join-Path ([Environment]::GetFolderPath('Desktop')) "$name.lnk")
    )
    $regKey = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\$name"
    function Quote($s) { "'" + ($s -replace "'", "''") + "'" }

    Write-Host "Installing $name..."
    Get-Process $name -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Milliseconds 300
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Invoke-WebRequest -Uri $url -OutFile "$exe.download" -UseBasicParsing
    # A just-closed instance can keep the old exe locked for a moment.
    for ($i = 1; ; $i++) {
        try { Move-Item -Force "$exe.download" $exe; break }
        catch { if ($i -ge 10) { throw }; Start-Sleep -Milliseconds 300 }
    }

    $shell = New-Object -ComObject WScript.Shell
    foreach ($link in $links) {
        $shortcut = $shell.CreateShortcut($link)
        $shortcut.TargetPath = $exe
        $shortcut.WorkingDirectory = $dir
        $shortcut.Save()
    }

    $uninstaller = Join-Path $dir 'uninstall.ps1'
    @(
        "Get-Process $name -ErrorAction SilentlyContinue | Stop-Process -Force"
        "Start-Sleep -Milliseconds 300"
        "Remove-Item -Force -ErrorAction SilentlyContinue $(Quote $links[0]), $(Quote $links[1])"
        "Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $(Quote $regKey)"
        "Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $(Quote $dir)"
    ) | Set-Content -Encoding UTF8 -Path $uninstaller

    New-Item -Force -Path $regKey | Out-Null
    $values = @{
        DisplayName     = $name
        DisplayVersion  = $version
        DisplayIcon     = $exe
        Publisher       = 'sadrax4'
        InstallLocation = $dir
        UninstallString = "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$uninstaller`""
    }
    foreach ($key in $values.Keys) {
        New-ItemProperty -Path $regKey -Name $key -Value $values[$key] -PropertyType String -Force | Out-Null
    }
    New-ItemProperty -Path $regKey -Name NoModify -Value 1 -PropertyType DWord -Force | Out-Null
    New-ItemProperty -Path $regKey -Name NoRepair -Value 1 -PropertyType DWord -Force | Out-Null

    Start-Process $exe
    Write-Host "$name $version installed. Start menu and desktop shortcuts created."
    Write-Host "Uninstall from Settings > Apps, or run: powershell -File `"$uninstaller`""
}
