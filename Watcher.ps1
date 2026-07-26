$ScraperDir = $PSScriptRoot
$IncomingDir = Join-Path $ScraperDir "Incoming"

if (!(Test-Path $IncomingDir)) {
    New-Item -ItemType Directory -Path $IncomingDir | Out-Null
}

Write-Host "Watching: $IncomingDir"
Write-Host "Press Ctrl+C to stop."
Write-Host ""

while ($true) {

    Get-ChildItem -Path $IncomingDir -Filter "*.url" -File | ForEach-Object {

        $File = $_.FullName

        try {

            # Read URL from .url file
            $Url = Get-Content $File |
                   Where-Object { $_ -match '^URL=' } |
                   ForEach-Object { $_.Substring(4) } |
                   Select-Object -First 1

            if ($Url) {
                Start-Process `
                    -FilePath (Join-Path $ScraperDir "SGo-Scraper-master.exe") `
                    -ArgumentList $Url `
                    -WorkingDirectory $ScraperDir `
                    -Wait `
                    -NoNewWindow
            }

            Remove-Item $File -Force

        }
        catch {
            Write-Warning "Failed to process: $File"
            Write-Warning $_.Exception.Message
        }
    }

    Start-Sleep -Seconds 2
}
