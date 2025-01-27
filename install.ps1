# PowerShell script to install CyberChat
$ErrorActionPreference = "Stop"

Write-Host "Installing CyberChat..." -ForegroundColor Blue

# Create temp directory
$tempDir = Join-Path $env:TEMP "cyberchat_install"
New-Item -ItemType Directory -Force -Path $tempDir | Out-Null

# Download URL
$downloadUrl = "https://raw.githubusercontent.com/RamboRogers/cyberchat/master/bins/cyberchat-windows-amd64.zip"
$zipPath = Join-Path $tempDir "cyberchat-windows-amd64.zip"

try {
    # Download the zip file
    Write-Host "Downloading CyberChat..." -ForegroundColor Blue
    Invoke-WebRequest -Uri $downloadUrl -OutFile $zipPath

    # Extract the zip
    Write-Host "Extracting files..." -ForegroundColor Blue
    Expand-Archive -Path $zipPath -DestinationPath $tempDir -Force

    # Find the exe
    $exePath = Get-ChildItem -Path $tempDir -Filter "cyberchat.exe" -Recurse | Select-Object -First 1

    # Create destination directory in user's profile
    $installDir = "$env:USERPROFILE\.cyberchat"
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null

    # Copy the exe
    Write-Host "Installing CyberChat to $installDir..." -ForegroundColor Blue
    Copy-Item -Path $exePath.FullName -Destination "$installDir\cyberchat.exe" -Force

    # Add to PATH if not already there
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath -notlike "*$installDir*") {
        Write-Host "Adding CyberChat to PATH..." -ForegroundColor Blue
        [Environment]::SetEnvironmentVariable(
            "Path",
            "$userPath;$installDir",
            "User"
        )
    }

    Write-Host "CyberChat installed successfully!" -ForegroundColor Green
    Write-Host "Please restart your terminal, then run 'cyberchat -h' to see available options." -ForegroundColor Blue
}
catch {
    Write-Host "Error installing CyberChat: $_" -ForegroundColor Red
    exit 1
}
finally {
    # Cleanup
    Remove-Item -Path $tempDir -Recurse -Force -ErrorAction SilentlyContinue
}